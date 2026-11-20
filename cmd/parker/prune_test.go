package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestPrintLabs(t *testing.T) {
	var b bytes.Buffer
	printLabs(&b, []labState{{name: "a", namespaces: 4, running: true}, {name: "b"}}, false)
	out := b.String()
	for _, want := range []string{"LAB  STATE    NAMESPACES", "a    running  4", "b    down     0", "1 down (logs kept); parker down LAB --purge or parker prune removes them"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if strings.Contains(out, "SUPERVISED") {
		t.Error("no daemon, no SUPERVISED column")
	}
	b.Reset()
	printLabs(&b, []labState{{name: "a", namespaces: 4, supervised: true, running: true}}, true)
	if out := b.String(); !strings.Contains(out, "SUPERVISED") || strings.Contains(out, "down") {
		t.Errorf("daemon listing:\n%s", out)
	}
}

func TestConfirm(t *testing.T) {
	for in, want := range map[string]bool{"y\n": true, "YES\n": true, "n\n": false, "\n": false, "": false} {
		if got, err := confirm(strings.NewReader(in), ""); err != nil || got != want {
			t.Errorf("%q: %v %v", in, got, err)
		}
	}
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := confirm(f, ""); err == nil || !strings.Contains(err.Error(), "pass -y") {
		t.Errorf("no terminal: %v", err)
	}
}
