package lab

import (
	"fmt"
	"maps"
	"slices"

	"context"
	"encoding/json"
	"net"
	"net/http"
	"time"

	"github.com/Ranjandas/parker/internal/selector"
	"github.com/hashicorp/consul/api"

	"github.com/Ranjandas/parker/internal/netsetup"
	"github.com/Ranjandas/parker/internal/spec"
	"github.com/Ranjandas/parker/internal/supervisor"
)

// consulRole asks the Consul agent inside the node's netns whether it is
// the raft leader, a follower or a client. "" when there's no answer.
// cc says how to reach it (token, TLS).
func consulRole(labName, node string, ips []string, cc *spec.ConsulCheck) string {
	dial := supervisor.DialerIn(netsetup.NetnsPath(labName, node))
	hc := &http.Client{Timeout: 700 * time.Millisecond, Transport: &http.Transport{DialContext: dial, DisableKeepAlives: true}}
	cfg, err := supervisor.ConsulConfig(cc, hc, supervisor.NodeDir(RunDir(labName), node))
	if err != nil {
		return ""
	}
	return roleFrom(cfg.HttpClient, cfg.Scheme+"://"+cfg.Address, ips, cc.Token)
}

// roleFrom asks the agent API at base; ips are the node's own addresses.
func roleFrom(hc *http.Client, base string, ips []string, token string) string {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	get := func(path string, v any) error {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
		if token != "" {
			req.Header.Set("X-Consul-Token", token)
		}
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

// RaftLeader asks the dc's servers, in turn, for the raft leader and
// returns its node name.
func RaftLeader(low *spec.File, dc string) (string, error) {
	var last error = fmt.Errorf("%s has no servers", dc)
	for _, n := range slices.Sorted(maps.Keys(low.Nodes)) {
		if l := low.Nodes[n].Labels; l == nil || l.DC != dc || l.Role != "server" {
			continue
		}
		dial := supervisor.DialerIn(netsetup.NetnsPath(low.Name, n))
		hc := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{DialContext: dial, DisableKeepAlives: true}}
		cc := AgentCheck(low, n)
		cfg, err := supervisor.ConsulConfig(cc, hc, supervisor.NodeDir(RunDir(low.Name), n))
		if err != nil {
			last = err
			continue
		}
		c, err := api.NewClient(cfg)
		if err != nil {
			last = err
			continue
		}
		addr, err := c.Status().Leader()
		if err != nil {
			last = fmt.Errorf("%s: %w", n, err)
			continue
		}
		return selector.LeaderByAddr(low, dc, addr)
	}
	return "", last
}
