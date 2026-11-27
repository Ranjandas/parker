package fake

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/Ranjandas/parker/internal/spec"
)

// DefaultPadHeader is the header a pad goes in when none is named.
const DefaultPadHeader = "X-Parker-Pad"

// UpstreamRequest is how a fake shapes the requests to its upstreams.
type UpstreamRequest struct {
	// Forward are headers passed on from the incoming request.
	Forward []string
	Method  string
	Headers map[string]string
	// PadName and PadBytes add a header whose "Name: value" line is
	// PadBytes long.
	PadName  string
	PadBytes int64
	// BodyBytes is the size of a filler body; 0 is none.
	BodyBytes int64
}

func (u *UpstreamRequest) apply(f *spec.Fake) error {
	for _, h := range f.ForwardHeaders {
		if strings.TrimSpace(h) == "" || strings.ContainsAny(h, ": \t") {
			return fmt.Errorf("forward_headers: %q is not a header name", h)
		}
	}
	if len(f.ForwardHeaders) > 0 {
		u.Forward = f.ForwardHeaders
	}
	r := f.UpstreamRequest
	if r == nil {
		return nil
	}
	switch m := strings.ToUpper(r.Method); m {
	case "":
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		u.Method = m
	default:
		return fmt.Errorf("upstream_request.method: want GET, POST, PUT, PATCH or DELETE, got %q", r.Method)
	}
	if len(r.Headers) > 0 {
		u.Headers = r.Headers
	}
	if p := r.PadHeader; p != nil {
		name := p.Name
		if name == "" {
			name = DefaultPadHeader
		}
		if strings.ContainsAny(name, ": \t") {
			return fmt.Errorf("upstream_request.pad_header.name: %q is not a header name", name)
		}
		if p.Bytes < 0 || int64(p.Bytes) > maxHeaderCap {
			return fmt.Errorf("upstream_request.pad_header.bytes: want 0 to %s, got %s", spec.ByteSize(maxHeaderCap), p.Bytes)
		}
		u.PadName, u.PadBytes = name, int64(p.Bytes)
	}
	if r.BodyBytes != nil {
		if *r.BodyBytes < 0 {
			return fmt.Errorf("upstream_request.body_bytes: want 0 or more, got %d", *r.BodyBytes)
		}
		u.BodyBytes = int64(*r.BodyBytes)
	}
	return nil
}

// method is the method of each upstream call: a body makes it POST
// unless one is named.
func (u UpstreamRequest) method() string {
	switch {
	case u.Method != "":
		return u.Method
	case u.BodyBytes > 0:
		return http.MethodPost
	}
	return http.MethodGet
}

// PadValue is a header value that makes the "Name: value" line n bytes
// long (at least one byte of value).
func PadValue(name string, n int64) string {
	return strings.Repeat("x", int(max(1, n-int64(len(name))-2)))
}

// ParsePad reads NAME=SIZE, as parker call --pad-header takes it:
// Cookie=9KB.
func ParsePad(s string) (name string, size int64, err error) {
	name, v, ok := strings.Cut(s, "=")
	name = strings.TrimSpace(name)
	if !ok || name == "" || strings.ContainsAny(name, ": \t") {
		return "", 0, fmt.Errorf("%q: want NAME=SIZE, such as Cookie=9KB", s)
	}
	b, err := spec.ParseByteSize(v)
	if err != nil {
		return "", 0, fmt.Errorf("%q: %w", s, err)
	}
	if int64(b) > maxHeaderCap {
		return "", 0, fmt.Errorf("%q: want at most %s", s, spec.ByteSize(maxHeaderCap))
	}
	return name, int64(b), nil
}

// outgoing is what every upstream call of one request carries.
type outgoing struct {
	id, trail, fault string
	// hdr holds the forwarded, configured and pad headers.
	hdr  http.Header
	body int64
}

// outgoingFor builds the upstream calls' extras for incoming request r;
// pad is a per-request pad-upstreams size (0 for none).
func (s *Server) outgoingFor(r *http.Request, id, trail string, pad int64) outgoing {
	u := s.cfg.Upstream
	o := outgoing{id: id, trail: trail, fault: r.Header.Get(HeaderFault), hdr: http.Header{}, body: u.BodyBytes}
	for _, h := range u.Forward {
		for _, v := range r.Header.Values(h) {
			o.hdr.Add(h, v)
		}
	}
	for k, v := range u.Headers {
		o.hdr.Set(k, v)
	}
	if u.PadBytes > 0 {
		o.hdr.Set(u.PadName, PadValue(u.PadName, u.PadBytes))
	}
	if pad > 0 {
		o.hdr.Set(DefaultPadHeader, PadValue(DefaultPadHeader, pad))
	}
	return o
}
