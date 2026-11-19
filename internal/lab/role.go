package lab

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"time"

	"github.com/Ranjandas/parker/internal/netsetup"
	"github.com/Ranjandas/parker/internal/supervisor"
)

// consulRole asks the Consul agent inside the node's netns whether it is
// the raft leader, a follower or a client. "" when there's no answer.
func consulRole(labName, node string, ips []string) string {
	dial := supervisor.DialerIn(netsetup.NetnsPath(labName, node))
	hc := &http.Client{Timeout: 700 * time.Millisecond, Transport: &http.Transport{DialContext: dial, DisableKeepAlives: true}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	get := func(path string, v any) error {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:8500"+path, nil)
		resp, err := hc.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		return json.NewDecoder(resp.Body).Decode(v)
	}
	var self struct {
		Config struct{ Server bool }
	}
	if err := get("/v1/agent/self", &self); err != nil {
		return ""
	}
	if !self.Config.Server {
		return "client"
	}
	var leader string
	if err := get("/v1/status/leader", &leader); err != nil || leader == "" {
		return "no-leader"
	}
	host, _, _ := net.SplitHostPort(leader)
	for _, ip := range ips {
		if ip == host {
			return "leader"
		}
	}
	return "follower"
}
