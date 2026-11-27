---
title: Vault clusters
description: Run Vault clusters next to Consul or on their own, with integrated storage, init and unseal done for you.
---

A `vault:` section runs Vault clusters. Each cluster gets its own network,
and its servers run on integrated storage (raft). Parker initialises the
cluster, unseals every server and makes a root token, so the lab is ready
to use as soon as `parker up` returns. A lab can have only Vault, only
Consul, or both.

```yaml
parker: v1alpha1
name: vault
versions: { vault: 1.21.1 }
vault:
  clusters:
    east:
      servers: 3
```

```console
$ parker up vault.yaml
...
lab "vault" is up in 7.9s
  east-vault-1             10.64.0.10     vault, vault-init
  east-vault-2             10.64.0.11     vault
  east-vault-3             10.64.0.12     vault
$ parker exec vault east/vault-1 -- vault operator raft list-peers
Node       Address            State       Voter
----       -------            -----       -----
vault-1    10.64.0.10:8201    leader      true
vault-2    10.64.0.11:8201    follower    true
vault-3    10.64.0.12:8201    follower    true
```

`parker exec` on a Vault server sets `VAULT_ADDR` and `VAULT_TOKEN` (the
cluster's root token), so the `vault` CLI works straight away. The
servers' nodes are named `<cluster>-vault-<n>`.

## Clusters

`clusters:` is keyed by the cluster's name. Cluster names share one name
space with Consul datacenter names, so a lab can't have a datacenter and a
cluster both called `east`. That keeps [selectors](selectors.md) short:
`east`, `east/vault-2`.

| Key | Default | Meaning |
|---|---|---|
| `servers` | `1` | The number of Vault servers. |
| `storage` | `raft` | `raft` is integrated storage (Vault 1.4 or later). `file` is one server with no HA, for a plain single-server repro. `{consul: DC}` keeps the data in a Consul datacenter of the lab; see [Consul storage](#consul-storage). |
| `dev` | off | Run one server in dev mode: in memory, unsealed, no init. Its data is gone when it stops. |
| `ui` | `true` | Serve the Vault web UI, linked from the [web UI](web-ui.md#open-links). |
| `tls` | off | Serve the API over HTTPS, with a certificate from the lab's CA. See [TLS](#tls). |
| `log_level` | `info` | `trace`, `debug`, `info`, `warn` or `error`. |
| `config` | | Merged into every server's configuration as is, in Vault's JSON form. |
| `subnet` | `10.<64+n>.0.0/24`, or a free subnet when that one is in use | The cluster's LAN. An explicit subnet never moves. |
| `meta` | | Metadata for the cluster's nodes, matched by [`meta:` selectors](selectors.md#every-selector). |

`defaults:` takes `storage`, `ui`, `tls`, `log_level` and `config` for every
cluster; a cluster's own settings win.

```yaml
parker: v1alpha1
versions: { vault: 1.21.1 }
vault:
  defaults:
    log_level: debug
    config: { default_lease_ttl: 1h }
  clusters:
    east: { servers: 3, meta: { region: us-east } }
    scratch: { dev: true }
```

## Initialisation and unsealing

On `parker up`, the cluster's first server runs `vault-init`. It
initialises the cluster with one unseal key, unseals every server, and
creates the lab's root token, the one `parker exec` hands you. The unseal
key stays in the lab's run directory.

[`parker up LAB`](lifecycle.md#start-a-stopped-lab-again) on a stopped lab
resumes it: the servers start sealed on their kept data, and `vault-init`
unseals them again.

A server that you [stop and start](faults.md) or kill comes back sealed,
as a real one does. That's often the point of the repro. `parker exec`
on a Vault server also sets `PARKER_VAULT_UNSEAL_KEY`, so unsealing it by
hand is one command:

```console
$ parker exec vault east/vault-2 -- sh -c 'vault operator unseal "$PARKER_VAULT_UNSEAL_KEY"'
```

## Status

`parker status`, the [TUI](tui.md) and the [web UI](web-ui.md) show
each server's seal and HA state, and the cluster's raft voters:

```console
$ parker status vault
lab vault: supervised by parker daemon; run dir /run/parker/vault
NODE          IP          PROCESS     PID    STATE  RESTARTS  ROLE
east-vault-1  10.64.0.10  vault       14910  ready  0         active (raft 3 voters)
east-vault-1  10.64.0.10  vault-init  14915  ready  0         -
east-vault-2  10.64.0.11  vault       14911  ready  0         standby (raft 3 voters)
east-vault-3  10.64.0.12  vault       14964  ready  1         sealed
```

Here `east-vault-3` was sealed by a scenario's `vault: { seal: ... }`
step, which restarted it. A server is `active`, `standby`,
`perf-standby` (Enterprise), `sealed` or `uninitialized`.

## TLS

`tls: true` serves each server's API over HTTPS. Parker signs every
server's certificate with the lab's CA, which is the same CA that
Consul's [TLS](acl-and-tls.md#tls) uses when both are on. The servers
join each other over HTTPS, and `parker exec` sets `VAULT_ADDR` to
`https://127.0.0.1:8200` and `VAULT_CACERT` to the CA, so the CLI works
as before.

```yaml
parker: v1alpha1
name: vault-tls
versions: { vault: 1.21.1 }
vault:
  clusters:
    east: { servers: 3, tls: true }
```

```console
$ parker exec vault-tls east/vault-1 -- sh -c 'echo $VAULT_ADDR; vault status -format=json | grep sealed'
https://127.0.0.1:8200
  "sealed": false,
```

A certificate names the server's IP, `localhost`, its short and full node
names (`vault-1`, `east-vault-1`) and its `parker proxy` names. A dev
server (`dev: true`) has no TLS.

## Seal, unseal and step down

Scenario steps act on Vault servers through [selectors](selectors.md):
`east/active` is the cluster's active server at that moment, and
`east/standbys` the others.

| Step | Does |
|---|---|
| `vault: { seal: SEL }` | Seals the servers. Vault seals only an active server through its API, so a standby is restarted instead, as Vault suggests, and comes back sealed. |
| `vault: { unseal: SEL }` | Unseals them with the cluster's key. An unsealed server is left as is. |
| `vault: { step_down: SEL }` | The active server among them gives up being active, and a standby takes over. Standbys are left as they are. |

`assert: { vault: ... }` checks them: `sealed`, `active` and
`raft_peers`, described under [assert](scenarios.md#assert).

```yaml
parker: v1alpha1
name: vault-seal
versions: { vault: 1.21.1 }
vault:
  clusters:
    east: { servers: 3 }
scenario:
  steps:
    - wait: east ready
    # New servers join raft as non-voters; wait until all three vote.
    - assert: { vault: { on: east/active, raft_peers: ">=3" }, within: 60s }
    - vault: { step_down: east/active }
    - vault: { seal: east/vault-2 }
    - assert: { vault: { on: east/vault-2, sealed: true }, within: 30s }
    - assert: { vault: { on: east/active, sealed: false }, within: 30s }
    - vault: { unseal: east/vault-2 }
    - assert: { vault: { on: east, sealed: false, raft_peers: ">=3" }, within: 60s }
```

## Consul storage

`storage: {consul: DC}` keeps the cluster's data in the lab's Consul
datacenter `DC`, the way Vault ran before integrated storage. Each Vault
server also runs a Consul client agent of `DC`, joined to its LAN, and
Vault talks to that local agent. High availability comes from Consul
locks, so a cluster can have several servers.

```yaml
parker: v1alpha1
name: vault-consul
versions: { consul: 1.22.7, vault: 1.3.10 }
consul:
  datacenters:
    dc1: { servers: 3 }
vault:
  clusters:
    east:
      servers: 2
      storage: { consul: dc1 }
```

```console
$ parker exec vault-consul dc1/server-1 -- consul members
Node          Address          Status  Type    Build   Protocol  DC   Partition  Segment
server-1      10.1.0.10:8301   alive   server  1.22.7  2         dc1  default    <all>
server-2      10.1.0.11:8301   alive   server  1.22.7  2         dc1  default    <all>
server-3      10.1.0.12:8301   alive   server  1.22.7  2         dc1  default    <all>
east-vault-1  10.1.0.100:8301  alive   client  1.22.7  2         dc1  default    <default>
east-vault-2  10.1.0.101:8301  alive   client  1.22.7  2         dc1  default    <default>
$ parker exec vault-consul dc1/server-1 -- consul kv get -keys vault-east/
vault-east/core/
vault-east/logical/
vault-east/sys/
```

- The data lives under the KV path `vault-<cluster>/`.
- When the datacenter has [ACLs or TLS](acl-and-tls.md), Vault uses the
  agent's token, and HTTPS with the lab CA.
- `parker exec` on a Vault server sets both the Vault and the Consul
  variables, so `vault` and `consul` both work there.
- The servers stay in their cluster for [selectors](selectors.md)
  (`east/vault-1`); `dc1` doesn't match them.

Faults on the datacenter reach Vault: stop its servers, or partition a
Vault server from them, and Vault loses its storage and its lock.

## Vault as the Connect CA

`connect: { ca: { vault: CLUSTER } }` on a Consul datacenter makes the
Vault cluster the service mesh's certificate authority, through Vault's
PKI secrets engine. The datacenter's servers join the cluster's network
(from `.200` up) and start once the cluster is unsealed. Consul mounts
`connect-root/` and `connect-<dc>-intermediate/` in Vault and signs every
sidecar's certificate there.

```yaml
parker: v1alpha1
name: vault-connect-ca
versions: { consul: 1.22.7, envoy: 1.35.12, vault: 1.21.1 }
vault:
  clusters:
    east: { servers: 3 }
consul:
  datacenters:
    dc1:
      servers: 3
      connect: { ca: { vault: east } }
      service_template: { run: fake-service, connect: sidecar }
      nodes:
        web:
          service:
            port: 8080
            upstreams: { api: 9091 }
        api:
          service: { port: 9090 }
```

```console
$ parker exec vault-connect-ca dc1/server-1 -- consul connect ca get-config
{
	"Provider": "vault",
	"Config": {
		"Address": "http://10.64.0.10:8200",
...
$ parker exec vault-connect-ca east/vault-1 -- vault secrets list
Path                         Type         Accessor              Description
----                         ----         --------              -----------
connect-dc1-intermediate/    pki          pki_dcd832e6          intermediate CA backend for Consul Connect
connect-root/                pki          pki_008e0664          root CA backend for Consul Connect
cubbyhole/                   cubbyhole    cubbyhole_23339f83    per-token private secret storage
identity/                    identity     identity_5f9c13ae     identity store
sys/                         system       system_34d9b279       system endpoints used for control, policy and debugging
```

- Consul uses the cluster's root token, and HTTPS with the lab CA when
  the cluster has `tls: true`.
- With the datacenter's [TLS](acl-and-tls.md#tls) on, Parker puts the
  lab's CA into `connect-root/` before Consul starts, so client agents'
  auto-encrypt certificates, which the Connect CA signs, chain to the same
  CA as everything else. That needs a cluster without `dev: true`.
- Vault faults reach the mesh: seal or stop the cluster and Consul can't
  sign new certificates or rotate them.

## Old Vault releases

Integrated storage needs Vault 1.4 or later, and `parker validate` refuses
`storage: raft` on anything older. For HA on an old release, use
[Consul storage](#consul-storage). For one server, `storage: file` is
enough:

```yaml
parker: v1alpha1
versions: { vault: 1.3.10 }
vault:
  clusters:
    old: { storage: file }
```

## Limits

- Replication, Enterprise builds, and other links to Consul (service registration, the Consul secrets engine) are planned.
