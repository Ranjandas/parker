package main

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/hashicorp/consul/api"

	"github.com/Ranjandas/parker/internal/expand"
)

type fakeACL struct {
	policies map[string]*api.ACLPolicy
	tokens   map[string]*api.ACLToken
	creates  int
}

func (f *fakeACL) PolicyReadByName(n string, _ *api.QueryOptions) (*api.ACLPolicy, *api.QueryMeta, error) {
	if p := f.policies[n]; p != nil {
		return p, nil, nil
	}
	return nil, nil, errors.New("Unexpected response code: 404 (ACL not found)")
}

func (f *fakeACL) PolicyCreate(p *api.ACLPolicy, _ *api.WriteOptions) (*api.ACLPolicy, *api.WriteMeta, error) {
	f.policies[p.Name] = p
	f.creates++
	return p, nil, nil
}

func (f *fakeACL) TokenReadSelf(q *api.QueryOptions) (*api.ACLToken, *api.QueryMeta, error) {
	if t := f.tokens[q.Token]; t != nil {
		return t, nil, nil
	}
	return nil, nil, errors.New("Unexpected response code: 403 (ACL not found)")
}

func (f *fakeACL) TokenCreate(t *api.ACLToken, _ *api.WriteOptions) (*api.ACLToken, *api.WriteMeta, error) {
	f.tokens[t.SecretID] = t
	f.creates++
	return t, nil, nil
}

func TestEnsureAgentToken(t *testing.T) {
	f := &fakeACL{policies: map[string]*api.ACLPolicy{}, tokens: map[string]*api.ACLToken{}}
	const secret = "11111111-2222-4333-8444-555555555555"
	if err := ensureAgentToken(f, secret, aclAgentRules, ""); err != nil {
		t.Fatal(err)
	}
	tok := f.tokens[secret]
	if tok == nil || len(tok.Policies) != 1 || tok.Policies[0].Name != "parker-agent" {
		t.Fatalf("token: %+v", tok)
	}
	if f.policies["parker-agent"].Rules != aclAgentRules {
		t.Error("policy rules")
	}
	// A second run (restart) creates nothing.
	if err := ensureAgentToken(f, secret, aclAgentRules, ""); err != nil || f.creates != 2 {
		t.Errorf("rerun: err %v, creates %d", err, f.creates)
	}
}

type failingACL struct{ fakeACL }

func (failingACL) PolicyReadByName(string, *api.QueryOptions) (*api.ACLPolicy, *api.QueryMeta, error) {
	return nil, nil, errors.New("Unexpected response code: 500 (No cluster leader)")
}

func TestEnsureAgentTokenNoLeader(t *testing.T) {
	if err := ensureAgentToken(&failingACL{}, "x", aclAgentRules, ""); err == nil {
		t.Error("want error without a leader, so acl-setup retries")
	}
}

// Consul Enterprise checks node:read in the namespace of each instance it
// returns, so every namespace needs node read for a sidecar to see its
// upstreams' nodes (a terminating gateway's, in tgw-failover-ent).
func TestAgentRulesNamespaceNodeRead(t *testing.T) {
	for name, rules := range map[string]string{"default partition": aclAgentRulesEnt} {
		_, ns, ok := strings.Cut(rules, `namespace_prefix "" {`)
		if !ok || !strings.Contains(ns, `node_prefix "" { policy = "read" }`) || !strings.Contains(ns, `service_prefix "" { policy = "write" }`) {
			t.Errorf("%s: namespace block lacks node read or service write:\n%s", name, rules)
		}
	}
}

func TestEnsureIdentityToken(t *testing.T) {
	f := &fakeACL{policies: map[string]*api.ACLPolicy{}, tokens: map[string]*api.ACLToken{}}
	pol := expand.IdentityPolicy{Name: "parker-tgw", Partition: "part-a", Rules: `service "api" { policy = "write" }`}
	if err := ensurePolicy(f, pol); err != nil {
		t.Fatal(err)
	}
	f.policies["parker-tgw"].ID = "pol-1"
	tok := expand.IdentityToken{SecretID: "s-1", Description: "Parker terminating gateway tgw", Partition: "part-a", Namespace: "app",
		Service: "tgw", Policies: []string{"parker-tgw"}}
	if err := ensureIdentityToken(f, tok); err != nil {
		t.Fatal(err)
	}
	got := f.tokens["s-1"]
	if got == nil || got.Partition != "part-a" || got.Namespace != "app" || len(got.ServiceIdentities) != 1 || got.ServiceIdentities[0].ServiceName != "tgw" ||
		len(got.Policies) != 1 || got.Policies[0].ID != "pol-1" {
		t.Fatalf("token: %+v", got)
	}
	// A scoped token sits in the partition's default namespace, with no
	// service identity: its policy carries the rules.
	scoped := expand.IdentityToken{SecretID: "s-3", Partition: "part-a", Namespace: "app", Service: "tgw", Scoped: true, Policies: []string{"parker-tgw"}}
	if err := ensureIdentityToken(f, scoped); err != nil {
		t.Fatal(err)
	}
	if got := f.tokens["s-3"]; got == nil || got.Partition != "part-a" || got.Namespace != "" || len(got.ServiceIdentities) != 0 || len(got.Policies) != 1 {
		t.Fatalf("scoped token: %+v", got)
	}
	node := expand.IdentityToken{SecretID: "s-2", Node: "web", Datacenter: "dc1"}
	if err := ensureIdentityToken(f, node); err != nil {
		t.Fatal(err)
	}
	if n := f.tokens["s-2"].NodeIdentities; len(n) != 1 || n[0].NodeName != "web" || n[0].Datacenter != "dc1" {
		t.Errorf("node identity: %+v", n)
	}
	// A rerun creates nothing.
	creates := f.creates
	if err := ensurePolicy(f, pol); err != nil || ensureIdentityToken(f, tok) != nil || f.creates != creates {
		t.Errorf("rerun created %d more", f.creates-creates)
	}
}

// docs/acl.md quotes the servers' rules; keep it in step.
func TestACLDocQuotesAgentRules(t *testing.T) {
	b, err := os.ReadFile("../../docs/acl.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, rules := range []string{aclAgentRules, aclNamespaceRules} {
		if !strings.Contains(string(b), "```hcl\n"+rules+"```") {
			t.Errorf("docs/acl.md doesn't quote:\n%s", rules)
		}
	}
}
