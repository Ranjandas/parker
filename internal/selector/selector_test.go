package selector

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/Ranjandas/parker/internal/spec"
)

func node(dc, name, role, group string, ip string, svcs ...string) *spec.RawNode {
	return &spec.RawNode{
		Networks: map[string]*spec.Attachment{dc + "-lan": {IP: ip}},
		Labels:   &spec.NodeLabels{DC: dc, Name: name, Role: role, Group: group, Services: svcs},
	}
}

func lab() *spec.File {
	return &spec.File{Name: "t", Nodes: map[string]*spec.RawNode{
		"dc1-server-1": node("dc1", "server-1", "server", "", "10.1.0.10"),
		"dc1-server-2": node("dc1", "server-2", "server", "", "10.1.0.11"),
		"dc1-server-3": node("dc1", "server-3", "server", "", "10.1.0.12"),
		"dc1-web-1":    node("dc1", "web-1", "client", "web", "10.1.0.100", "web"),
		"dc1-web-2":    node("dc1", "web-2", "client", "web", "10.1.0.101", "web"),
		"dc1-api":      node("dc1", "api", "client", "api", "10.1.0.102", "api", "metrics"),
		"dc1-bare":     node("dc1", "bare", "none", "bare", "10.1.0.103"),
		"dc2-server-1": node("dc2", "server-1", "server", "", "10.2.0.10"),
		"dc2-api":      node("dc2", "api", "client", "api", "10.2.0.100", "api"),
		"toxiproxy":    {Networks: map[string]*spec.Attachment{"dc1-lan": {IP: "10.1.0.200"}}},
	}}
}

func raft(dc string) (string, error) {
	if dc == "dc1" {
		return "dc1-server-2", nil
	}
	return "", errors.New("no leader")
}

func TestResolve(t *testing.T) {
	low := lab()
	for sel, want := range map[string]string{
		"dc2":               "dc2-api dc2-server-1",
		"dc1/servers":       "dc1-server-1 dc1-server-2 dc1-server-3",
		"dc1/clients":       "dc1-api dc1-web-1 dc1-web-2",
		"dc1/server-2":      "dc1-server-2",
		"dc1/web":           "dc1-web-1 dc1-web-2",
		"dc1/web-1":         "dc1-web-1",
		"dc1/default/web-1": "dc1-web-1",
		"dc1/api":           "dc1-api",
		"svc:api":           "dc1-api dc2-api",
		"svc:dc2/api":       "dc2-api",
		"svc:metrics":       "dc1-api",
		"dc1/leader":        "dc1-server-2",
		"dc1/followers":     "dc1-server-1 dc1-server-3",
		"dc1/active":        "dc1-server-2",
		"dc1/standbys":      "dc1-server-1 dc1-server-3",
		"toxiproxy":         "toxiproxy",
		"dc1-server-3":      "dc1-server-3",
		" dc1/bare ":        "dc1-bare",
	} {
		got, err := Resolve(low, sel, raft)
		if err != nil {
			t.Errorf("%q: %v", sel, err)
			continue
		}
		if strings.Join(got, " ") != want {
			t.Errorf("%q = %v, want %s", sel, got, want)
		}
	}
	if got, _ := Resolve(low, "dc1", raft); len(got) != 7 || slices.Contains(got, "toxiproxy") {
		t.Errorf("dc1 = %v (top-level nodes are not in a dc)", got)
	}
}

func TestResolveErrors(t *testing.T) {
	low := lab()
	for sel, want := range map[string]string{
		"":                 "empty selector",
		"dc9/servers":      `no datacenter "dc9"`,
		"svc:dc9/api":      `no datacenter "dc9"`,
		"dc1/nope":         "matches no nodes",
		"svc:nope":         "matches no nodes",
		"dc1/gateways":     "matches no nodes",
		"nope":             "matches no nodes",
		"dc1/team-a/web-1": "matches no nodes",
		"dc1/":             "want dc, dc/name",
		"a/b/c/d":          "want dc, dc/name",
		"dc2/leader":       "no leader",
	} {
		if _, err := Resolve(low, sel, raft); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err %v, want %q", sel, err, want)
		}
	}
	if _, err := Resolve(low, "dc1/leader", nil); !errors.Is(err, ErrNoRaft) {
		t.Errorf("no raft: %v", err)
	}
}

