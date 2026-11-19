package supervisor

import (
	"errors"
	"slices"
	"testing"

	"github.com/Ranjandas/parker/internal/spec"
)

func TestShouldRestart(t *testing.T) {
	fail := errors.New("exit status 1")
	for _, c := range []struct {
		policy string
		err    error
		want   bool
	}{
		{"", fail, false}, {"no", fail, false},
		{"on-failure", nil, false}, {"on-failure", fail, true},
		{"always", nil, true}, {"always", fail, true},
	} {
		if got := ShouldRestart(c.policy, c.err); got != c.want {
			t.Errorf("ShouldRestart(%q, %v) = %v", c.policy, c.err, got)
		}
	}
}

func TestUsedKeys(t *testing.T) {
	f := &spec.File{Versions: map[string]string{"consul": "1.22.6", "envoy": "1.35.12"}}
	p := &spec.Process{Run: []string{"consul"}, Uses: []string{"envoy", "nomad"}}
	if got := usedKeys(f, p); !slices.Equal(got, []string{"envoy@1.35.12"}) {
		t.Errorf("usedKeys = %v, want the pinned envoy only", got)
	}
}
