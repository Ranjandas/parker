package expand

import (
	"errors"
	"slices"
	"testing"

	"github.com/Ranjandas/parker/internal/spec"
)

// testProduct records the passes it sees and reads another product's facts.
type testProduct struct {
	name  string
	in    bool
	calls *[]string
	read  string // a product whose facts Wire expects
}

func (p testProduct) Name() string       { return p.name }
func (p testProduct) In(*spec.File) bool { return p.in }
func (p testProduct) Expand(_, _ *spec.File, f Facts) error {
	*p.calls = append(*p.calls, "expand "+p.name)
	f[p.name] = p.name + "-facts"
	return nil
}
func (p testProduct) Wire(_, _ *spec.File, f Facts) error {
	*p.calls = append(*p.calls, "wire "+p.name)
	if p.read != "" && f[p.read] != p.read+"-facts" {
		return errors.New(p.name + ": no facts from " + p.read)
	}
	return nil
}

func TestProductsExpandThenWire(t *testing.T) {
	saved := products
	defer func() { products = saved }()
	var calls []string
	products = nil
	Register(testProduct{name: "a", in: true, calls: &calls, read: "b"})
	Register(testProduct{name: "skipped", calls: &calls})
	Register(testProduct{name: "b", in: true, calls: &calls, read: "a"})
	if err := expandProducts(&spec.File{}, &spec.File{}); err != nil {
		t.Fatal(err)
	}
	want := []string{"expand a", "expand b", "wire a", "wire b"}
	if !slices.Equal(calls, want) {
		t.Errorf("calls %v, want %v", calls, want)
	}
}

func TestRegisterTwicePanics(t *testing.T) {
	saved := products
	defer func() { products = saved }()
	defer func() {
		if recover() == nil {
			t.Error("no panic")
		}
	}()
	Register(consulProduct{})
}

func TestConsulIsRegistered(t *testing.T) {
	if !slices.Contains(Products(), "consul") {
		t.Errorf("products %v", Products())
	}
}
