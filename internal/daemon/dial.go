package daemon

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/netip"
	"slices"
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
	// node is an expanded name; a dc-qualified form matches by labels, so
	// it finds a node in any partition (dc, partition and name can all hold
	// hyphens, so the expanded name can't be split back).
	var node, labName, dc, part, short string
	switch len(parts) {
	case 1:
		node, labName = parts[0], scopeLab
	case 2:
		node, labName = parts[0], parts[1]
	case 3:
		short, dc, labName = parts[0], parts[1], parts[2]
	case 4: // <name>.<partition>.<dc>.<lab>, like the dc/partition/name selector
		short, part, dc, labName = parts[0], parts[1], parts[2], parts[3]
	default:
		return "", fmt.Errorf("%q: want <node>.<lab>.parker, <name>.<dc>.<lab>.parker or <name>.<partition>.<dc>.<lab>.parker", host)
	}
	type hit struct{ key, addr string }
	var found []hit
	for _, ln := range slices.Sorted(maps.Keys(labs)) {
		if labName != "" && ln != labName {
			continue
		}
		low := labs[ln]
		for _, key := range slices.Sorted(maps.Keys(low.Nodes)) {
			n := low.Nodes[key]
			match := key == node
			if dc != "" {
				l := n.Labels
				match = l != nil && l.DC == dc && l.Name == short && (part == "" || nodePartition(n) == part)
			}
			if !match {
				continue
			}
			for _, ip := range lab.NodeIPs(n) {
				found = append(found, hit{key + "." + ln, net.JoinHostPort(ip, port)})
				break
			}
		}
	}
	switch len(found) {
	case 0:
		return "", fmt.Errorf("no node %q in %s", host, labOrAll(labName))
	case 1:
		return found[0].addr, nil
	}
	var keys []string
	for _, h := range found {
		keys = append(keys, h.key+".parker")
	}
	return "", fmt.Errorf("%q matches several nodes (%s); name one", host, strings.Join(keys, ", "))
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

// nodePartition is the admin partition a node's agent joins ("default"
// when it names none).
func nodePartition(n *spec.RawNode) string {
	if n.Labels != nil && n.Labels.Partition != "" {
		return n.Labels.Partition
	}
	return spec.DefaultPartition
}
