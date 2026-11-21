package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/hashicorp/consul/api"
	"github.com/spf13/cobra"

	"github.com/Ranjandas/parker/internal/expand"
)

// aclAgentRules are the servers' agent token's. Client agents, services
// and gateways get identity tokens (expand/identity.go, created by
// tenancy).
const aclAgentRules = `node_prefix "" { policy = "write" }
service_prefix "" { policy = "write" }
`

// aclNamespaceRules give every namespace (Consul Enterprise) its
// services and node read. Consul checks node:read in the namespace of the
// service it returns, so without the node rule here a sidecar in a
// non-default namespace can't see the nodes of its upstreams' instances,
// e.g. a terminating gateway's.
const aclNamespaceRules = `namespace_prefix "" {
  node_prefix "" { policy = "read" }
  service_prefix "" { policy = "write" }
}
`

// aclAgentRulesEnt add services in every namespace (Consul Enterprise),
// for labs with namespaces.
const aclAgentRulesEnt = aclAgentRules + aclNamespaceRules

// aclSetupCmd runs inside the ACL primary's first server once its leader
// is up: it creates the agent policy and the agent token (with the
// SecretID expand generated, which every agent already has in its config),
// then waits so the process stays up for status.
func aclSetupCmd() *cobra.Command {
	var addr, agentToken string
	var enterprise bool
	c := &cobra.Command{
		Use:    expand.ACLSetup,
		Short:  "Create the lab's ACL agent token (run by parker up)",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// CONSUL_HTTP_TOKEN (the management token), CONSUL_HTTP_ADDR
			// and CONSUL_CACERT come from the environment.
			cfg := api.DefaultConfig()
			if addr != "" {
				cfg.Address = addr
			}
			client, err := api.NewClient(cfg)
			if err != nil {
				return err
			}
			ctx, stop := signalContext()
			defer stop()
			deadline := time.Now().Add(60 * time.Second)
			for {
				rules := aclAgentRules
				if enterprise {
					rules = aclAgentRulesEnt
				}
				err = ensureAgentToken(client.ACL(), agentToken, rules, "")
				if err == nil {
					break
				}
				if time.Now().After(deadline) {
					return fmt.Errorf("acl setup: %w", err)
				}
				fmt.Fprintf(os.Stderr, "acl setup: %v (retrying)\n", err)
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(time.Second):
				}
			}
			fmt.Println(expand.ACLReadyLine)
			<-ctx.Done()
			return nil
		},
	}
	c.Flags().StringVar(&addr, "addr", "", "Consul HTTP address (default $CONSUL_HTTP_ADDR)")
	c.Flags().StringVar(&agentToken, "agent-token", "", "SecretID of the agent token to create")
	c.Flags().BoolVar(&enterprise, "enterprise", false, "let the token register services in every namespace (Consul Enterprise)")
	return c
}

type aclAPI interface {
	PolicyReadByName(string, *api.QueryOptions) (*api.ACLPolicy, *api.QueryMeta, error)
	PolicyCreate(*api.ACLPolicy, *api.WriteOptions) (*api.ACLPolicy, *api.WriteMeta, error)
	TokenReadSelf(*api.QueryOptions) (*api.ACLToken, *api.QueryMeta, error)
	TokenCreate(*api.ACLToken, *api.WriteOptions) (*api.ACLToken, *api.WriteMeta, error)
}

// ensureAgentToken is idempotent: it creates what is missing.
// partition is "" for the default one.
func ensureAgentToken(a aclAPI, secret, rules, partition string) error {
	const policy = "parker-agent"
	p, _, err := a.PolicyReadByName(policy, &api.QueryOptions{Partition: partition})
	if err != nil && !strings.Contains(err.Error(), "404") {
		return err
	}
	if p == nil {
		if _, _, err := a.PolicyCreate(&api.ACLPolicy{Name: policy, Description: "Parker lab agents", Rules: rules, Partition: partition}, &api.WriteOptions{Partition: partition}); err != nil {
			return fmt.Errorf("create policy: %w", err)
		}
	}
	if t, _, err := a.TokenReadSelf(&api.QueryOptions{Token: secret, Partition: partition}); err == nil && t != nil {
		return nil
	}
	_, _, err = a.TokenCreate(&api.ACLToken{
		SecretID:    secret,
		Description: "Parker agent token",
		Policies:    []*api.ACLTokenPolicyLink{{Name: policy}},
		Partition:   partition,
	}, &api.WriteOptions{Partition: partition})
	if err != nil {
		return fmt.Errorf("create agent token: %w", err)
	}
	return nil
}

type identityACL interface {
	PolicyReadByName(string, *api.QueryOptions) (*api.ACLPolicy, *api.QueryMeta, error)
	PolicyCreate(*api.ACLPolicy, *api.WriteOptions) (*api.ACLPolicy, *api.WriteMeta, error)
	TokenReadSelf(*api.QueryOptions) (*api.ACLToken, *api.QueryMeta, error)
	TokenCreate(*api.ACLToken, *api.WriteOptions) (*api.ACLToken, *api.WriteMeta, error)
}

// ensurePolicy creates a policy in its partition's default namespace
// unless it is there.
func ensurePolicy(a identityACL, p expand.IdentityPolicy) error {
	got, _, err := a.PolicyReadByName(p.Name, &api.QueryOptions{Partition: p.Partition})
	if err != nil && !strings.Contains(err.Error(), "404") {
		return err
	}
	if got != nil {
		return nil
	}
	_, _, err = a.PolicyCreate(&api.ACLPolicy{Name: p.Name, Description: "Parker lab", Rules: p.Rules, Partition: p.Partition},
		&api.WriteOptions{Partition: p.Partition})
	return err
}

// ensureIdentityToken creates a token with the SecretID expand picked
// unless it is there (a re-up keeps Consul's data).
func ensureIdentityToken(a identityACL, t expand.IdentityToken) error {
	q := &api.QueryOptions{Token: t.SecretID, Partition: t.Partition, Namespace: t.Namespace}
	if got, _, err := a.TokenReadSelf(q); err == nil && got != nil {
		return nil
	}
	tok := &api.ACLToken{SecretID: t.SecretID, Description: t.Description, Partition: t.Partition, Namespace: t.Namespace}
	if t.Node != "" {
		tok.NodeIdentities = []*api.ACLNodeIdentity{{NodeName: t.Node, Datacenter: t.Datacenter}}
	}
	if t.Service != "" {
		tok.ServiceIdentities = []*api.ACLServiceIdentity{{ServiceName: t.Service}}
	}
	for _, name := range t.Policies {
		// The policies are in the partition's default namespace, which a
		// token in another namespace links by ID.
		p, _, err := a.PolicyReadByName(name, &api.QueryOptions{Partition: t.Partition})
		if err != nil {
			return fmt.Errorf("policy %s: %w", name, err)
		}
		if p == nil {
			return fmt.Errorf("policy %s: not found", name)
		}
		tok.Policies = append(tok.Policies, &api.ACLTokenPolicyLink{ID: p.ID})
	}
	_, _, err := a.TokenCreate(tok, &api.WriteOptions{Partition: t.Partition, Namespace: t.Namespace})
	return err
}
