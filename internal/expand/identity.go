package expand

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/Ranjandas/parker/internal/spec"
)

// With ACLs, every client agent, service and gateway gets its own token,
// the way Consul's docs set them up (#136): a node identity for an agent,
// a service identity for a service and its sidecar, and a service
// identity plus the documented rules for a gateway. Setup jobs (acl-setup,
// tenancy, config entries, peering) keep the management token: they are
// lab plumbing, not what a lab tests. Servers keep the parker-agent token
// acl-setup creates, since they start before anything else can be made.
//
// expand picks every SecretID, so configs stay static; the tenancy
// process creates the policies and tokens before any client starts.

// IdentityToken is a token the tenancy process creates.
type IdentityToken struct {
	SecretID    string `json:"secret_id"`
	Description string `json:"description"`
	Partition   string `json:"partition,omitempty"` // "" for the default
	Namespace   string `json:"namespace,omitempty"` // "" for the default
	// Node and Datacenter make a node identity.
	Node       string `json:"node,omitempty"`
	Datacenter string `json:"datacenter,omitempty"`
	// Service makes a service identity.
	Service string `json:"service,omitempty"`
	// Policies (by name, from TenancySpec.Policies in the token's
	// partition) add to the identity.
	Policies []string `json:"policies,omitempty"`
	// Scoped: the service is in a namespace other than default, and the
	// token lives in its partition's default namespace with no service
	// identity; its policy grants the identity's rules inside a namespace
	// block. Consul Enterprise links a token only to policies of its own
	// namespace, and only default-namespace policies may name other
	// namespaces, which partition-wide rules (mesh, peering, read
	// everywhere) need.
	Scoped bool `json:"scoped,omitempty"`
}

// IdentityPolicy is a policy the tenancy process creates, in its
// partition's default namespace.
type IdentityPolicy struct {
	Name      string `json:"name"`
	Partition string `json:"partition,omitempty"`
	Rules     string `json:"rules"`
}

// identities collects one datacenter's tokens and policies.
type identities struct {
	dc       string
	ent      bool // rules may have namespace blocks (Consul Enterprise)
	tokens   []IdentityToken
	policies []IdentityPolicy
	byKey    map[string]string // what a token is for -> its SecretID
}

func newIdentities(dc string, ent bool) *identities {
	return &identities{dc: dc, ent: ent, byKey: map[string]string{}}
}

// token returns the SecretID for key, creating the token with mk the
// first time.
func (id *identities) token(key string, mk func() IdentityToken) (string, error) {
	if s, ok := id.byKey[key]; ok {
		return s, nil
	}
	s, err := uuid()
	if err != nil {
		return "", err
	}
	t := mk()
	t.SecretID = s
	id.tokens = append(id.tokens, t)
	id.byKey[key] = s
	return s, nil
}

func (id *identities) policy(partition, name, rules string) string {
	for _, p := range id.policies {
		if p.Partition == partition && p.Name == name {
			return name
		}
	}
	id.policies = append(id.policies, IdentityPolicy{Name: name, Partition: partition, Rules: rules})
	return name
}

// readEverywhere is service and node read in every namespace (Consul
// Enterprise checks node read in the namespace of each instance it
// returns, #135), so callers and gateways see their upstreams anywhere in
// the partition.
const readEverywhere = `namespace_prefix "" {
  service_prefix "" { policy = "read" }
  node_prefix "" { policy = "read" }
}
`

func (id *identities) entRead(rules string) string {
	if id.ent {
		return rules + readEverywhere
	}
	return rules
}

// node is a client agent's token: a node identity.
func (id *identities) node(partition, node string) (string, error) {
	return id.token("node/"+partition+"/"+node, func() IdentityToken {
		return IdentityToken{Description: "Parker agent " + node, Partition: partition, Node: node, Datacenter: id.dc}
	})
}

// dns is the token an agent answers DNS with (transparent proxy nodes).
func (id *identities) dns(partition string) (string, error) {
	return id.token("dns/"+partition, func() IdentityToken {
		p := id.policy(partition, "parker-dns", id.entRead(`node_prefix "" { policy = "read" }
service_prefix "" { policy = "read" }
query_prefix "" { policy = "read" }
`))
		return IdentityToken{Description: "Parker DNS", Partition: partition, Policies: []string{p}}
	})
}

// identityRules are what Consul's service identity grants (service
// identity template), inside a namespace block.
func identityRules(namespace, svc string) string {
	return fmt.Sprintf("namespace %q {\n%s}\n", namespace, identityBody(svc, "  "))
}

