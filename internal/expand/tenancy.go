package expand

import (
	"maps"
	"slices"
	"strings"

	"github.com/Ranjandas/parker/internal/spec"
)

// Tenancy (Consul Enterprise): a one-shot process on a dc's first server
// creates its admin partitions, then the namespaces its services use, and
// with ACLs an agent token inside each partition, before any client agent
// joins.
const (
	Tenancy          = "tenancy"
	TenancyReadyLine = "parker: partitions and namespaces ready"
	TenancyManifest  = "tenancy.json"
)

// TenancySpec is what the tenancy process creates.
type TenancySpec struct {
	Partitions  []string       `json:"partitions,omitempty"`
	Namespaces  []NamespaceRef `json:"namespaces,omitempty"`
	AgentTokens []AgentToken   `json:"agent_tokens,omitempty"`
}

// NamespaceRef is a namespace in a partition ("" for the default).
type NamespaceRef struct {
	Partition string `json:"partition,omitempty"`
	Name      string `json:"name"`
}

// AgentToken is the client agents' token in a non-default partition.
type AgentToken struct {
	Partition string `json:"partition"`
	SecretID  string `json:"secret_id"`
}

func (t TenancySpec) empty() bool { return len(t.Partitions) == 0 && len(t.Namespaces) == 0 }

// partitionName is how Consul's config names a partition: "" for the
// default one.
func partitionName(p string) string {
	if p == spec.DefaultPartition {
		return ""
	}
	return p
}

// tenancyFor lists a datacenter's partitions (besides the default) and
// namespaces.
func tenancyFor(c *spec.Consul, dc *spec.Datacenter) TenancySpec {
	var t TenancySpec
	for _, p := range slices.Sorted(maps.Keys(dc.Partitions)) {
		if partitionName(p) != "" {
			t.Partitions = append(t.Partitions, p)
		}
	}
	for _, pn := range c.Namespaces(dc) {
		part, ns, _ := strings.Cut(pn, "/")
		t.Namespaces = append(t.Namespaces, NamespaceRef{Partition: part, Name: ns})
	}
	return t
}

// tenancyProcess runs `parker tenancy` once the servers (and ACLs) are
// ready.
func tenancyProcess(deps map[string]*spec.Dep, env map[string]string) *spec.Process {
	return &spec.Process{
		Run:       spec.Command{VarParker, Tenancy, VarNodeDir + "/" + TenancyManifest},
		Env:       env,
		DependsOn: deps,
		ReadyWhen: &spec.Ready{Log: TenancyReadyLine, Timeout: "60s"},
	}
}

// tenantEnv points the consul CLI (consul connect envoy) at a partition
// and namespace.
func tenantEnv(env map[string]string, partition, namespace string) map[string]string {
	if partition != "" {
		env["CONSUL_PARTITION"] = partition
	}
	if namespace != "" {
		env["CONSUL_NAMESPACE"] = namespace
	}
	return env
}

// nodeNamespaces are the non-default namespaces of a node's services, for
// the ns: selector.
func nodeNamespaces(c *spec.Consul, dc *spec.Datacenter, part *spec.Partition, services map[string]*spec.Service) []string {
	seen := map[string]bool{}
	for _, s := range services {
		if ns := spec.ServiceNamespace(c, dc, part, s); ns != "" && ns != spec.DefaultNamespace {
			seen[ns] = true
		}
	}
	if len(seen) == 0 {
		return nil
	}
	return slices.Sorted(maps.Keys(seen))
}

// hasEntries: the dc or one of its partitions has config_entries.
func hasEntries(dc *spec.Datacenter) bool {
	if len(dc.ConfigEntries) > 0 {
		return true
	}
	for _, p := range dc.Partitions {
		if p != nil && len(p.ConfigEntries) > 0 {
			return true
		}
	}
	return false
}

// aclSetupCmd creates the agent token; --enterprise grants it services in
// every namespace too.
func aclSetupCmd(agentToken string, enterprise bool) spec.Command {
	cmd := spec.Command{VarParker, ACLSetup, "--agent-token", agentToken}
	if enterprise {
		cmd = append(cmd, "--enterprise")
	}
	return cmd
}
