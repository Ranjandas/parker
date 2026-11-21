---
title: Consul Enterprise
description: Run Consul Enterprise builds with a license, and use admin partitions and namespaces.
---

Add `+ent` to the Consul version to run the Enterprise build:

```yaml
parker: v1alpha1
versions: { consul: 1.22.7+ent }
license: { file: ~/consul.hclic }
consul:
  datacenters:
    dc1:
      servers: 3
```

## The license

Enterprise needs a license. Parker looks for it in this order:

1. `license: { file: PATH }` in the lab file: absolute, `~/...`, or
   relative to the lab file;
2. the file at `CONSUL_LICENSE_PATH`;
3. `CONSUL_LICENSE`, the license itself.

```console
$ export CONSUL_LICENSE_PATH=~/consul.hclic
$ parker up enterprise.yaml
```

`parker up` stops before downloading or starting anything when a lab
needs a license and has none, and says which pin needs it.

The license only reaches the agents. It's read where you run `parker`,
sent to the daemon with `up` (over TLS when the daemon is
[remote](remote.md)), and written to a private (mode 0600) file in the
lab's run directory, which `parker down` removes. It never appears in
`parker expand`, `describe`, the saved topology, or logs.

## Admin partitions

`partitions:` splits a datacenter's nodes and gateways into admin
partitions. Nodes in the default partition go under `partitions.default`:

```yaml
parker: v1alpha1
versions: { consul: 1.22.7+ent, envoy: 1.35.12 }
consul:
  defaults: { acl: true }
  datacenters:
    dc1:
      servers: 3
      partitions:
        default:
          nodes:
            frontend:
              service:
                port: 8080
                run: fake-service
                connect: sidecar
                upstreams: { billing: { port: 9091, partition: team-a, namespace: payments } }
        team-a:
          service_defaults: { run: fake-service, connect: sidecar, namespace: payments }
          gateways: { mesh: true }
          nodes:
            billing: { count: 2, service: { port: 9000 } }
          config_entries:
            - Kind: exported-services
              Name: team-a
              Services:
                - { Name: billing, Namespace: payments, Consumers: [{ Partition: default }] }
```

Each partition takes:

| Key | Meaning |
|---|---|
| `nodes` | The partition's nodes, as under a datacenter. |
| `gateways` | Its gateways. Mesh gateways are per partition. |
| `client` | [Agent configuration](datacenters.md#agent-configuration) for its client agents. |
| `service_defaults` | Merged into its services. |
| `config_entries` | [Config entries](config-entries.md) written into the partition, after the datacenter's. |

Parker creates the partitions as soon as the servers have a leader,
joins each client agent to its partition, and with ACLs gives every
agent, service and gateway its token in its partition.

### Node names in partitions

Partitions can reuse a node name, as Consul allows. Outside the default
partition, nodes are named `<dc>-<partition>-<name>`, so `billing-1`
above is the node `dc1-team-a-billing-1` (and `billing-1` to Consul).
Selectors name it `dc1/team-a/billing-1`, or `dc1/team-a/billing` for
every copy. See [selectors](selectors.md).

## Namespaces

`namespace:` on a service registers it, its sidecar and its checks in
that namespace. Parker creates the namespace. Set it on one service, or
for many in `service_defaults`:

```yaml
nodes:
  billing:
    service: { port: 9000, run: fake-service, namespace: payments }
```

Upstreams to a service in another namespace say so:
`upstreams: { billing: { port: 9091, namespace: payments } }`.
Terminating, ingress and API gateways take `namespace:` too (see
[gateways](gateways.md#gateways-in-a-namespace)).

## Limits

- Partitions and namespaces need an Enterprise version.
- Partitions can't be combined with WAN federation yet. Partitions in
  different datacenters can [peer](multiple-datacenters.md#peering-admin-partitions).

## Examples

- [partitions.yaml](https://github.com/Ranjandas/parker/blob/main/examples/partitions.yaml):
  frontend calls team-a's billing and fails over to team-b's when
  team-a's is stopped.
- [peering-partitions.yaml](https://github.com/Ranjandas/parker/blob/main/examples/peering-partitions.yaml):
  partitions in two datacenters, peered.
- [terminating-failover-partitions.yaml](https://github.com/Ranjandas/parker/blob/main/examples/terminating-failover-partitions.yaml):
  terminating gateway failover between partitions.
