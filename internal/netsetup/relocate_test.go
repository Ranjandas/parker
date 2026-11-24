package netsetup

import (
	"net/netip"
	"reflect"
	"strings"
	"testing"

	"github.com/Ranjandas/parker/internal/spec"
)

func autoLab() *spec.File {
	return &spec.File{
		Networks: map[string]*spec.Network{
			"dc1-lan": {Subnet: "10.1.0.0/24", Gateway: "10.1.0.1", Auto: true},
			"dc2-lan": {Subnet: "10.2.0.0/24", Gateway: "10.2.0.1", Auto: true},
		},
		Nodes: map[string]*spec.RawNode{
			"dc1-server-1": {
				Networks: map[string]*spec.Attachment{"dc1-lan": {IP: "10.1.0.10"}},
				Files:    map[string]string{"consul.hcl": `retry_join = ["10.1.0.10", "10.1.0.11"]` + "\nbind_addr = \"10.1.0.10\"\nversion = \"1.10.1\"\nnot_ours = \"10.10.0.5\"\n"},
				Processes: map[string]*spec.Process{
					"consul": {Run: []string{"consul", "agent", "-retry-join=10.2.0.10", "-advertise", "10.1.0.10."}},
				},
			},
		},
	}
}

func TestRelocateMovesAutoNetworks(t *testing.T) {
	host := []HostNet{
		hn("10.1.0.0/24", "pk-aaaa", "other", false), // another lab on dc1's default
		hn("10.3.0.0/24", "pk-bbbb", "other", false),
		hn("10.2.0.0/24", "pk-cccc", "me", false), // this lab's own leftover
	}
	f := autoLab()
	moves, err := Relocate("me", f, host, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(moves) != 1 || moves[0].Network != "dc1-lan" || moves[0].To.String() != "10.4.0.0/24" {
		t.Fatalf("moves: %v", moves)
	}
	if !strings.Contains(moves[0].String(), `10.1.0.0/24 is taken (running lab "other" uses it); using 10.4.0.0/24`) {
		t.Errorf("message: %s", moves[0])
	}
	n := f.Networks["dc1-lan"]
	if n.Subnet != "10.4.0.0/24" || n.Gateway != "10.4.0.1" || f.Networks["dc2-lan"].Subnet != "10.2.0.0/24" {
		t.Errorf("networks: %+v %+v", n, f.Networks["dc2-lan"])
	}
	node := f.Nodes["dc1-server-1"]
	if ip := node.Networks["dc1-lan"].IP; ip != "10.4.0.10" {
		t.Errorf("node ip %s", ip)
	}
	want := `retry_join = ["10.4.0.10", "10.4.0.11"]` + "\nbind_addr = \"10.4.0.10\"\nversion = \"1.10.1\"\nnot_ours = \"10.10.0.5\"\n"
	if got := node.Files["consul.hcl"]; got != want {
		t.Errorf("file:\n%s\nwant:\n%s", got, want)
	}
	if got := strings.Join(node.Processes["consul"].Run, " "); got != "consul agent -retry-join=10.2.0.10 -advertise 10.4.0.10." {
		t.Errorf("run: %s", got)
	}
}

func TestRelocatePrefersLastRun(t *testing.T) {
	host := []HostNet{hn("10.1.0.0/24", "pk-aaaa", "other", false)}
	f := autoLab()
	prev := map[string]netip.Prefix{"dc1-lan": netip.MustParsePrefix("10.9.0.0/24")}
	moves, err := Relocate("me", f, host, prev)
	if err != nil || len(moves) != 1 || moves[0].To.String() != "10.9.0.0/24" {
		t.Fatalf("moves %v, err %v", moves, err)
	}
}

func TestRelocateKeepsFixedSubnets(t *testing.T) {
	host := []HostNet{hn("10.1.0.0/24", "pk-aaaa", "other", false)}
	f := autoLab()
	f.Networks["dc1-lan"].Auto = false
	_, err := Relocate("me", f, host, nil)
	if err == nil || !strings.Contains(err.Error(), `running lab "other"`) || !strings.Contains(err.Error(), "leave subnet: out") {
		t.Fatalf("want a clash error naming the lab, got %v", err)
	}
	if f.Networks["dc1-lan"].Subnet != "10.1.0.0/24" {
		t.Error("fixed subnet moved")
	}
	if err := clashError("me", autoLab(), host); err != nil {
		t.Errorf("auto networks shouldn't fail the check: %v", err)
	}
}

func TestRelocateNoClash(t *testing.T) {
	f := autoLab()
	moves, err := Relocate("me", f, []HostNet{hn("172.17.0.0/16", "docker0", "", false)}, nil)
	if err != nil || len(moves) != 0 || f.Nodes["dc1-server-1"].Networks["dc1-lan"].IP != "10.1.0.10" {
		t.Fatalf("moves %v err %v", moves, err)
	}
}

func TestRewriteStringsInterfaces(t *testing.T) {
	m := []Move{{From: netip.MustParsePrefix("10.1.0.0/24"), To: netip.MustParsePrefix("10.7.0.0/24")}}
	v := map[string]any{"a": "10.1.0.5", "b": []any{"x 10.1.0.6", map[string]any{"c": "10.1.0.7:8500"}}, "n": 3}
	type wrap struct{ V any }
	w := &wrap{V: v}
	f := func(s string) string { return MoveAddrs(s, m) }
	rewriteStrings(reflect.ValueOf(w), f)
	got := w.V.(map[string]any)
	if got["a"] != "10.7.0.5" || got["b"].([]any)[0] != "x 10.7.0.6" || got["b"].([]any)[1].(map[string]any)["c"] != "10.7.0.7:8500" || got["n"] != 3 {
		t.Fatalf("%v", got)
	}
}
