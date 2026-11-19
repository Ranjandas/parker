package supervisor

import (
	"errors"
	"testing"
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
