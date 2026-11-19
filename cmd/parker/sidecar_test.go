package main

import "testing"

func TestSidecarName(t *testing.T) {
	if got := sidecarName([]string{"consul", "connect", "envoy", "-sidecar-for", "web", "-admin-bind", "x"}); got != "web" {
		t.Errorf("sidecarName = %q", got)
	}
	if got := sidecarName([]string{"consul", "connect", "envoy", "-sidecar-for"}); got != "sidecar" {
		t.Errorf("sidecarName without a value = %q", got)
	}
}
