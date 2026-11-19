package netsetup

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// fakeIptables keeps chains as -S listings and applies -I / -D.
type fakeIptables struct {
	policy string
	chains map[string][]string // chain -> "-A ..." rules
	calls  [][]string
}

func (f *fakeIptables) fw() *Firewall {
	return &Firewall{Run: func(args ...string) (string, error) {
		f.calls = append(f.calls, args)
		switch args[0] {
		case "-S":
			rules, ok := f.chains[args[1]]
			if !ok {
				return "", errors.New("no chain")
			}
			head := "-N " + args[1]
			if args[1] == "FORWARD" {
				head = "-P FORWARD " + f.policy
			}
			return head + "\n" + strings.Join(rules, "\n") + "\n", nil
		case "-I":
			rule := "-A " + args[1] + " " + strings.Join(args[2:], " ")
			rule = strings.Replace(rule, "--comment parker:", `--comment "parker:`, 1)
			rule = strings.Replace(rule, " -j ACCEPT", `" -j ACCEPT`, 1)
			f.chains[args[1]] = append([]string{rule}, f.chains[args[1]]...)
		case "-D":
			want := "-A " + args[1] + " " + strings.Join(args[2:], " ")
			kept := f.chains[args[1]][:0]
			for _, r := range f.chains[args[1]] {
				if strings.ReplaceAll(r, `"`, "") != want {
					kept = append(kept, r)
				}
			}
			f.chains[args[1]] = kept
		}
		return "", nil
	}}
}

func TestFirewallNoDockerNoRules(t *testing.T) {
	f := &fakeIptables{policy: "ACCEPT", chains: map[string][]string{"FORWARD": nil}}
	chain, err := f.fw().Allow("me", lab(map[string]string{"dc1-lan": "10.1.0.0/24"}))
	if err != nil || chain != "" {
		t.Fatalf("chain %q err %v", chain, err)
	}
	if len(f.chains["FORWARD"]) != 0 {
		t.Fatal("added rules without need")
	}
}

func TestFirewallDockerUserRoundTrip(t *testing.T) {
	other := `-A DOCKER-USER -j RETURN`
	f := &fakeIptables{policy: "DROP", chains: map[string][]string{
		"FORWARD":     {"-A FORWARD -j DOCKER-USER"},
		"DOCKER-USER": {other},
	}}
	fw := f.fw()
	spec := lab(map[string]string{"dc1-lan": "10.1.0.0/24", "wan": "10.255.0.0/24"})
	chain, err := fw.Allow("me", spec)
	if err != nil || chain != "DOCKER-USER" {
		t.Fatalf("chain %q err %v", chain, err)
	}
	if got := len(f.chains["DOCKER-USER"]); got != 5 {
		t.Fatalf("want 4 lab rules + 1, got %d: %v", got, f.chains["DOCKER-USER"])
	}
	// Another lab's rules must survive our down.
	if _, err := fw.Allow("other", lab(map[string]string{"x": "10.9.0.0/24"})); err != nil {
		t.Fatal(err)
	}
	n, err := fw.Revoke("me")
	if err != nil || n != 4 {
		t.Fatalf("revoked %d, err %v", n, err)
	}
	for _, r := range f.chains["DOCKER-USER"] {
		if strings.Contains(r, "parker:me") {
			t.Errorf("left behind: %s", r)
		}
	}
	if len(f.chains["DOCKER-USER"]) != 3 {
		t.Errorf("unexpected chain: %v", f.chains["DOCKER-USER"])
	}
}

func TestFirewallPolicyDropUsesForward(t *testing.T) {
	f := &fakeIptables{policy: "DROP", chains: map[string][]string{"FORWARD": nil}}
	chain, err := f.fw().Allow("me", lab(map[string]string{"dc1-lan": "10.1.0.0/24"}))
	if err != nil || chain != "FORWARD" {
		t.Fatalf("chain %q err %v", chain, err)
	}
}

func TestDeleteArgs(t *testing.T) {
	listing := "-N DOCKER-USER\n" +
		`-A DOCKER-USER -o pk-12345678 -m comment --comment "parker:me" -j ACCEPT` + "\n" +
		`-A DOCKER-USER -o pk-87654321 -m comment --comment parker:meow -j ACCEPT` + "\n"
	got := deleteArgs(listing, "me")
	want := [][]string{{"-D", "DOCKER-USER", "-o", "pk-12345678", "-m", "comment", "--comment", "parker:me", "-j", "ACCEPT"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}
