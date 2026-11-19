package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"time"

	"connectrpc.com/connect"

	parkerv1 "github.com/Ranjandas/parker/internal/api/parkerv1"
	"github.com/Ranjandas/parker/internal/lab"
	"github.com/Ranjandas/parker/internal/spec"
)

// ResolveAddress turns a proxy target into ip:port inside a lab. host may
// be an IP in a lab network, or a node name: <node>.<lab>.parker,
// <short>.<dc>.<lab>.parker (server-1.dc1.minimal.parker), <node>.parker
// when unique across labs, or a bare <node> within scopeLab. Anything
// outside the labs' networks is refused, so the daemon is not an open proxy
// into the host's network.
func ResolveAddress(address, scopeLab string) (string, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return "", err
	}
	labs := map[string]*spec.File{}
	names, err := lab.Labs()
	if err != nil {
		return "", err
	}
	for _, n := range names {
		if low, err := lab.LoadTopology(n); err == nil {
			labs[n] = low
		}
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		for _, low := range labs {
			for _, nw := range low.Networks {
				if p, err := netip.ParsePrefix(nw.Subnet); err == nil && p.Contains(ip) {
					return net.JoinHostPort(ip.String(), port), nil
				}
			}
		}
		return "", fmt.Errorf("%s is not in any running lab's network", host)
	}

	parts := strings.Split(strings.TrimSuffix(strings.ToLower(host), "."), ".")
	if parts[len(parts)-1] == "parker" {
		parts = parts[:len(parts)-1]
	}
	var node, labName string
	switch len(parts) {
	case 1:
		node, labName = parts[0], scopeLab
	case 2:
		node, labName = parts[0], parts[1]
	case 3:
		node, labName = parts[1]+"-"+parts[0], parts[2]
	default:
		return "", fmt.Errorf("%q: want <node>.<lab>.parker or <name>.<dc>.<lab>.parker", host)
	}
	var found []string
	for ln, low := range labs {
		if labName != "" && ln != labName {
			continue
		}
		if n, ok := low.Nodes[node]; ok {
			for _, ip := range lab.NodeIPs(n) {
				found = append(found, net.JoinHostPort(ip, port))
				break
			}
		}
	}
	switch len(found) {
	case 0:
		return "", fmt.Errorf("no node %q in %s", node, labOrAll(labName))
	case 1:
		return found[0], nil
	}
	return "", fmt.Errorf("node %q is in several labs; use %s.<lab>.parker", node, node)
}

func labOrAll(l string) string {
	if l == "" {
		return "any running lab"
	}
	return "lab " + l
}

func (s *Server) Dial(ctx context.Context, stream *connect.BidiStream[parkerv1.DialRequest, parkerv1.DialResponse]) error {
	first, err := stream.Receive()
	if err != nil {
		return err
	}
	open := first.GetOpen()
	if open == nil {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("first message must be open"))
	}
	addr, err := ResolveAddress(open.Address, open.Lab)
	if err != nil {
		return connect.NewError(connect.CodePermissionDenied, err)
	}
	d := net.Dialer{Timeout: 5 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return connect.NewError(connect.CodeUnavailable, err)
	}
	defer conn.Close()
	if err := stream.Send(&parkerv1.DialResponse{Msg: &parkerv1.DialResponse_Opened{Opened: &parkerv1.DialOpened{Remote: addr}}}); err != nil {
		return err
	}
	// client -> target
	go func() {
		for {
			msg, err := stream.Receive()
			if err != nil {
				if tc, ok := conn.(*net.TCPConn); ok {
					_ = tc.CloseWrite()
				}
				return
			}
			if b := msg.GetData(); len(b) > 0 {
				if _, err := conn.Write(b); err != nil {
					return
				}
			}
		}
	}()
	// target -> client
	buf := make([]byte, 32*1024)
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			if serr := stream.Send(&parkerv1.DialResponse{Msg: &parkerv1.DialResponse_Data{Data: append([]byte(nil), buf[:n]...)}}); serr != nil {
				return nil
			}
		}
		if err != nil {
			return nil
		}
	}
}
