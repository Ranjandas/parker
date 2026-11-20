package labfile

import "testing"

func TestSpans(t *testing.T) {
	src := `# head
name: x
consul:
  datacenters:
    dc1:
      servers: 1 # one
      nodes:
        web: { service: { port: 8080 } }
        api:
          service:
            port: 9090
      config_entries:
        - Kind: proxy-defaults
          Name: global
        - { Kind: service-defaults, Name: api }
`
	got := Spans(src)
	want := map[string][2]int{
		"name":                             {2, 2},
		"consul":                           {3, 15},
		"consul.datacenters.dc1":           {5, 15},
		"consul.datacenters.dc1.servers":   {6, 6},
		"consul.datacenters.dc1.nodes.web": {8, 8},
		"consul.datacenters.dc1.nodes.web.service.port": {8, 8},
		"consul.datacenters.dc1.nodes.api":              {9, 11},
		"consul.datacenters.dc1.config_entries[0]":      {13, 14},
		"consul.datacenters.dc1.config_entries[1]":      {15, 15},
		"consul.datacenters.dc1.config_entries[0].Name": {14, 14},
	}
	for p, w := range want {
		if got[p] != w {
			t.Errorf("%s: %v, want %v", p, got[p], w)
		}
	}
	if len(Spans("a: [")) != 0 {
		t.Error("spans of a broken file")
	}
}
