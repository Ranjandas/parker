package expand

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// docs/guide/acl-tokens.md quotes every policy's rules; keep it in step with the code.
func TestACLDocQuotesTheRules(t *testing.T) {
	b, err := os.ReadFile("../../docs/guide/acl-tokens.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(b)
	mesh := meshGatewayRules("<svc>", false)
	meshAll := strings.TrimPrefix(meshGatewayRules("<svc>", true), mesh)
	for name, rules := range map[string]string{
		"node identity":          nodeIdentityRules("<node>"),
		"service identity":       identityBody("<svc>", ""),
		"parker-read-everywhere": readEverywhere,
		"parker-dns":             dnsRules,
		"scoped service":         identityRules("<ns>", "<svc>") + readEverywhere,
		"mesh gateway":           mesh,
		"mesh, all partitions":   meshAll,
		"terminating":            linkedRules([]linkedService{{"<linked>", ""}}, "", false, ""),
		"terminating, other ns":  linkedRules([]linkedService{{"<linked>", "<linked-ns>"}}, "", true, ""),
		"api gateway":            apiGatewayRules,
	} {
		if !strings.Contains(doc, "```hcl\n"+rules+"```") {
			t.Errorf("docs/guide/acl-tokens.md doesn't quote the %s rules:\n%s", name, rules)
		}
	}
}

// nodeIdentityRules are what Consul expands a node identity to.
func nodeIdentityRules(node string) string {
	return fmt.Sprintf("node %q { policy = \"write\" }\nservice_prefix \"\" { policy = \"read\" }\n", node)
}