func TestOne(t *testing.T) {
	low := lab()
	if n, err := One(low, "dc1/leader", raft); err != nil || n != "dc1-server-2" {
		t.Errorf("leader: %s %v", n, err)
	}
	if _, err := One(low, "dc1/web", raft); err == nil || !strings.Contains(err.Error(), "matches 2 nodes (dc1-web-1, dc1-web-2)") {
		t.Errorf("group: %v", err)
	}
}

func TestLeaderByAddr(t *testing.T) {
	low := lab()
	if n, err := LeaderByAddr(low, "dc1", "10.1.0.12:8300"); err != nil || n != "dc1-server-3" {
		t.Errorf("%s %v", n, err)
	}
	if _, err := LeaderByAddr(low, "dc1", ""); err == nil {
		t.Error("empty leader accepted")
	}
	if _, err := LeaderByAddr(low, "dc1", "10.1.0.100:8300"); err == nil {
		t.Error("a client as leader accepted")
	}
}

func TestResolvePartitions(t *testing.T) {
	low := lab()
	billing := node("dc1", "billing-1", "client", "billing", "10.1.0.110", "billing")
	billing.Labels.Partition, billing.Labels.Namespaces = "team-a", []string{"payments"}
	low.Nodes["dc1-billing-1"] = billing
	low.Nodes["dc1-api"].Labels.Namespaces = []string{"payments"}
	for sel, want := range map[string]string{
		"part:team-a":           "dc1-billing-1",
		"part:dc1/team-a":       "dc1-billing-1",
		"part:default":          "dc1-api dc1-bare dc1-web-1 dc1-web-2 dc2-api",
		"ns:payments":           "dc1-api dc1-billing-1",
		"ns:dc2/payments":       "",
		"dc1/team-a/billing-1":  "dc1-billing-1",
		"dc1/team-a/billing":    "dc1-billing-1",
		"dc1/default/billing-1": "",
	} {
		got, err := Resolve(low, sel, raft)
		if want == "" {
			if err == nil || !strings.Contains(err.Error(), "matches no nodes") {
				t.Errorf("%q = %v, %v; want no match", sel, got, err)
			}
			continue
		}
		if err != nil || strings.Join(got, " ") != want {
			t.Errorf("%q = %v, %v; want %s", sel, got, err, want)
		}
	}
}

func TestResolveMeta(t *testing.T) {
	low := lab()
	low.Nodes["dc1-web-1"].Labels.Meta = map[string]string{"zone": "a", "region": "us-east"}
	low.Nodes["dc1-server-1"].Labels.Meta = map[string]string{"region": "us-east"}
	low.Nodes["dc2-api"].Labels.Meta = map[string]string{"region": "us-east"}
	for sel, want := range map[string]string{
		"meta:zone=a":             "dc1-web-1",
		"meta:region=us-east":     "dc1-server-1 dc1-web-1 dc2-api",
		"meta:dc2/region=us-east": "dc2-api",
		"meta:zone=":              "",
	} {
		got, err := Resolve(low, sel, nil)
		if want == "" {
			if err == nil {
				t.Errorf("%s: matched %v, want no nodes", sel, got)
			}
			continue
		}
		if err != nil || strings.Join(got, " ") != want {
			t.Errorf("%s: %v %v, want %s", sel, got, err, want)
		}
	}
	for _, sel := range []string{"meta:zone", "meta:=a", "meta:dc9/zone=a"} {
		if _, err := Resolve(low, sel, nil); err == nil {
			t.Errorf("%s: no error", sel)
		}
	}
}
