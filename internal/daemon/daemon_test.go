package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	parkerv1 "github.com/Ranjandas/parker/internal/api/parkerv1"
	"github.com/Ranjandas/parker/internal/lab"
)

const topo = `parker: v1alpha1
name: t
networks:
  lan: { subnet: 10.77.0.0/24 }
nodes:
  a:
    networks: { lan: { ip: 10.77.0.10 } }
    processes:
      app: { run: sleep 1 }
  b:
    networks: { lan: { ip: 10.77.0.11 } }
    processes:
      consul: { run: sleep 1 }
      extra: { run: sleep 1 }
`

func fakeLab(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	t.Setenv("PARKER_RUN_DIR", base)
	dir := filepath.Join(base, "t")
	if err := os.MkdirAll(filepath.Join(dir, "nodes", "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, lab.TopologyFile), []byte(topo), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestServeOverSocket(t *testing.T) {
	fakeLab(t)
	sock := filepath.Join(t.TempDir(), "p.sock")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, ServeOptions{Socket: sock}, os.Stderr) }()
	c := Client(sock)
	var err error
	for range 50 {
		if _, err = c.Version(ctx, connect.NewRequest(&parkerv1.VersionRequest{})); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	labs, err := c.ListLabs(ctx, connect.NewRequest(&parkerv1.ListLabsRequest{}))
	if err != nil || len(labs.Msg.Labs) != 1 || labs.Msg.Labs[0].Name != "t" {
		t.Fatalf("labs %v err %v", labs, err)
	}
	st, err := c.Status(ctx, connect.NewRequest(&parkerv1.StatusRequest{Lab: "t"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Msg.Nodes) != 2 || st.Msg.Nodes[0].Ips[0] != "10.77.0.10" || st.Msg.Supervised {
		t.Fatalf("status %v", st.Msg)
	}
	if _, err := c.Status(ctx, connect.NewRequest(&parkerv1.StatusRequest{Lab: "nope"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("want not found, got %v", err)
	}
	d, err := c.Describe(ctx, connect.NewRequest(&parkerv1.DescribeRequest{Lab: "t", Node: "b"}))
	if err != nil || d.Msg.Node != "b" || !strings.Contains(d.Msg.Yaml, "extra:") {
		t.Fatalf("describe %v err %v", d, err)
	}
	if _, err := c.Describe(ctx, connect.NewRequest(&parkerv1.DescribeRequest{Lab: "t", Node: "zz"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("describe unknown node: %v", err)
	}
	if err := Serve(ctx, ServeOptions{Socket: sock}, os.Stderr); err == nil || !strings.Contains(err.Error(), "already listening") {
		t.Fatalf("second daemon: %v", err)
	}
	if _, err := c.Shutdown(ctx, connect.NewRequest(&parkerv1.ShutdownRequest{})); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("daemon did not stop")
	}
	cancel()
}

func TestLogsTailAndFollow(t *testing.T) {
	dir := fakeLab(t)
	path := filepath.Join(dir, "nodes", "a", "app.log")
	if err := os.WriteFile(path, []byte("1\n2\n3\n4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := LogPath("t", "a", "")
	if err != nil || got != path {
		t.Fatalf("LogPath %q %v", got, err)
	}
	var out strings.Builder
	send := func(b []byte) error { out.Write(b); return nil }
	if err := FollowLog(context.Background(), path, 2, false, send); err != nil {
		t.Fatal(err)
	}
	if out.String() != "3\n4\n" {
		t.Fatalf("tail 2: %q", out.String())
	}
	out.Reset()
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- FollowLog(ctx, path, 0, true, send) }()
	time.Sleep(100 * time.Millisecond)
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("5\n")
	f.Close()
	time.Sleep(400 * time.Millisecond)
	cancel()
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	if out.String() != "5\n" {
		t.Fatalf("follow: %q", out.String())
	}
}

func TestLogPathDefaults(t *testing.T) {
	fakeLab(t)
	if p, err := LogPath("t", "b", ""); err != nil || filepath.Base(p) != "consul.log" {
		t.Fatalf("%q %v", p, err)
	}
	if _, err := LogPath("t", "b", "nope"); err == nil {
		t.Fatal("want error for unknown process")
	}
	if _, err := LogPath("t", "zzz", ""); err == nil {
		t.Fatal("want error for unknown node")
	}
}

func TestLineWriter(t *testing.T) {
	var lines []string
	w := &lineWriter{send: func(l string) error { lines = append(lines, l); return nil }}
	w.Write([]byte("a\nb"))
	w.Write([]byte("c\n"))
	w.Write([]byte("tail"))
	w.Flush()
	if strings.Join(lines, "|") != "a|bc|tail" {
		t.Fatalf("%q", lines)
	}
}

func TestResolveAddress(t *testing.T) {
	fakeLab(t)
	for in, want := range map[string]string{
		"10.77.0.10:22":   "10.77.0.10:22",
		"a.t.parker:8500": "10.77.0.10:8500",
		"a.t:8500":        "10.77.0.10:8500",
		"b.parker:80":     "10.77.0.11:80",
		"B.T.PARKER.:80":  "10.77.0.11:80",
	} {
		got, err := ResolveAddress(in, "")
		if err != nil || got != want {
			t.Errorf("%s: got %q %v, want %s", in, got, err, want)
		}
	}
	if got, err := ResolveAddress("a:1", "t"); err != nil || got != "10.77.0.10:1" {
		t.Errorf("bare node with lab: %q %v", got, err)
	}
	for _, bad := range []string{"192.168.1.1:22", "127.0.0.1:8500", "zzz.t.parker:1", "a.other.parker:1", "a.b.c.d.e.parker:1"} {
		if _, err := ResolveAddress(bad, ""); err == nil {
			t.Errorf("%s: want refusal", bad)
		}
	}
}

func TestResolvePartitionNames(t *testing.T) {
	dir := fakeLab(t)
	part := topo + `  dc1-billing-1:
    networks: { lan: { ip: 10.77.0.12 } }
    labels: { dc: dc1, name: billing-1, role: client, partition: team-a }
  dc1-web-1:
    networks: { lan: { ip: 10.77.0.13 } }
    labels: { dc: dc1, name: web-1, role: client }
`
	if err := os.WriteFile(filepath.Join(dir, lab.TopologyFile), []byte(part), 0o644); err != nil {
		t.Fatal(err)
	}
	// Partitioned nodes keep their dc-unique name; the partition form
	// mirrors the dc1/team-a/billing-1 selector.
	for in, want := range map[string]string{
		"billing-1.dc1.t.parker:80":        "10.77.0.12:80",
		"dc1-billing-1.t.parker:80":        "10.77.0.12:80",
		"billing-1.team-a.dc1.t.parker:80": "10.77.0.12:80",
		"web-1.default.dc1.t.parker:80":    "10.77.0.13:80",
	} {
		got, err := ResolveAddress(in, "")
		if err != nil || got != want {
			t.Errorf("%s: got %q %v, want %s", in, got, err, want)
		}
	}
	for _, bad := range []string{"billing-1.default.dc1.t.parker:1", "web-1.team-a.dc1.t.parker:1"} {
		if _, err := ResolveAddress(bad, ""); err == nil {
			t.Errorf("%s: want refusal", bad)
		}
	}
}
