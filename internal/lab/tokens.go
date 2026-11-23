package lab

import (
	"encoding/json"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Ranjandas/parker/internal/expand"
	"github.com/Ranjandas/parker/internal/spec"
	"github.com/Ranjandas/parker/internal/supervisor"
)

// TokenLabels names every ACL token of a lab by what it is for, by its
// SecretID: the management token, the servers' agent token and each
// identity token (expand/identity.go). Troubleshooting needs to know whose
// token a config carries without anyone pasting secrets around (#133).
func TokenLabels(low *spec.File) map[string]string {
	out := map[string]string{}
	if m := ManagementToken(low); m != "" {
		out[m] = "management token"
	}
	for _, name := range slices.Sorted(maps.Keys(low.Nodes)) {
		n := low.Nodes[name]
		if s := serverAgentToken(n); s != "" {
			out[s] = "servers' agent token (parker-agent policy)"
		}
		var ten expand.TenancySpec
		if b := n.Files[expand.TenancyManifest]; b == "" || json.Unmarshal([]byte(b), &ten) != nil {
			continue
		}
		for _, t := range ten.Tokens {
			out[t.SecretID] = identityLabel(t)
		}
	}
	return out
}

func identityLabel(t expand.IdentityToken) string {
	var what string
	switch {
	case t.Node != "":
		what = "node identity " + t.Node + " (" + t.Datacenter + ")"
	case t.Service != "":
		what = "service identity " + t.Service
	default:
		what = strings.ToLower(strings.TrimPrefix(t.Description, "Parker "))
	}
	var where []string
	if t.Partition != "" {
		where = append(where, "partition "+t.Partition)
	}
	if t.Scoped {
		where = append(where, "default namespace, scoped to "+t.Namespace)
	} else if t.Namespace != "" {
		where = append(where, "namespace "+t.Namespace)
	}
	if len(t.Policies) > 0 {
		where = append(where, "policies "+strings.Join(t.Policies, ", "))
	}
	if len(where) > 0 {
		what += "; " + strings.Join(where, "; ")
	}
	return what
}

// serverAgentToken is acl.tokens.agent in a server's consul.json.
func serverAgentToken(n *spec.RawNode) string {
	b := n.Files["consul.json"]
	if b == "" || n.Labels == nil || n.Labels.Role != "server" {
		return ""
	}
	var cfg struct {
		ACL struct {
			Tokens struct {
				Agent string `json:"agent"`
			} `json:"tokens"`
		} `json:"acl"`
	}
	if json.Unmarshal([]byte(b), &cfg) != nil {
		return ""
	}
	return cfg.ACL.Tokens.Agent
}

// tokenNotes lists the tokens text mentions, as YAML comments.
func tokenNotes(low *spec.File, text string) string {
	labels := TokenLabels(low)
	var lines []string
	for _, s := range slices.Sorted(maps.Keys(labels)) {
		if strings.Contains(text, s) {
			lines = append(lines, fmt.Sprintf("#   %s  %s", s, labels[s]))
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return "# ACL tokens above:\n" + strings.Join(lines, "\n") + "\n"
}

// ConsulEnv is what the consul CLI needs to talk to a node's agent from
// the host: its address, the management token and the lab CA.
func ConsulEnv(low *spec.File, node string) ([]string, error) {
	n := low.Nodes[node]
	if n == nil {
		return nil, fmt.Errorf("no node %s", node)
	}
	ips := NodeIPs(n)
	if len(ips) == 0 {
		return nil, fmt.Errorf("%s has no address", node)
	}
	cc := AgentCheck(low, node)
	var env []string
	if cc.CAFile != "" {
		ca := cc.CAFile
		if !filepath.IsAbs(ca) {
			ca = filepath.Join(supervisor.NodeDir(RunDir(low.Name), node), ca)
		}
		env = append(env, "CONSUL_HTTP_ADDR=https://"+ips[0]+":8501", "CONSUL_CACERT="+ca)
	} else {
		env = append(env, "CONSUL_HTTP_ADDR=http://"+ips[0]+":8500")
	}
	if cc.Token != "" {
		env = append(env, "CONSUL_HTTP_TOKEN="+cc.Token)
	}
	return env, nil
}
