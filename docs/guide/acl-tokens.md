---
title: ACL tokens
description: Every token Parker creates when ACLs are on, where it is used, and its exact rules.
---

With `acl: true`, parker sets up ACLs the way Consul's docs do: deny by
default, and every agent, service and gateway gets its own token with
only what it needs. This page lists each token, where it's used and its
exact rules. `parker describe FILE NODE` names the tokens in a node's
configs, and [troubleshooting](troubleshooting.md) shows how to read
them in a running lab.

parker picks every token's SecretID when it expands the lab, so configs
are static. The `tenancy` process on each datacenter's `server-1` creates
the policies and tokens before any client starts, and its log lists them.
In the rules below, `<node>`, `<svc>` and `<ns>` are the node, service
and namespace names.

## At a glance

| Component | Token | Consul identity | Policies |
|---|---|---|---|
| Servers | `acl.tokens.agent` | none | `parker-agent` |
| Client agent | `acl.tokens.agent` | node identity `<node>` | none |
| Agent DNS (transparent-proxy nodes) | `acl.tokens.dns` | none | `parker-dns` |
| Service, its sidecar Envoy, its consul-dataplane | `token` in the service definition, `CONSUL_HTTP_TOKEN` | service identity `<svc>` | Enterprise: `parker-read-everywhere` |
| Service in a namespace other than `default` (Enterprise) | the same | none | `parker-service-<ns>-<svc>` |
| Mesh gateway | `CONSUL_HTTP_TOKEN` | service identity | `parker-mesh-gateway-<svc>` |
| Terminating gateway | `CONSUL_HTTP_TOKEN` | service identity | `parker-terminating-gateway-<svc>` |
| API gateway | `CONSUL_HTTP_TOKEN` | service identity | `parker-api-gateway-<svc>` |
| Ingress gateway | `CONSUL_HTTP_TOKEN` | service identity | Enterprise: `parker-ingress-gateway-<svc>` |
| Setup jobs, `parker exec`, the Consul UI link | `CONSUL_HTTP_TOKEN` | the management token | `global-management` |

Every token and policy is in its component's admin partition, in that
partition's `default` namespace. A token can only use policies from its
own namespace, and only a policy in `default` can grant rules for other
namespaces.

## Servers

The servers start before anything else exists, so `acl-setup` creates
their agent token, `parker-agent`. It's the one token the servers share:

```hcl
node_prefix "" { policy = "write" }
service_prefix "" { policy = "write" }
```

With namespaces (Consul Enterprise), they can also read nodes and write
services in every namespace:

```hcl
namespace_prefix "" {
  node_prefix "" { policy = "read" }
  service_prefix "" { policy = "write" }
}
```

In a secondary datacenter, the management token is also the
`replication` token.

## Client agents

Each client agent's token has a node identity for its node, in its
datacenter. Consul expands that to:

```hcl
node "<node>" { policy = "write" }
service_prefix "" { policy = "read" }
```

## DNS

On a node with transparent proxy, the node's DNS goes to its agent.
Under default deny, lookups need a token, which links `parker-dns`:

```hcl
node_prefix "" { policy = "read" }
service_prefix "" { policy = "read" }
query_prefix "" { policy = "read" }
```

With namespaces, it can also read in every namespace (see
`parker-read-everywhere` below).

## Services

A service, its sidecar Envoy and its consul-dataplane share one token.
It has a service identity for the service, which Consul expands to:

```hcl
service "<svc>" { policy = "write" }
service "<svc>-sidecar-proxy" { policy = "write" }
service_prefix "" { policy = "read" }
node_prefix "" { policy = "read" }
```

On Consul Enterprise the token also links `parker-read-everywhere`, so a
service can see its upstreams in any namespace of its partition.
Enterprise checks node read in the namespace of each instance it
returns:

```hcl
namespace_prefix "" {
  service_prefix "" { policy = "read" }
  node_prefix "" { policy = "read" }
}
```

### Services in a namespace other than default

A service identity applies in its token's namespace. A token there can't
link `parker-read-everywhere`, which has to live in `default`. So a
service in namespace `<ns>` gets a token in its partition's `default`
namespace with no identity. Its policy, `parker-service-<ns>-<svc>`,
grants the identity's rules inside a namespace block:

```hcl
namespace "<ns>" {
  service "<svc>" { policy = "write" }
  service "<svc>-sidecar-proxy" { policy = "write" }
  service_prefix "" { policy = "read" }
  node_prefix "" { policy = "read" }
}
namespace_prefix "" {
  service_prefix "" { policy = "read" }
  node_prefix "" { policy = "read" }
}
```

Gateways in a namespace (`namespace:` on a gateway) work the same way:
their policy is `parker-<kind>-gateway-<ns>-<svc>`, and their identity's
rules join the policy below inside `namespace "<ns>" { ... }`.

## Gateways

Each gateway's token has a service identity for the gateway's service,
plus a policy with the rules Consul documents for its kind. On Consul
Enterprise every gateway policy also gets `parker-read-everywhere`'s
rules. An ingress gateway has no extra rules, so it gets a policy only
on Enterprise.

### Mesh gateway

`parker-mesh-gateway-<svc>`:

```hcl
mesh = "write"
peering = "read"
service "<svc>" { policy = "write" }
service_prefix "" { policy = "read" }
node_prefix "" { policy = "read" }
agent_prefix "" { policy = "read" }
```

On Consul Enterprise, the default partition's mesh gateway also reads
every partition, so it can route failover to services in other local
partitions. Only a policy in the default partition can grant this:

```hcl
partition_prefix "" {
  peering = "read"
  namespace_prefix "" {
    service_prefix "" { policy = "read" }
    node_prefix "" { policy = "read" }
  }
}
```

### Terminating gateway

`parker-terminating-gateway-<svc>` lets the gateway write each service
it fronts (the `Services` of its `terminating-gateway` entry), as Consul
requires:

```hcl
service "<linked>" { policy = "write" }
```

A linked service in another namespace goes in that namespace's block
(Enterprise):

```hcl
namespace "<linked-ns>" {
  service "<linked>" { policy = "write" }
}
```

A wildcard (`Name: "*"`) becomes `service_prefix "" { policy = "write" }`.

### API gateway

`parker-api-gateway-<svc>`:

```hcl
mesh = "read"
```

### Ingress gateway

Just the service identity.

## Setup jobs

These lab jobs use the management token: `acl-setup`, `tenancy`, config
entries, peering, the dataplane health sync, and `parker exec`. They are
lab plumbing rather than what a lab tests. The token is also
`initial_management` on the primary datacenter's servers, so the Consul
UI's *Open* link signs in with it.

## Compared with consul-k8s

These rules follow Consul's docs and consul-k8s's `server-acl-init`,
with two differences:

- consul-k8s leaves write on a terminating gateway's linked services to
  the user. parker adds it from the gateway's entry.
- consul-k8s gives the anonymous token read on every service, for DNS
  and cross-datacenter calls. parker leaves the anonymous token alone:
  DNS has its own token, and parker's federation and peering labs pass
  without anonymous access.
