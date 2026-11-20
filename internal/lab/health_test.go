package lab

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/hashicorp/consul/api"

	"github.com/Ranjandas/parker/internal/spec"
)

func TestServiceHealth(t *testing.T) {
	node := func(dc, part, name string) *spec.RawNode {
		return &spec.RawNode{Labels: &spec.NodeLabels{DC: dc, Partition: part, Name: name}}
	}
	low := &spec.File{Nodes: map[string]*spec.RawNode{
		"dc1-web":            node("dc1", "", "web"),
		"dc1-api":            node("dc1", "", "api"),
		"dc1-team-a-billing": node("dc1", "team-a", "billing"),
		"dc1-team-b-billing": node("dc1", "team-b", "billing"),
		"dc2-web":            node("dc2", "", "web"),
		"loose":              {},
	}}
	checks := map[string]api.HealthChecks{
		"dc1/default": {
			{Node: "web", ServiceName: "web", Status: api.HealthPassing},
			{Node: "web", ServiceName: "web", Status: api.HealthWarning}, // the worst check wins
			{Node: "web", ServiceName: "web-sidecar-proxy", Status: api.HealthPassing},
			{Node: "api", CheckID: "serfHealth", Status: api.HealthCritical}, // the node is down
			{Node: "api", ServiceName: "api", Status: api.HealthPassing},
			{Node: "elsewhere", ServiceName: "x", Status: api.HealthPassing}, // no such parker node
		},
		"dc1/team-a": {{Node: "billing", ServiceName: "billing", Status: api.HealthPassing}},
		"dc1/team-b": {{Node: "billing", ServiceName: "billing", Status: api.HealthCritical}},
	}
	got := serviceHealth(low, func(dc, part string) (api.HealthChecks, error) {
		if c, ok := checks[dc+"/"+part]; ok {
			return c, nil
		}
		return nil, errors.New("no servers answer") // dc2
	})
	want := map[string]map[string]string{
		"dc1-web":            {"web": "warning", "web-sidecar-proxy": "passing"},
		"dc1-api":            {"api": "critical"},
		"dc1-team-a-billing": {"billing": "passing"},
		"dc1-team-b-billing": {"billing": "critical"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("health\n got %v\nwant %v", got, want)
	}
}

func TestMembersFrom(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]any{{"Name": "a", "Status": 1}, {"Name": "b", "Status": 1}, {"Name": "c", "Status": 4}})
	}))
	defer srv.Close()
	if a, n := membersFrom(srv.Client(), srv.URL, ""); a != 2 || n != 3 {
		t.Errorf("members %d/%d, want 2/3", a, n)
	}
	if a, n := membersFrom(http.DefaultClient, "http://127.0.0.1:1", ""); a != 0 || n != 0 {
		t.Errorf("unreachable agent: %d/%d", a, n)
	}
}
