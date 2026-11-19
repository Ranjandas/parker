package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	parkerv1 "github.com/Ranjandas/parker/internal/api/parkerv1"
)

func sampleStatus() *parkerv1.StatusResponse {
	yes, no := true, false
	return &parkerv1.StatusResponse{
		Lab: "demo", RunDir: "/run/parker/demo", Supervised: true,
		Nodes: []*parkerv1.NodeStatus{
			{Name: "dc1-server-1", Ips: []string{"10.1.0.10"}, Namespace: true, Role: "leader",
				Processes: []*parkerv1.ProcessStatus{{Name: "consul", Pid: 101, Alive: true, Ready: &yes}}},
			{Name: "dc1-web-1", Ips: []string{"10.1.0.101"}, Namespace: true, Role: "client",
				Processes: []*parkerv1.ProcessStatus{
					{Name: "consul", Pid: 102, Alive: true, Ready: &no},
					{Name: "web", Pid: 103, Alive: true, Restarts: 2},
				}},
			{Name: "gone", Ips: []string{"10.1.0.200"}, Namespace: false,
				Processes: []*parkerv1.ProcessStatus{{Name: "sleeper"}}},
		},
	}
}

func TestPrintStatusTable(t *testing.T) {
	var b bytes.Buffer
	printStatus(&b, sampleStatus())
	want := `lab demo: supervised by parker daemon; run dir /run/parker/demo
NODE          IP                     PROCESS  PID  STATE     RESTARTS  ROLE
dc1-server-1  10.1.0.10              consul   101  ready     0         leader
dc1-web-1     10.1.0.101             consul   102  starting  0         client
dc1-web-1     10.1.0.101             web      103  running   2         -
gone          10.1.0.200 (no netns)  sleeper  -    dead      0         -
`
	if b.String() != want {
		t.Errorf("got:\n%s\nwant:\n%s", b.String(), want)
	}
}

func TestPrintStatusJSON(t *testing.T) {
	var b bytes.Buffer
	if err := printStatusJSON(&b, sampleStatus()); err != nil {
		t.Fatal(err)
	}
	var v struct {
		Lab   string
		Nodes []struct {
			Name      string
			Role      string
			Processes []struct {
				Name     string
				Pid      int
				Ready    *bool
				Restarts int
			}
		}
	}
	if err := json.Unmarshal(b.Bytes(), &v); err != nil {
		t.Fatalf("%v\n%s", err, b.String())
	}
	if v.Lab != "demo" || len(v.Nodes) != 3 || v.Nodes[0].Role != "leader" || v.Nodes[1].Processes[1].Restarts != 2 {
		t.Errorf("decoded %+v", v)
	}
	if !strings.Contains(b.String(), "\n  \"lab\": \"demo\",\n") {
		t.Errorf("not stable two-space indentation:\n%s", b.String())
	}
	var again bytes.Buffer
	printStatusJSON(&again, sampleStatus())
	if again.String() != b.String() {
		t.Error("output differs between runs")
	}
}
