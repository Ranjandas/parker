package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"time"

	"github.com/hashicorp/consul/api"
	"github.com/spf13/cobra"

	"github.com/Ranjandas/parker/internal/expand"
)

// dataplaneHealthCmd runs on a dc's first server: it registers the dc's
// dataplane services and sidecar proxies in the catalog, then probes each
// every second and writes its check status when it changes, as the
// consul-k8s endpoints controller does for pods.
func dataplaneHealthCmd() *cobra.Command {
	return &cobra.Command{
		Use:    expand.DataplaneHealth + " MANIFEST",
		Short:  "Register dataplane services and keep their health current (run by parker up)",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			b, err := os.ReadFile(args[0])
			if err != nil {
				return err
			}
			var entries []expand.DataplaneEntry
			if err := json.Unmarshal(b, &entries); err != nil {
				return fmt.Errorf("%s: %w", args[0], err)
			}
			client, err := api.NewClient(api.DefaultConfig())
			if err != nil {
				return err
			}
			ctx, stop := signalContext()
			defer stop()
			return keepHealth(ctx, client.Catalog(), entries, tcpProbe, time.Second, os.Stdout, os.Stderr)
		},
	}
}

type catalogAPI interface {
	Register(*api.CatalogRegistration, *api.WriteOptions) (*api.WriteMeta, error)
}

func tcpProbe(addr string) bool {
	c, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// registration is the catalog form of an entry with its check status.
func registration(e expand.DataplaneEntry, status string) *api.CatalogRegistration {
	s := e.Service
	svc := &api.AgentService{ID: s.ID, Service: s.Name, Port: s.Port, Address: s.Address, Namespace: s.Namespace, Partition: e.Partition}
	if s.Kind != "" {
		svc.Kind = api.ServiceKind(s.Kind)
	}
	if p := s.Proxy; p != nil {
		svc.Proxy = &api.AgentServiceConnectProxyConfig{DestinationServiceName: p.DestinationServiceName, DestinationServiceID: p.DestinationServiceID,
			LocalServiceAddress: p.LocalServiceAddress, LocalServicePort: p.LocalServicePort}
		for _, u := range p.Upstreams {
			svc.Proxy.Upstreams = append(svc.Proxy.Upstreams, api.Upstream{DestinationName: u.DestinationName, DestinationNamespace: u.DestinationNamespace,
				DestinationPartition: u.DestinationPartition, DestinationPeer: u.DestinationPeer, Datacenter: u.Datacenter, LocalBindPort: u.LocalBindPort})
		}
	}
	return &api.CatalogRegistration{
		Node: e.Node, Address: e.Address, Datacenter: e.Datacenter, Partition: e.Partition, NodeMeta: e.NodeMeta,
		Service: svc,
		Check: &api.AgentCheck{Node: e.Node, CheckID: "parker:" + s.ID, Name: "parker probe " + s.Name, Status: status,
			ServiceID: s.ID, ServiceName: s.Name, Namespace: s.Namespace, Partition: e.Partition,
			Output: "probed by parker dataplane-health"},
		SkipNodeUpdate: false,
	}
}

// keepHealth registers every entry, says so once all are in, then rewrites
// an entry whenever its probe result changes. Registration errors are
// retried: the cluster may still be settling.
func keepHealth(ctx context.Context, cat catalogAPI, entries []expand.DataplaneEntry, probe func(string) bool, every time.Duration, out, log io.Writer) error {
	last := make([]string, len(entries))
	ready := false
	for {
		done := true
		for i, e := range entries {
			status := api.HealthPassing
			if e.Probe != "" && !probe(e.Probe) {
				status = api.HealthCritical
			}
			if status == last[i] {
				continue
			}
			if _, err := cat.Register(registration(e, status), &api.WriteOptions{Partition: e.Partition, Namespace: e.Service.Namespace}); err != nil {
				fmt.Fprintf(log, "register %s on %s: %v (retrying)\n", e.Service.ID, e.Node, err)
				done = false
				continue
			}
			if ready {
				fmt.Fprintf(log, "%s on %s is %s\n", e.Service.ID, e.Node, status)
			}
			last[i] = status
		}
		if done && !ready {
			ready = true
			fmt.Fprintln(out, expand.DataplaneHealthReadyLine)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(every):
		}
	}
}
