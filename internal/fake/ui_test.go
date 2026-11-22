package fake

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// uiHelpers runs the pure helpers of ui.html with node over a few answers
// and returns what they made of them.
func uiHelpers(t *testing.T, driver string) map[string]json.RawMessage {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	page := string(uiPage)
	const from, to = "// ---------- pure helpers", "// ---------- end of pure helpers ----------"
	i, j := strings.Index(page, from), strings.Index(page, to)
	if i < 0 || j < i {
		t.Fatal("ui.html: pure helpers block not found")
	}
	dir := t.TempDir()
	src := page[i:j] + "\n" + driver
	file := filepath.Join(dir, "ui.js")
	if err := os.WriteFile(file, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(node, file).CombinedOutput()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, out)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("node output: %v\n%s", err, out)
	}
	return got
}

func TestUIHelpers(t *testing.T) {
	// web -> api -> db, as the fake answers; api failed because this page
	// broke it, and web answered 500 because of it.
	const answers = `
const ok = { name: "web", node: "dc1-web", dc: "dc1", code: 200, duration: "2.3ms", ip_addresses: ["10.0.1.12"], fact: "web fact",
  upstream_calls: { "http://localhost:9091": { name: "api", node: "dc2-api", dc: "dc2", version: "v1", code: 200, duration: "1.4ms", fact: "api fact",
    upstream_calls: { "http://localhost:9092": { name: "db", node: "dc2-db", dc: "dc2", code: 200, duration: "400us", fact: "db fact", ip_addresses: ["10.0.2.30"] } } } } };
const broke = { name: "web", code: 500, duration: "1.9ms", error: "upstream http://localhost:9091 failed",
  upstream_calls: { "http://localhost:9091": { name: "api", dc: "dc2", code: 500, injected: "error", duration: "300us" } } };
const own = { name: "web", code: 503, duration: "180ms", injected: "error" };
const down = { name: "web", code: 500, duration: "1ms", upstream_calls: { "http://localhost:9091": { name: "api", code: -1, error: "connection refused" } } };
const recs = [
  { fail: false, ms: 2, labels: { api: "dc2 v1", db: "dc2" } },
  { fail: false, ms: 4, labels: { api: "dc3", db: "dc2" } },
  { fail: true, ms: 10, labels: { api: "dc2 v1" } },
  { fail: true, error: true, ms: 0 },
];
const rows = [{ svc: "api", on: true, fail: 100, delay: 0 }, { svc: "db", on: true, fail: 0, delay: 2000 }, { svc: "x", on: false, fail: 100, delay: 0 }];
console.log(JSON.stringify({
  ms: [FakeUI.ms("1.5ms"), FakeUI.ms("2m3s"), FakeUI.ms("400µs")],
  below: FakeUI.below(ok),
  header: FakeUI.header(rows),
  summary: FakeUI.brokeSummary(rows),
  off: FakeUI.header([{ svc: "api", on: true, fail: 0, delay: 0 }]),
  headlines: [FakeUI.headline(ok, []), FakeUI.headline(broke, ["api"]), FakeUI.headline(broke, []), FakeUI.headline(own, []), FakeUI.headline(down, [])],
  whys: [FakeUI.why(broke.upstream_calls["http://localhost:9091"], ["api"]), FakeUI.why(broke, ["api"]), FakeUI.why(own, []), FakeUI.why(down.upstream_calls["http://localhost:9091"], []), FakeUI.why(ok, [])],
  fact: FakeUI.factHop(ok).r.name,
  labels: FakeUI.labelsOf(ok),
  stats: FakeUI.stats(recs),
  sums: [FakeUI.faultSummary({ health_code: 200, error_rate: 0.5, error_code: 503, p50: 180e6, p99: 180e6, rate_limit: 0 }),
    FakeUI.faultSummary({ health_code: 503, p50: 100e6, p99: 2000e6, rate_limit: 5 }), FakeUI.faultSummary({ health_code: 200 })],
  labs: [FakeUI.labFromHost("dc1-web-8080--peering.parker.localhost:35237"), FakeUI.labFromHost("127.0.0.1:8080")],
}));
`
	got := uiHelpers(t, answers)
	want := map[string]string{
		"ms":      `[1.5,123000,0.4]`,
		"below":   `["api","db"]`,
		"header":  `"api=error-rate:100;db=delay:2000ms"`,
		"summary": `"api: fail 100% · db: +2000ms"`,
		"off":     `""`,
		"headlines": `["web answered in 2.3ms","api failed, broken from this page, so web answered 500",` +
			`"api failed on purpose, so web answered 500","web failed on purpose in 180ms","api failed, so web answered 500"]`,
		"whys": `[{"kind":"req","text":"Broken for requests from this page"},{"kind":"up","text":"an upstream failed"},` +
			`{"kind":"own","text":"Fault injection on web: error 503"},{"kind":"real","text":"connection refused"},null]`,
		"fact":   `"db"`,
		"labels": `{"api":"dc2 v1","db":"dc2"}`,
		"stats":  `{"n":4,"ok":50,"p50":4,"p99":10,"split":[{"svc":"api","label":"dc2 v1","pct":67},{"svc":"api","label":"dc3","pct":33}]}`,
		"labs":   `["peering","LAB"]`,
		"sums":   `["errors 50% (503) · latency 180ms","health 503 · latency 100–2000ms · rate limit 5/s",""]`,
	}
	for k, w := range want {
		if string(got[k]) != w {
			t.Errorf("%s:\n got %s\nwant %s", k, got[k], w)
		}
	}
}

// The page sends X-Parker-Fault for "Break an upstream" and reads the
// request id from the answer's header, so both must reach it.
func TestUIPageNames(t *testing.T) {
	page := string(uiPage)
	for _, s := range []string{UIControlPath, HeaderFault, HeaderRequestID, "Break an upstream for requests from this page", "Fault injection", "Space fact"} {
		if !strings.Contains(page, s) {
			t.Errorf("ui.html does not mention %q", s)
		}
	}
}
