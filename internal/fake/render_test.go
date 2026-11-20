package fake

import (
	"strings"
	"testing"
)

func chain() *Response {
	return &Response{Name: "web", Node: "dc1-web", DC: "dc1", Version: "v1", Code: 200, Duration: "12ms", Fact: "f1",
		UpstreamCalls: map[string]*Response{
			"http://127.0.0.1:9091/": {Name: "api", Node: "dc2-api", DC: "dc2", Code: 503, Duration: "3ms", Injected: "error",
				UpstreamCalls: map[string]*Response{"http://db/": {Name: "db", DC: "dc2", Code: 200, Duration: "1ms", Cycle: true}}},
			"tcp://127.0.0.1:9093": {URI: "tcp://127.0.0.1:9093", Type: "TCP", Code: -1, Error: "connection refused"},
		}}
}

func TestTree(t *testing.T) {
	got := strings.Join(Tree(chain(), false), "\n")
	want := `web  dc1-web · dc1 · v1  200  12ms
├─ api  dc2-api · dc2  503  3ms  injected error
│  └─ db  dc2  200  1ms  cycle: stopped here
└─ tcp://127.0.0.1:9093  TCP  failed  connection refused`
	if got != want {
		t.Errorf("tree:\n%s\nwant:\n%s", got, want)
	}
	if got := Tree(chain(), true); got[1] != "│  ✦ f1" {
		t.Errorf("fact line = %q", got[1])
	}
}

func TestOnelineAndSplit(t *testing.T) {
	if got := Oneline(chain()); got != "200  12ms      web@dc1/v1 → api@dc2(503) → db@dc2 → tcp://127.0.0.1:9093(-1)" {
		t.Errorf("oneline = %q", got)
	}
	var s Split
	for i := range 10 {
		dc := "dc1"
		if i >= 7 {
			dc = "dc2"
		}
		s.Add(&Response{Name: "web", DC: "dc1", Code: 200, UpstreamCalls: map[string]*Response{"a": {Name: "api", DC: dc, Version: "v1", Code: 200}}})
	}
	s.Add(&Response{Name: "web", DC: "dc1", Code: 500})
	if s.Total != 11 || s.Failed() != 1 {
		t.Errorf("total %d failed %d", s.Total, s.Failed())
	}
	// web only ever answered from dc1: no split to report.
	if got := s.Lines(); len(got) != 1 || got[0] != "api: dc1 v1 70% · dc2 v1 30%" {
		t.Errorf("split = %q", got)
	}
}
