package lab

import (
	"strings"
	"testing"
)

func TestTokenLabels(t *testing.T) {
	low := expandExample(t, "../../hack/e2e/tproxy-secure.yaml")
	labels := TokenLabels(low)
	mgmt := ManagementToken(low)
	if labels[mgmt] != "management token" {
		t.Errorf("management: %q", labels[mgmt])
	}
	var node, svc, dns bool
	for _, l := range labels {
		node = node || strings.HasPrefix(l, "node identity web (dc1)")
		svc = svc || strings.HasPrefix(l, "service identity web")
		dns = dns || strings.Contains(l, "policies parker-dns")
	}
	if !node || !svc || !dns {
		t.Errorf("labels: %v", labels)
	}

	// describe names the tokens a node's config carries, not others.
	_, out, err := describeFile(low, "dc1-web")
	if err != nil {
		t.Fatal(err)
	}
	notes := out[strings.Index(out, "# ACL tokens above:"):]
	if !strings.Contains(notes, "node identity web (dc1)") || !strings.Contains(notes, "service identity web") ||
		strings.Contains(notes, "node identity api") {
		t.Errorf("notes:\n%s", notes)
	}
}

func TestConsulEnv(t *testing.T) {
	low := expandExample(t, "../../hack/e2e/tproxy-secure.yaml")
	env, err := ConsulEnv(low, "dc1-web")
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(env, " ")
	if !strings.Contains(got, "CONSUL_HTTP_ADDR=https://10.1.0.") || !strings.Contains(got, ":8501") ||
		!strings.Contains(got, "CONSUL_HTTP_TOKEN="+ManagementToken(low)) || !strings.Contains(got, "/nodes/dc1-web/ca.pem") {
		t.Errorf("env: %s", got)
	}
	plain := expandExample(t, "../../examples/minimal.yaml")
	if env, _ := ConsulEnv(plain, "dc1-server-1"); len(env) != 1 || !strings.HasPrefix(env[0], "CONSUL_HTTP_ADDR=http://10.1.0.10:8500") {
		t.Errorf("minimal env: %v", env)
	}
}
