package main

import (
	"errors"
	"testing"

	"github.com/hashicorp/consul/api"
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
