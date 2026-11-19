package netsetup

import (
	"os"
	"strings"
	"testing"
)

// Run with PARKER_LIVE_IPTABLES=1 inside a throwaway netns (unshare -Urn):
// it changes the real FORWARD chain.
func TestFirewallLive(t *testing.T) {
	if os.Getenv("PARKER_LIVE_IPTABLES") == "" {
		t.Skip("set PARKER_LIVE_IPTABLES=1 in a private netns")
	}
	fw := SystemFirewall()
	if fw == nil {
		t.Skip("no iptables")
	}
	if _, err := fw.Run("-P", "FORWARD", "DROP"); err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Run("-N", "DOCKER-USER"); err != nil {
		t.Fatal(err)
	}
	spec := lab(map[string]string{"dc1-lan": "10.1.0.0/24"})
	chain, err := fw.Allow("live", spec)
	if err != nil || chain != "DOCKER-USER" {
		t.Fatalf("chain %q err %v", chain, err)
	}
	out, _ := fw.Run("-S", "DOCKER-USER")
	if strings.Count(out, "parker:live") != 2 {
		t.Fatalf("rules not added:\n%s", out)
	}
	n, err := fw.Revoke("live")
	if err != nil || n != 2 {
		t.Fatalf("revoked %d err %v\n%s", n, err, out)
	}
	out, _ = fw.Run("-S", "DOCKER-USER")
	if strings.Contains(out, "parker:live") {
		t.Fatalf("rules left:\n%s", out)
	}
}
