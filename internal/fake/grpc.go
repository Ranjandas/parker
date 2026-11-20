package fake

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protowire"
)

// gRPC: fake-service's FakeService.Handle (grpc/protos/api.proto: Request
// { bytes data = 1 }, Response { string Message = 1 }, the Message holding
// the JSON answer) and grpc.health.v1.Health/Check. The messages are tiny,
// so they are encoded by hand rather than generated.
const (
	procHandle      = "/FakeService/Handle"
	procHealthCheck = "/grpc.health.v1.Health/Check"
)

type handleRequest struct{ Data []byte }
type handleResponse struct{ Message string }
type healthRequest struct{ Service string }
type healthResponse struct{ Status int32 }

// The grpc.health.v1 serving statuses.
const (
	healthServing    = 1
	healthNotServing = 2
)

// wireCodec is a protobuf codec for the four messages above.
type wireCodec struct{}

func (wireCodec) Name() string { return "proto" }

func (wireCodec) Marshal(m any) ([]byte, error) {
	var b []byte
	switch v := m.(type) {
	case *handleRequest:
		if len(v.Data) > 0 {
			b = protowire.AppendTag(b, 1, protowire.BytesType)
			b = protowire.AppendBytes(b, v.Data)
		}
	case *handleResponse:
		if v.Message != "" {
			b = protowire.AppendTag(b, 1, protowire.BytesType)
			b = protowire.AppendString(b, v.Message)
		}
	case *healthRequest:
		if v.Service != "" {
			b = protowire.AppendTag(b, 1, protowire.BytesType)
			b = protowire.AppendString(b, v.Service)
		}
	case *healthResponse:
		if v.Status != 0 {
			b = protowire.AppendTag(b, 1, protowire.VarintType)
			b = protowire.AppendVarint(b, uint64(v.Status))
		}
	default:
		return nil, fmt.Errorf("fake grpc: can't marshal %T", m)
	}
	return b, nil
}

func (wireCodec) Unmarshal(b []byte, m any) error {
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return protowire.ParseError(n)
		}
		b = b[n:]
		if num == 1 {
			switch v := m.(type) {
			case *handleRequest, *handleResponse, *healthRequest:
				if typ != protowire.BytesType {
					return errors.New("fake grpc: field 1: want bytes")
				}
				val, n := protowire.ConsumeBytes(b)
				if n < 0 {
					return protowire.ParseError(n)
				}
				b = b[n:]
				switch v := v.(type) {
				case *handleRequest:
					v.Data = append([]byte(nil), val...)
				case *handleResponse:
					v.Message = string(val)
				case *healthRequest:
					v.Service = string(val)
				}
				continue
			case *healthResponse:
				if typ != protowire.VarintType {
					return errors.New("fake grpc: field 1: want varint")
				}
				val, n := protowire.ConsumeVarint(b)
				if n < 0 {
					return protowire.ParseError(n)
				}
				b = b[n:]
				v.Status = int32(val)
				continue
			default:
				return fmt.Errorf("fake grpc: can't unmarshal %T", m)
			}
		}
		n = protowire.ConsumeFieldValue(num, typ, b)
		if n < 0 {
			return protowire.ParseError(n)
		}
		b = b[n:]
	}
	return nil
}

