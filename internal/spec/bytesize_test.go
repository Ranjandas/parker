package spec

import (
	"encoding/json"
	"testing"

	"github.com/goccy/go-yaml"
)

func TestParseByteSize(t *testing.T) {
	for in, want := range map[string]ByteSize{"8192": 8192, "8KB": 8192, "8kb": 8192, "8K": 8192, "8KiB": 8192,
		"1MB": 1 << 20, "1.5k": 1536, "2 MB": 2 << 20, "1GB": 1 << 30, "100B": 100, "0": 0} {
		got, err := ParseByteSize(in)
		if err != nil || got != want {
			t.Errorf("%q: got %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "KB", "8XB", "-1", "1..2"} {
		if _, err := ParseByteSize(in); err == nil {
			t.Errorf("%q: want an error", in)
		}
	}
}

func TestByteSizeString(t *testing.T) {
	for b, want := range map[ByteSize]string{8192: "8KB", 1 << 20: "1MB", 9120: "9120B", 0: "0B", 3 << 30: "3GB"} {
		if got := b.String(); got != want {
			t.Errorf("%d: got %s, want %s", b, got, want)
		}
	}
}

func TestByteSizeYAMLAndJSON(t *testing.T) {
	var l FakeLimits
	if err := yaml.Unmarshal([]byte("max_header_bytes: 8192\nmax_body_bytes: 1MB\nmax_url_bytes: '4KB'\n"), &l); err != nil {
		t.Fatal(err)
	}
	if *l.MaxHeaderBytes != 8192 || *l.MaxBodyBytes != 1<<20 || *l.MaxURLBytes != 4096 {
		t.Errorf("yaml: %d %d %d", *l.MaxHeaderBytes, *l.MaxBodyBytes, *l.MaxURLBytes)
	}
	if err := yaml.Unmarshal([]byte("max_header_bytes: lots\n"), &l); err == nil {
		t.Error("yaml: want an error for a bad size")
	}
	// The fake gets its block as JSON: numbers out, numbers or strings in.
	b, err := json.Marshal(l)
	if err != nil {
		t.Fatal(err)
	}
	var back FakeLimits
	if err := json.Unmarshal(b, &back); err != nil || *back.MaxBodyBytes != 1<<20 {
		t.Errorf("json round trip %s: %v", b, err)
	}
	if err := json.Unmarshal([]byte(`{"max_header_bytes":"8KB"}`), &back); err != nil || *back.MaxHeaderBytes != 8192 {
		t.Errorf("json string: %v", err)
	}
}
