package main

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/Ranjandas/parker/internal/expand"
)

type fakeTenancy struct {
	calls []string
	fail  map[string]int // call -> times to fail first
	err   string
}

func (f *fakeTenancy) do(call string) error {
	f.calls = append(f.calls, call)
	if f.fail[call] > 0 {
		f.fail[call]--
		return errors.New(f.err)
	}
	return nil
}

func (f *fakeTenancy) CreatePartition(_ context.Context, name string) error {
	return f.do("partition " + name)
}
func (f *fakeTenancy) CreateNamespace(p, name string) error {
	return f.do("namespace " + p + "/" + name)
}
func (f *fakeTenancy) SetNamespaceDefaults(p, name string, policies []string) error {
	return f.do("defaults " + p + "/" + name + " " + strings.Join(policies, "+"))
}
func (f *fakeTenancy) EnsurePolicy(p expand.IdentityPolicy) error {
	return f.do("policy " + p.Partition + "/" + p.Namespace + " " + p.Name)
}
func (f *fakeTenancy) EnsureToken(t expand.IdentityToken) error {
	return f.do("token " + t.Partition + " " + t.SecretID)
}

func TestApplyTenancy(t *testing.T) {
	spec := expand.TenancySpec{
		Partitions: []string{"team-a"},
		Namespaces: []expand.NamespaceRef{{Name: "payments"}, {Partition: "team-a", Name: "payments", PolicyDefaults: []string{"parker-read-everywhere"}}},
		Policies: []expand.IdentityPolicy{{Name: "parker-dns", Partition: "team-a"}, {Name: "parker-read-everywhere", Partition: "team-a"},
			{Name: "parker-terminating-gateway-tgw", Partition: "team-a", Namespace: "payments"}},
		Tokens: []expand.IdentityToken{{Partition: "team-a", SecretID: "s1", Node: "web", Policies: []string{"parker-dns"}}},
	}
	// The first try fails while the cluster settles; then everything goes
	// in order: partitions before the namespaces, policies and tokens in
	// them, and a namespace's ACL defaults after the policies they name.
	f := &fakeTenancy{fail: map[string]int{"partition team-a": 1}, err: "Unexpected response code: 500 (No cluster leader)"}
	if err := applyTenancy(context.Background(), f, spec, 5*time.Second, io.Discard); err != nil {
		t.Fatal(err)
	}
	want := "partition team-a,partition team-a,namespace /payments,namespace team-a/payments," +
		"policy team-a/ parker-dns,policy team-a/ parker-read-everywhere,policy team-a/payments parker-terminating-gateway-tgw," +
		"defaults team-a/payments parker-read-everywhere,token team-a s1"
	if got := strings.Join(f.calls, ","); got != want {
		t.Errorf("calls %s", got)
	}
	// Already there (a rerun) counts as done.
	f = &fakeTenancy{fail: map[string]int{"namespace /payments": 5}, err: `Unexpected response code: 500 (namespace "payments" already exists)`}
	if err := applyTenancy(context.Background(), f, spec, 0, io.Discard); err != nil {
		t.Errorf("already exists: %v", err)
	}
	// It gives up after its patience.
	f = &fakeTenancy{fail: map[string]int{"partition team-a": 5}, err: "Unexpected response code: 403 (Permission denied)"}
	err := applyTenancy(context.Background(), f, spec, 0, io.Discard)
	if err == nil || !strings.HasPrefix(err.Error(), "partition team-a: ") {
		t.Errorf("err %v", err)
	}
}
