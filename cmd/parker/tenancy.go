package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/hashicorp/consul/api"
	"github.com/spf13/cobra"

	"github.com/Ranjandas/parker/internal/expand"
)

// tenancyCmd runs on a dc's first server once its servers (and ACLs) are
// ready: it creates the admin partitions and namespaces the lab uses
// (Consul Enterprise), and with ACLs the identity tokens of its agents,
// services and gateways, then waits so the process stays up for status.
func tenancyCmd() *cobra.Command {
	return &cobra.Command{
		Use:    expand.Tenancy + " MANIFEST",
		Short:  "Create a datacenter's partitions, namespaces and tokens (run by parker up)",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			b, err := os.ReadFile(args[0])
			if err != nil {
				return err
			}
			var t expand.TenancySpec
			if err := json.Unmarshal(b, &t); err != nil {
				return fmt.Errorf("%s: %w", args[0], err)
			}
			// CONSUL_HTTP_ADDR, CONSUL_HTTP_TOKEN and CONSUL_CACERT come
			// from the environment.
			client, err := api.NewClient(api.DefaultConfig())
			if err != nil {
				return err
			}
			ctx, stop := signalContext()
			defer stop()
			if err := applyTenancy(ctx, consulTenancy{client}, t, 60*time.Second, os.Stderr); err != nil {
				return err
			}
			fmt.Println(expand.TenancyReadyLine)
			<-ctx.Done()
			return nil
		},
	}
}

// tenancyAPI is what applyTenancy needs from Consul.
type tenancyAPI interface {
	CreatePartition(ctx context.Context, name string) error
	CreateNamespace(partition, name string) error
	EnsurePolicy(p expand.IdentityPolicy) error
	EnsureToken(t expand.IdentityToken) error
}

type consulTenancy struct{ c *api.Client }

func (t consulTenancy) CreatePartition(ctx context.Context, name string) error {
	_, _, err := t.c.Partitions().Create(ctx, &api.Partition{Name: name, Description: "Parker lab"}, nil)
	return err
}

func (t consulTenancy) CreateNamespace(partition, name string) error {
	_, _, err := t.c.Namespaces().Create(&api.Namespace{Name: name, Partition: partition, Description: "Parker lab"}, &api.WriteOptions{Partition: partition})
	return err
}

func (t consulTenancy) EnsurePolicy(p expand.IdentityPolicy) error {
	return ensurePolicy(t.c.ACL(), p)
}

func (t consulTenancy) EnsureToken(tok expand.IdentityToken) error {
	return ensureIdentityToken(t.c.ACL(), tok)
}

func inPartition(p string) string {
	if p == "" {
		return ""
	}
	return " in partition " + p
}

// exists: creating something that's there (a rerun) is fine.
func exists(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "already exists")
}

// applyTenancy creates partitions, then namespaces, then policies and
// tokens, retrying while the cluster settles.
func applyTenancy(ctx context.Context, a tenancyAPI, t expand.TenancySpec, patience time.Duration, log io.Writer) error {
	type step struct {
		label string
		do    func() error
	}
	var steps []step
	for _, p := range t.Partitions {
		steps = append(steps, step{"partition " + p, func() error { return a.CreatePartition(ctx, p) }})
	}
	for _, n := range t.Namespaces {
		label := "namespace " + n.Name
		if n.Partition != "" {
			label += " in partition " + n.Partition
		}
		steps = append(steps, step{label, func() error { return a.CreateNamespace(n.Partition, n.Name) }})
	}
	for _, p := range t.Policies {
		steps = append(steps, step{"policy " + p.Name + inPartition(p.Partition), func() error { return a.EnsurePolicy(p) }})
	}
	for _, tok := range t.Tokens {
		steps = append(steps, step{"token " + strings.ToLower(tok.Description) + inPartition(tok.Partition), func() error { return a.EnsureToken(tok) }})
	}
	for _, s := range steps {
		deadline := time.Now().Add(patience)
		for {
			err := s.do()
			if err == nil || exists(err) {
				fmt.Fprintf(log, "created %s\n", s.label)
				break
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("%s: %w", s.label, err)
			}
			fmt.Fprintf(log, "%s: %v (retrying)\n", s.label, err)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Second):
			}
		}
	}
	return nil
}
