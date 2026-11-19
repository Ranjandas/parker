package lab

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"

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

// ServerClients returns an API client for each server of dc, by node
// name, reaching it inside its netns with the lab's token and CA.
func ServerClients(low *spec.File, dc string) (map[string]*api.Client, error) {
	out := map[string]*api.Client{}
	for _, n := range slices.Sorted(maps.Keys(low.Nodes)) {
		if l := low.Nodes[n].Labels; l == nil || l.DC != dc || l.Role != "server" {
			continue
		}
		dial := supervisor.DialerIn(netsetup.NetnsPath(low.Name, n))
		hc := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{DialContext: dial, DisableKeepAlives: true}}
		cfg, err := supervisor.ConsulConfig(AgentCheck(low, n), hc, supervisor.NodeDir(RunDir(low.Name), n))
		if err != nil {
			return nil, err
		}
		c, err := api.NewClient(cfg)
		if err != nil {
			return nil, err
		}
		out[n] = c
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s has no servers", dc)
	}
	return out, nil
}

// RaftLeader asks every server of dc for the raft leader and returns the
// node most of them name: during a partition a cut-off server may still
// report the old leader.
func RaftLeader(low *spec.File, dc string) (string, error) {
	clients, err := ServerClients(low, dc)
	if err != nil {
		return "", err
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	votes := map[string]int{}
	var errs []error
	for n, c := range clients {
		wg.Add(1)
		go func() {
			defer wg.Done()
			addr, err := c.Status().Leader()
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", n, err))
				return
			}
			if addr != "" {
				votes[addr]++
			}
		}()
	}
	wg.Wait()
	return pickLeader(low, dc, votes, errs)
}

func pickLeader(low *spec.File, dc string, votes map[string]int, errs []error) (string, error) {
	best := ""
	for _, addr := range slices.Sorted(maps.Keys(votes)) {
		if best == "" || votes[addr] > votes[best] {
			best = addr
		}
	}
	if best == "" {
		if len(errs) > 0 {
			slices.SortFunc(errs, func(a, b error) int { return strings.Compare(a.Error(), b.Error()) })
			return "", fmt.Errorf("%s has no raft leader (%v)", dc, errors.Join(errs...))
		}
		return "", fmt.Errorf("%s has no raft leader", dc)
	}
	return selector.LeaderByAddr(low, dc, best)
}
