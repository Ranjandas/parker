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

func TestBuildMismatch(t *testing.T) {
	defer func(v, c string) { version, commit = v, c }(version, commit)
	version, commit = "dev", "abc123def456"
	if buildID() != "dev+abc123def456" {
		t.Errorf("buildID = %q", buildID())
	}
	if err := buildMismatch("dev+abc123def456", 7, ""); err != nil {
		t.Errorf("same build: %v", err)
	}
	err := buildMismatch("dev+0123456789ab", 7, "")
	if err == nil || !strings.Contains(err.Error(), "pid 7") || !strings.Contains(err.Error(), "parker daemon stop") {
		t.Errorf("local mismatch: %v", err)
	}
	if err := buildMismatch("v0.3.0", 7, "https://lab:7443"); err != nil {
		t.Errorf("a remote daemon only warns: %v", err)
	}
	commit = ""
	if buildID() != "dev" {
		t.Errorf("no commit: %q", buildID())
	}
}