// grpcHandlers mounts FakeService.Handle and the health check on mux.
func (s *Server) grpcHandlers(mux *http.ServeMux) {
	opts := []connect.HandlerOption{connect.WithCodec(wireCodec{})}
	mux.Handle(procHandle, connect.NewUnaryHandler(procHandle,
		func(ctx context.Context, req *connect.Request[handleRequest]) (*connect.Response[handleResponse], error) {
			r, err := http.NewRequestWithContext(ctx, http.MethodGet, procHandle, nil)
			if err != nil {
				return nil, err
			}
			r.Header = req.Header().Clone()
			r.RemoteAddr = req.Peer().Addr
			resp, id := s.handle(r, "gRPC")
			out := connect.NewResponse(&handleResponse{Message: mustJSON(resp)})
			out.Header().Set(HeaderRequestID, id)
			if resp.Code >= 400 {
				// fake-service answers an injected error with no message;
				// keep the JSON in the error's details-free message.
				return nil, connect.NewError(grpcCode(resp.Code), errors.New(mustJSON(resp)))
			}
			return out, nil
		}, opts...))
	mux.Handle(procHealthCheck, connect.NewUnaryHandler(procHealthCheck,
		func(ctx context.Context, req *connect.Request[healthRequest]) (*connect.Response[healthResponse], error) {
			st := int32(healthServing)
			if s.Faults().HealthCode >= 400 {
				st = healthNotServing
			}
			return connect.NewResponse(&healthResponse{Status: st}), nil
		}, opts...))
}

// grpcCode maps an HTTP code to the gRPC code closest to it.
func grpcCode(code int) connect.Code {
	switch code {
	case http.StatusBadRequest:
		return connect.CodeInvalidArgument
	case http.StatusTooManyRequests:
		return connect.CodeResourceExhausted
	case http.StatusServiceUnavailable:
		return connect.CodeUnavailable
	case http.StatusGatewayTimeout:
		return connect.CodeDeadlineExceeded
	case http.StatusNotFound:
		return connect.CodeNotFound
	}
	return connect.CodeInternal
}

// httpCode maps a gRPC code back, for the chain's code fields.
func httpCode(c connect.Code) int {
	switch c {
	case connect.CodeInvalidArgument:
		return http.StatusBadRequest
	case connect.CodeResourceExhausted:
		return http.StatusTooManyRequests
	case connect.CodeUnavailable:
		return http.StatusServiceUnavailable
	case connect.CodeDeadlineExceeded:
		return http.StatusGatewayTimeout
	case connect.CodeNotFound:
		return http.StatusNotFound
	}
	return http.StatusInternalServerError
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf(`{"error":%q}`, err.Error())
	}
	return string(b)
}

// h2cClient speaks HTTP/2 without TLS, as gRPC to a local sidecar does.
func h2cClient(timeout time.Duration) *http.Client {
	tr := &http.Transport{Protocols: new(http.Protocols)}
	tr.Protocols.SetUnencryptedHTTP2(true)
	return &http.Client{Timeout: timeout, Transport: tr}
}

// callGRPC calls a grpc:// upstream's FakeService.Handle.
func (s *Server) callGRPC(ctx context.Context, uri string, hdr http.Header) *Response {
	start := s.now()
	fail := func(code int, msg string) *Response {
		r := &Response{URI: uri, Type: "gRPC", Code: code, Error: msg}
		s.finish(r, start)
		return r
	}
	target := "http://" + strings.TrimSuffix(strings.TrimPrefix(uri, "grpc://"), "/")
	cl := connect.NewClient[handleRequest, handleResponse](s.h2c, target+procHandle, connect.WithGRPC(), connect.WithCodec(wireCodec{}))
	req := connect.NewRequest(&handleRequest{})
	for k, v := range hdr {
		req.Header()[k] = v
	}
	res, err := cl.CallUnary(ctx, req)
	var msg string
	code := http.StatusOK
	if err != nil {
		var ce *connect.Error
		var ne net.Error
		if !errors.As(err, &ce) || errors.As(err, &ne) {
			// Not an answer: the upstream couldn't be reached.
			return fail(-1, err.Error())
		}
		code, msg = httpCode(ce.Code()), ce.Message()
	} else {
		msg = res.Msg.Message
	}
	var up Response
	if json.Unmarshal([]byte(msg), &up) != nil || up.Name == "" {
		r := fail(code, "")
		r.Body = msg
		if err != nil {
			r.Error = err.Error()
		}
		return r
	}
	up.URI, up.Code = uri, code
	return &up
}
