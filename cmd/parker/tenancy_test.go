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
func (f *fakeTenancy) EnsureAgentToken(p, secret string) error {
	return f.do("token " + p + " " + secret)
}

func TestApplyTenancy(t *testing.T) {
	spec := expand.TenancySpec{
		Partitions:  []string{"team-a"},
		Namespaces:  []expand.NamespaceRef{{Name: "payments"}, {Partition: "team-a", Name: "payments"}},
		AgentTokens: []expand.AgentToken{{Partition: "team-a", SecretID: "s1"}},
	}
	// The first try fails while the cluster settles; then everything goes
	// in order: partitions before the namespaces and tokens in them.
	f := &fakeTenancy{fail: map[string]int{"partition team-a": 1}, err: "Unexpected response code: 500 (No cluster leader)"}
	if err := applyTenancy(context.Background(), f, spec, 5*time.Second, io.Discard); err != nil {
		t.Fatal(err)
	}
	want := "partition team-a,partition team-a,namespace /payments,namespace team-a/payments,token team-a s1"
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