func identityBody(svc, indent string) string {
	return fmt.Sprintf(`%[1]sservice %[2]q { policy = "write" }
%[1]sservice %[3]q { policy = "write" }
%[1]sservice_prefix "" { policy = "read" }
%[1]snode_prefix "" { policy = "read" }
`, indent, svc, svc+"-sidecar-proxy")
}

// service is a service's token: a service identity, which covers its
// sidecar too.
func (id *identities) service(partition, namespace, svc string) (string, error) {
	return id.token("service/"+partition+"/"+namespace+"/"+svc, func() IdentityToken {
		t := IdentityToken{Description: "Parker service " + svc, Partition: partition, Namespace: namespace, Service: svc}
		switch {
		case namespace != "" && namespace != spec.DefaultNamespace:
			t.Scoped = true
			t.Policies = []string{id.policy(partition, "parker-service-"+namespace+"-"+svc, identityRules(namespace, svc)+readEverywhere)}
		case id.ent:
			t.Policies = []string{id.policy(partition, "parker-read-everywhere", readEverywhere)}
		}
		return t
	})
}

// gateway is a gateway's token: a service identity for the gateway and
// the rules Consul documents for its kind. linked are a terminating
// gateway's services (from its entry).
func (id *identities) gateway(g gatewayNode, partition, namespace string, linked []linkedService) (string, error) {
	svc := g.service()
	return id.token("gateway/"+partition+"/"+namespace+"/"+svc, func() IdentityToken {
		scoped := namespace != "" && namespace != spec.DefaultNamespace
		var rules string
		switch g.kind {
		case "mesh":
			rules = fmt.Sprintf(`mesh = "write"
peering = "read"
service %q { policy = "write" }
service_prefix "" { policy = "read" }
node_prefix "" { policy = "read" }
agent_prefix "" { policy = "read" }
`, svc)
		case "terminating":
			own := ""
			if scoped {
				own = svc // its identity's rules join its namespace's block
			}
			rules = linkedRules(linked, namespace, id.ent, own)
		case "api":
			rules = `mesh = "read"
`
		}
		t := IdentityToken{Description: "Parker " + g.kind + " gateway " + svc, Partition: partition, Namespace: namespace, Service: svc}
		name := "parker-" + g.kind + "-gateway-" + svc
		if scoped {
			t.Scoped = true
			if g.kind != "terminating" {
				rules += identityRules(namespace, svc)
			}
			name = "parker-" + g.kind + "-gateway-" + namespace + "-" + svc
		}
		if rules = id.entRead(rules); rules != "" {
			// Partition-wide rules (mesh, peering, other namespaces) only
			// go in a policy of the partition's default namespace.
			t.Policies = []string{id.policy(partition, name, rules)}
		}
		return t
	})
}

// linkedService is a service a terminating gateway fronts.
type linkedService struct{ name, namespace string }

// linkedServices reads a terminating-gateway entry's Services.
func linkedServices(e spec.ConfigEntry) []linkedService {
	l, _ := e["Services"].([]any)
	var out []linkedService
	for _, s := range l {
		m, ok := s.(map[string]any)
		if !ok {
			continue
		}
		name, _ := m["Name"].(string)
		ns, _ := m["Namespace"].(string)
		if name != "" {
			out = append(out, linkedService{name, ns})
		}
	}
	return out
}

// linkedRules let a terminating gateway write (register) the services it
// fronts, which Consul requires. A service with no namespace is in the
// gateway's (gwNS, "" for the default). own, when set, is the gateway's
// service, whose identity rules go in gwNS's block (one block per
// namespace).
func linkedRules(linked []linkedService, gwNS string, ent bool, own string) string {
	byNS := map[string][]string{}
	if own != "" {
		byNS[gwNS] = nil
	}
	for _, s := range linked {
		ns := s.namespace
		if ns == "" {
			ns = gwNS
		}
		if !ent || ns == spec.DefaultNamespace {
			ns = ""
		}
		byNS[ns] = append(byNS[ns], s.name)
	}
	var b strings.Builder
	for _, ns := range slices.Sorted(maps.Keys(byNS)) {
		indent := ""
		if ns != "" {
			fmt.Fprintf(&b, "namespace %q {\n", ns)
			indent = "  "
		}
		for _, name := range byNS[ns] {
			if name == "*" {
				fmt.Fprintf(&b, "%sservice_prefix \"\" { policy = \"write\" }\n", indent)
			} else {
				fmt.Fprintf(&b, "%sservice %q { policy = \"write\" }\n", indent, name)
			}
		}
		if own != "" && ns == gwNS {
			b.WriteString(identityBody(own, indent))
		}
		if ns != "" {
			b.WriteString("}\n")
		}
	}
	return b.String()
}
