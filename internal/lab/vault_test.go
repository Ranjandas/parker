package lab

import "testing"

func TestVaultStateWord(t *testing.T) {
	for want, s := range map[string]VaultState{
		VaultStateUninitialized: {},
		VaultStateSealed:        {Initialized: true, Sealed: true},
		VaultStateStandby:       {Initialized: true, Standby: true},
		VaultStatePerfStandby:   {Initialized: true, Standby: true, PerfStandby: true},
		VaultStateActive:        {Initialized: true},
	} {
		if got := s.State(); got != want {
			t.Errorf("%+v: %q, want %q", s, got, want)
		}
	}
}
