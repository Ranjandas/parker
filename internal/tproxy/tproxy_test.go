package tproxy

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
)

var r = Redirect{InboundPort: 21000, OutboundPort: 15001, DNSPort: 8600, ExcludeIn: []int{8301, 8500}, ExcludeOut: []int{8300}}

func TestRules(t *testing.T) {
	var lines []string
	for _, a := range Rules(r) {
		lines = append(lines, strings.Join(a, " "))
	}
	got := strings.Join(lines, "\n")
	for _, want := range []string{
		"-t nat -A PARKER_TPROXY_DNS -p udp --dport 53 -m mark ! --mark 0x5041 -j REDIRECT --to-ports 8600",
		"-t nat -A PARKER_TPROXY_OUT -m mark --mark 0x5041 -j RETURN\n-t nat -A PARKER_TPROXY_OUT -d 127.0.0.0/8 -j RETURN\n" +
			"-t nat -A PARKER_TPROXY_OUT -p tcp -m multiport --dports 8300 -j RETURN\n" +
			"-t nat -A PARKER_TPROXY_OUT -p tcp -j REDIRECT --to-ports 15001",
		"-t nat -A PARKER_TPROXY_IN -p tcp -m multiport --dports 8301,8500 -j RETURN\n" +
			"-t nat -A PARKER_TPROXY_IN -p tcp -j REDIRECT --to-ports 21000",
		"-t nat -C OUTPUT -j PARKER_TPROXY_DNS\n-t nat -A OUTPUT -j PARKER_TPROXY_DNS",
		"-t nat -C PREROUTING -p tcp -j PARKER_TPROXY_IN\n-t nat -A PREROUTING -p tcp -j PARKER_TPROXY_IN",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rules lack\n%s\n--- got\n%s", want, got)
		}
	}
	noDNS := r
	noDNS.DNSPort = 0
	for _, a := range Rules(noDNS) {
		if slices.Contains(a, "53") {
			t.Errorf("DNSPort 0 still redirects DNS: %v", a)
		}
	}
}

// fakeIPT keeps the nat table as chain -> rules, enough for Apply.
type fakeIPT struct {
	chains map[string][]string
	calls  int
}

func (f *fakeIPT) run(a ...string) error {
	f.calls++
	op, chain, rule := a[2], a[3], strings.Join(a[4:], " ")
	_, exists := f.chains[chain]
	switch op {
	case "-N":
		if exists {
			return errors.New("chain exists")
		}
		f.chains[chain] = nil
	case "-F":
		f.chains[chain] = nil
	case "-C":
		if !slices.Contains(f.chains[chain], rule) {
			return errors.New("no such rule")
		}
	case "-A":
		f.chains[chain] = append(f.chains[chain], rule)
	}
	return nil
}

func TestApplyTwice(t *testing.T) {
	f := &fakeIPT{chains: map[string][]string{"OUTPUT": nil, "PREROUTING": nil}}
	if err := Apply(r, f.run); err != nil {
		t.Fatal(err)
	}
	first := len(f.chains[ChainOut])
	if err := Apply(r, f.run); err != nil {
		t.Fatalf("second apply (a sidecar restart): %v", err)
	}
	if len(f.chains["OUTPUT"]) != 2 || len(f.chains["PREROUTING"]) != 1 {
		t.Errorf("jumps added twice: OUTPUT %v PREROUTING %v", f.chains["OUTPUT"], f.chains["PREROUTING"])
	}
	if len(f.chains[ChainOut]) != first || first != 4 {
		t.Errorf("%s has %d rules after two applies, want 4", ChainOut, len(f.chains[ChainOut]))
	}
}

func TestApplyError(t *testing.T) {
	err := Apply(r, func(a ...string) error {
		if a[2] == "-A" {
			return errors.New("no REDIRECT target")
		}
		return nil
	})
	if err == nil {
		t.Fatal("an -A failure is returned")
	}
}

func TestPatchBootstrap(t *testing.T) {
	b, err := PatchBootstrap([]byte(`{"admin": {}, "static_resources": {"clusters": []}}`))
	if err != nil {
		t.Fatal(err)
	}
	var boot map[string]any
	if err := json.Unmarshal(b, &boot); err != nil {
		t.Fatal(err)
	}
	bind := boot["cluster_manager"].(map[string]any)["upstream_bind_config"].(map[string]any)
	opt := bind["socket_options"].([]any)[0].(map[string]any)
	if opt["level"] != float64(1) || opt["name"] != float64(36) || opt["int_value"] != float64(Mark) || opt["state"] != "STATE_PREBIND" {
		t.Errorf("socket option = %v", opt)
	}
	if _, ok := boot["static_resources"]; !ok {
		t.Error("the rest of the bootstrap is kept")
	}
	if _, err := PatchBootstrap([]byte("not json")); err == nil {
		t.Error("bad bootstrap accepted")
	}
}

func TestSplitArgs(t *testing.T) {
	c, e := SplitArgs([]string{"consul", "connect", "envoy", "-sidecar-for", "web", "--", "--log-level", "info"})
	if !slices.Equal(c, []string{"consul", "connect", "envoy", "-sidecar-for", "web"}) || !slices.Equal(e, []string{"--log-level", "info"}) {
		t.Errorf("SplitArgs = %v / %v", c, e)
	}
	if c, e := SplitArgs([]string{"consul"}); len(c) != 1 || e != nil {
		t.Errorf("no --: %v / %v", c, e)
	}
}
