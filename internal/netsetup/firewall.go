package netsetup

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"os/exec"
	"slices"
	"strings"

	"github.com/Ranjandas/parker/internal/spec"
)

// Docker sets the iptables FORWARD policy to DROP and loads br_netfilter
// with bridge-nf-call-iptables=1, so even traffic between two nodes on the
// same Parker bridge goes through FORWARD and is silently dropped. Parker
// inserts labelled ACCEPT rules for its bridges at the top of DOCKER-USER
// (Docker's documented hook, evaluated before its own rules) or FORWARD, and
// removes them on down by their comment, so cleanup needs no state file.

// Firewall reads and changes iptables rules. Run executes `iptables ARGS...`
// and returns its stdout.
type Firewall struct {
	Run func(args ...string) (string, error)
}

// SystemFirewall uses the iptables binary on PATH (which picks the legacy or
// nft backend the host uses). It is nil if iptables is not installed, in
// which case nothing can be filtering through it either.
func SystemFirewall() *Firewall {
	bin, err := exec.LookPath("iptables")
	if err != nil {
		return nil
	}
	return &Firewall{Run: func(args ...string) (string, error) {
		var out, stderr bytes.Buffer
		cmd := exec.Command(bin, append([]string{"-w"}, args...)...)
		cmd.Stdout, cmd.Stderr = &out, &stderr
		if err := cmd.Run(); err != nil {
			return "", fmt.Errorf("iptables %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
		}
		return out.String(), nil
	}}
}

// ForwardState is what Parker needs to know about the host's FORWARD chain.
type ForwardState struct {
	PolicyDrop bool // FORWARD policy is DROP (Docker sets this)
	DockerUser bool // the DOCKER-USER chain exists
}

// NeedsRules reports whether bridged lab traffic would hit a DROP.
func (s ForwardState) NeedsRules() bool { return s.PolicyDrop || s.DockerUser }

// Chain is where Parker puts its rules.
func (s ForwardState) Chain() string {
	if s.DockerUser {
		return "DOCKER-USER"
	}
	return "FORWARD"
}

func (fw *Firewall) State() (ForwardState, error) {
	out, err := fw.Run("-S", "FORWARD")
	if err != nil {
		return ForwardState{}, err
	}
	var s ForwardState
	for _, l := range strings.Split(out, "\n") {
		if strings.TrimSpace(l) == "-P FORWARD DROP" {
			s.PolicyDrop = true
		}
	}
	// -S on a missing chain fails; that is the "no Docker" answer.
	if _, err := fw.Run("-S", "DOCKER-USER"); err == nil {
		s.DockerUser = true
	}
	return s, nil
}

func comment(lab string) string { return Label(lab) }

// RuleArgs returns the rules (as iptables -I arguments) that let traffic in
// and out of the lab's bridges through chain.
func RuleArgs(lab, chain string, f *spec.File) [][]string {
	var out [][]string
	for _, name := range slices.Sorted(maps.Keys(f.Networks)) {
		br := BridgeName(lab, name)
		for _, dir := range []string{"-i", "-o"} {
			out = append(out, []string{"-I", chain, dir, br, "-m", "comment", "--comment", comment(lab), "-j", "ACCEPT"})
		}
	}
	return out
}

// Allow adds the lab's ACCEPT rules if the host's FORWARD chain would drop
// lab traffic. It returns the chain used, or "" when no rules were needed.
func (fw *Firewall) Allow(lab string, f *spec.File) (string, error) {
	s, err := fw.State()
	if err != nil {
		return "", err
	}
	if !s.NeedsRules() {
		return "", nil
	}
	chain := s.Chain()
	for _, args := range RuleArgs(lab, chain, f) {
		if _, err := fw.Run(args...); err != nil {
			return chain, err
		}
	}
	return chain, nil
}

// Revoke deletes every rule labelled for the lab from FORWARD and
// DOCKER-USER. It returns how many rules it removed.
func (fw *Firewall) Revoke(lab string) (int, error) {
	n := 0
	var errs []error
	for _, chain := range []string{"DOCKER-USER", "FORWARD"} {
		out, err := fw.Run("-S", chain)
		if err != nil {
			continue // chain absent
		}
		for _, args := range deleteArgs(out, lab) {
			if _, err := fw.Run(args...); err != nil {
				errs = append(errs, err)
				continue
			}
			n++
		}
	}
	return n, errors.Join(errs...)
}

// deleteArgs turns the lab's rules in `iptables -S` output into -D commands.
func deleteArgs(listing, lab string) [][]string {
	var out [][]string
	want := `--comment ` + comment(lab) + ` `
	wantQ := `--comment "` + comment(lab) + `" `
	for _, l := range strings.Split(listing, "\n") {
		l = strings.TrimSpace(l)
		if !strings.HasPrefix(l, "-A ") || !(strings.Contains(l, want) || strings.Contains(l, wantQ)) {
			continue
		}
		f := strings.Fields(l)
		f[0] = "-D"
		for i, s := range f {
			f[i] = strings.Trim(s, `"`)
		}
		out = append(out, f)
	}
	return out
}

// FirewallHints explains host settings Parker does not change itself.
func FirewallHints() []string {
	var hints []string
	if out, err := exec.Command("firewall-cmd", "--state").Output(); err == nil && strings.TrimSpace(string(out)) == "running" {
		hints = append(hints, "firewalld is running; if nodes can't reach the host or each other, put the lab bridges in the trusted zone: "+
			"sudo firewall-cmd --zone=trusted --add-interface=<pk-bridge>  (bridge names: `ip -br link | grep pk-`)")
	}
	return hints
}
