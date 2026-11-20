package fake

import (
	"context"
	"io"
	"log"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ranjandas/parker/internal/spec"
)

func TestControl(t *testing.T) {
	c, err := Load("api", "", 1, env(map[string]string{EnvConfig: `{"latency":{"p50":"20ms"}}`}))
	if err != nil {
		t.Fatal(err)
	}
	s := New(c)
	s.log = log.New(io.Discard, "", 0)
	sock := filepath.Join(t.TempDir(), SocketName("api"))
	if err := s.ListenControl(sock); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	f, err := Control(ctx, sock, nil, false)
	if err != nil || f.P50 != c.Faults.P50 || f.HealthCode != 200 {
		t.Fatalf("get: %+v %v", f, err)
	}
	rate := 0.3
	f, err = Control(ctx, sock, &spec.Fake{Health: &spec.FakeHealth{Code: 503}, Errors: &spec.FakeErrors{Rate: &rate, Code: 502}}, false)
	if err != nil || f.HealthCode != 503 || f.ErrorRate != 0.3 || f.ErrorCode != 502 || f.P50 != c.Faults.P50 {
		t.Fatalf("change: %+v %v", f, err)
	}
	if got := s.Faults(); got != f {
		t.Errorf("server has %+v", got)
	}
	// A change without a rate keeps it.
	f, _ = Control(ctx, sock, &spec.Fake{Errors: &spec.FakeErrors{Code: 504}}, false)
	if f.ErrorRate != 0.3 || f.ErrorCode != 504 {
		t.Errorf("code only: %+v", f)
	}
	if _, err := Control(ctx, sock, &spec.Fake{Health: &spec.FakeHealth{Code: 7}}, false); err == nil || !strings.Contains(err.Error(), "health.code") {
		t.Errorf("bad change: %v", err)
	}
	f, err = Control(ctx, sock, nil, true)
	if err != nil || f != c.Faults {
		t.Errorf("reset: %+v %v", f, err)
	}
	if _, err := Control(ctx, filepath.Join(t.TempDir(), "none.sock"), nil, false); err == nil || !strings.Contains(err.Error(), "no control socket") {
		t.Errorf("missing socket: %v", err)
	}
}

func TestSummary(t *testing.T) {
	f := DefaultFaults()
	if got := f.Summary(); got != "health 200 · errors off · latency off · rate limit off" {
		t.Errorf("default: %s", got)
	}
	f.HealthCode, f.ErrorRate, f.ErrorCode = 503, 0.3, 503
	f.P50, f.P99, f.Variance = 20e6, 1e9, 10
	f.RateLimit, f.RateLimitCode = 50, 429
	if got := f.Summary(); got != "health 503 · errors 30% (503) · latency p50 20ms p99 1s ±10% · rate limit 50/s (429)" {
		t.Errorf("faults: %s", got)
	}
}
