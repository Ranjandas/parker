package main

import (
	"runtime"
	"strings"
	"testing"
)

func TestVersionString(t *testing.T) {
	defer func(v, c, d string) { version, commit, date = v, c, d }(version, commit, date)
	version, commit, date = "v0.1.0", "abc1234", "2026-11-19T00:00:00Z"
	got := versionString()
	want := "parker v0.1.0 (commit abc1234, built 2026-11-19T00:00:00Z) " + runtime.GOOS + "/" + runtime.GOARCH
	if !strings.HasPrefix(got, want) {
		t.Errorf("versionString() = %q, want prefix %q", got, want)
	}
	commit = ""
	if got := versionString(); !strings.HasPrefix(got, "parker v0.1.0 "+runtime.GOOS) {
		t.Errorf("no commit: %q", got)
	}
}

func TestPseudoVersion(t *testing.T) {
	for v, pseudo := range map[string]bool{
		"v0.0.0-20261119104137-fb489a5deaf1":       true,
		"v0.0.0-20261119104137-fb489a5deaf1+dirty": true,
		"v0.1.1-0.20261119104137-fb489a5deaf1":     true,
		"v0.1.0":                                   false,
		"v0.2.0-rc1":                               false,
	} {
		if got := pseudoVersion.MatchString(v); got != pseudo {
			t.Errorf("pseudoVersion(%q) = %v, want %v", v, got, pseudo)
		}
	}
}
