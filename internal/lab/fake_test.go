package lab

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ranjandas/parker/internal/expand"
	"github.com/Ranjandas/parker/internal/fake"
	"github.com/Ranjandas/parker/internal/spec"
	"github.com/Ranjandas/parker/internal/supervisor"
)

func TestScenarioFake(t *testing.T) {
	t.Setenv("PARKER_RUN_DIR", t.TempDir())
	low := &spec.File{Name: "demo", Nodes: map[string]*spec.RawNode{
		"dc1-api": {Processes: map[string]*spec.Process{
			"api":    {Env: map[string]string{expand.FakeEnv: "{}"}},
			"consul": {},
		}},
		"dc1-server-1": {Processes: map[string]*spec.Process{"consul": {}}},
	}}
	if got := FakeServices(low, "dc1-api"); len(got) != 1 || got[0] != "api" {
		t.Fatalf("FakeServices = %v", got)
	}
	dir := supervisor.NodeDir(RunDir("demo"), "dc1-api")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg, err := fake.Load("api", "dc1", 1, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	s := fake.New(cfg)
	s.SetLogOutput(io.Discard)
	if err := s.ListenControl(filepath.Join(dir, fake.SocketName("api"))); err != nil {
		t.Fatal(err)
	}
	l := &ScenarioLab{Low: low}
	ctx := context.Background()

	lines, err := l.Fake(ctx, "dc1-api", &spec.FakeStep{On: "dc1/api", Health: &spec.FakeHealth{Code: 503}})
	if err != nil || len(lines) != 1 || !strings.HasPrefix(lines[0], "dc1-api/api: health 503") {
		t.Fatalf("change: %v %v", lines, err)
	}
	if s.Faults().HealthCode != 503 {
		t.Error("not applied")
	}
	if lines, _ := l.Fake(ctx, "dc1-api", &spec.FakeStep{On: "dc1/api", Reset: true}); !strings.HasPrefix(lines[0], "dc1-api/api: health 200") {
		t.Errorf("reset: %v", lines)
	}
	if _, err := l.Fake(ctx, "dc1-server-1", &spec.FakeStep{On: "dc1"}); err == nil || !strings.Contains(err.Error(), "no fake services") {
		t.Errorf("no fakes: %v", err)
	}
	if _, err := l.Fake(ctx, "dc1-api", &spec.FakeStep{On: "dc1", Service: "web"}); err == nil || !strings.Contains(err.Error(), `no fake service "web"`) {
		t.Errorf("wrong service: %v", err)
	}
}
