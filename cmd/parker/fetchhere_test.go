package main

import (
	"testing"

	"github.com/Ranjandas/parker/internal/daemon"
)

// `up` fetches on this machine only when the daemon shares its cache and
// platform; from macOS (even through a tunnelled socket) or over https the
// daemon fetches for its own OS and CPU.
func TestFetchHere(t *testing.T) {
	for _, c := range []struct {
		host, goos string
		want       bool
	}{
		{"", "linux", true},
		{"unix:///run/parker/parker.sock", "linux", true},
		{"https://lab-vm:7443", "linux", false},
		{"unix:///tmp/parker.sock", "darwin", false},
		{"https://lab-vm:7443", "darwin", false},
		{"", "darwin", false},
	} {
		if got := fetchHere(daemon.Target{Host: c.host}, c.goos); got != c.want {
			t.Errorf("fetchHere(%q, %s) = %v, want %v", c.host, c.goos, got, c.want)
		}
	}
}
