package expand

import (
	"slices"
	"testing"
)

// A gateway in a namespace other than default gets its token there when a
// policy in that namespace can hold its rules, and a scoped token in the
// partition's default namespace only when it can't.
func TestNamespacedGatewayTokens(t *testing.T) {
	for _, c := range []struct {
		name       string
		kind       string
		linked     []linkedService
		scoped     bool
		policy     string
		policyNS   string
		policyRule string
	}{
		{"terminating, linked here", "terminating", []linkedService{{"api", ""}, {"db", "app"}}, false,
			"parker-terminating-gateway-tgw", "app", "service \"api\" { policy = \"write\" }\nservice \"db\" { policy = \"write\" }\n"},
		{"terminating, wildcard", "terminating", []linkedService{{"*", ""}}, false,
			"parker-terminating-gateway-tgw", "app", "service_prefix \"\" { policy = \"write\" }\n"},
		{"terminating, linked in another namespace", "terminating", []linkedService{{"api", "other"}}, true,
			"parker-terminating-gateway-app-tgw", "", ""},
		{"ingress", "ingress", nil, false, "", "", ""},
		{"api: mesh read is partition-wide", "api", nil, true, "parker-api-gateway-app-tgw", "", ""},
	} {
		id := newIdentities("dc1", true)
		s, err := id.gateway(gatewayNode{group: "tgw", kind: c.kind}, "part-a", "app", c.linked)
		if err != nil {
			t.Fatal(err)
		}
		tok := id.tokens[0]
		if tok.SecretID != s || tok.Scoped != c.scoped || tok.Namespace != "app" || tok.Service != "tgw" {
			t.Errorf("%s: token %+v", c.name, tok)
		}
		var want []string
		if c.policy != "" {
			want = []string{c.policy}
		}
		if !slices.Equal(tok.Policies, want) {
			t.Errorf("%s: policies %v, want %v", c.name, tok.Policies, want)
		}
		for _, p := range id.policies {
			if p.Name == c.policy && (p.Namespace != c.policyNS || c.policyRule != "" && p.Rules != c.policyRule) {
				t.Errorf("%s: policy %+v", c.name, p)
			}
		}
	}
}
