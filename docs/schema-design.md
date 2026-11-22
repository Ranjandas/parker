# Parker topology file: schema design (draft v0.2)

Status: draft for discussion · Author: Claude for Ranjan · 2026-11-19

Parker reproduces Consul issues fast by wiring Linux network namespaces (plus mount/UTS/PID ns where needed) instead of containers, VMs or Kubernetes. The YAML file is the product's main surface: people write it, copy it into bug reports, and extend it. This doc proposes its shape.

**Changes in v0.2 (Ranjan's decisions, 2026-11-19)**
- Everything Consul lives under a `consul:` key.
- The Consul section is nested: `datacenter → partition → node → service`, replacing the flat top-level `services:` / `gateways:` maps.
- Templating is simple `${}` variables only (no loops or conditionals). Modules stay.
- Each Parker node is one network namespace with one Consul client agent. Dataplane (agentless) mode is reserved in the schema (`agent: dataplane`) but comes later.
- Section 9 is new: trade-offs of the nested shape and how the schema softens them.

Sections: 1 Principles · 2 The file at a glance · 3 Field reference · 4 Selectors and references · 5 Extension · 6 Validation and tooling · 7 Worked examples · 8 Open questions · 9 Nesting trade-offs

---

## 1. Design principles

1. **A 5-line file must boot a working cluster.** Everything beyond `consul version + datacenter + server count` has a sensible default.
2. **Speak Consul, not Linux.** The `consul:` section talks in datacenters, partitions, nodes and services, nested the way Consul itself scopes them. Namespaces, veths, bridges and IPs are derived.
3. **Every shorthand has a visible expansion.** The `consul:` section expands into the low-level layer (networks, raw nodes, processes). `parker expand file.yaml` prints the fully expanded, fully defaulted file, and that output is valid input.
4. **Maps keyed by name, not lists.** Named maps make overrides, overlays and error messages (`consul.datacenters.dc2.nodes.web`) easy. Lists are only used where order is the meaning (scenario steps).
5. **Consul config is passthrough, not re-modelled.** Anything not first-class goes in a `config:` block that is merged verbatim into the agent's JSON config. Same for service registrations, Envoy overrides and config entries.
6. **Inherit downward.** Settings declared higher in the tree (dc, partition) are defaults for everything below. This is what pays for the nesting.
7. **Declarative state, then a timeline.** Topology describes steady state; faults live in `scenario:`.
8. **Fail at validate time, with a line number.**
9. **Deterministic.** Same file gives the same names, IPs, ports, node IDs and keys.

---

## 2. The file at a glance

```yaml
parker: v1alpha1          # schema version (required)
name: my-repro            # defaults to file name

versions:                 # pinned binaries, fetched into a cache
  consul: 1.20.1
  envoy: 1.31.2           # default: matched to the Consul version

imports: []               # modules (section 5)
vars: {}                  # simple variables (section 5)

consul:                   # everything Consul; expands into the layers below
  defaults: {}            # inherited by every dc/partition/node/service
  federation: {}          # WAN federation
  peering: {}             # cluster peering
  datacenters:
    dc1:
      servers: 3
      partitions:         # optional; omit and use `nodes:` directly for the default partition
        default:
          nodes:
            web:
              services:
                web: { port: 8080 }

# --- low level (usually derived; declare to add or amend) ---
networks: {}              # bridges, subnets, impairments
nodes: {}                 # raw netns + processes not managed by Consul (toxiproxy, vault...)

scenario: {}              # faults, actions, waits, assertions
```

Only `parker`, `versions.consul` and one datacenter are required.

---

## 3. Field reference

### 3.1 Top level

| Field | Type | Default | Notes |
|---|---|---|---|
| `parker` | string | required | Schema version. `v1alpha1` until we commit. |
| `name` | string | file stem | Prefix for netns, bridges, run directory. Short (bridge names max 15 chars). |
| `versions.consul` | string | required | Semver, `+ent`, or `path:/abs/consul`. |
| `versions.envoy` | string | matched to Consul | Only fetched when something needs Envoy. |
| `versions.<other>` | string | | For user processes (e.g. `vault`). |

### 3.2 `consul.defaults`

Same shape as the inheritable parts of a datacenter (see 3.3): `agents`, `server`, `client`, `service_template`, `acl`, `tls`, `gossip_encryption`. Use it for "every agent everywhere logs at TRACE" or "every service uses tproxy".

### 3.3 `consul.datacenters.<dc>`

```yaml
datacenters:
  dc1:
    primary: true              # default: first dc listed
    subnet: 10.1.0.0/24        # LAN subnet; default 10.<index>.0.0/24
    acl: false                 # false | true (bootstrap, token printed) | object
    tls: false                 # false | true (auto CA) | object
    gossip_encryption: false   # false | true (generated key) | "<key>"
    ui: true                   # on first server, forwarded to localhost:85xx

    servers: 3                 # int, or { count, overrides } (below)

    agents:   { config: {}, log_level: DEBUG }   # all agents in this dc
    server:   { config: {} }                      # servers only
    client:   { config: {} }                      # client agents only
    service_template: {}       # inherited by every service in this dc (see 3.6)

    # Either nodes directly (default partition) ...
    nodes: {}
    # ... or partitions (Enterprise). Not both.
    partitions: {}

    config_entries: []         # applied after this dc's leader is ready
```

**Servers stay at the datacenter level.** In Consul, servers belong to a datacenter, not to a partition, so the schema mirrors that. Per-server overrides:

```yaml
servers:
  count: 3
  overrides:
    server-3:
      version: 1.19.4                       # mixed-version cluster
      config: { non_voting_server: true }
```

**`nodes:` directly under a dc means the default partition.** This keeps OSS files one level shallower and makes adding partitions later a mechanical move.

### 3.4 `consul.datacenters.<dc>.partitions.<partition>` (Enterprise)

```yaml
partitions:
  team-a:
    client: { config: {} }       # client agents in this partition
    service_template: {}
    nodes: {}
    gateways: {}                 # mesh gateways are per partition in Enterprise
    config_entries: []           # written into this partition
```

Parker creates the partition, sets each client agent's `partition`, and scopes config entries to it.

### 3.5 Nodes: `...nodes.<node>`

A node is one network namespace running one Consul client agent plus the services and processes on it. This matches the "one VM" mental model.

```yaml
nodes:
  web:
    count: 2                     # → web-1, web-2 (identical copies). Default 1 → "web"
    agent: client                # client (default) | dataplane (reserved, later) | none
    config: {}                   # this node's agent config (merged last)
    meta: { zone: a }            # node_meta
    services: {}                 # see 3.6
    processes: {}                # extra processes in this netns (tcpdump, load generator)
    networks: {}                 # extra attachments beyond the dc LAN
```

Shorthand for the very common one-service node: `service:` (singular) registers a service named after the node.

```yaml
nodes:
  api:
    count: 2
    service: { port: 9090 }      # same as services: { api: { port: 9090 } }
```

### 3.6 Services: `...nodes.<node>.services.<service>`

```yaml
services:
  web:
    port: 8080
    run: fake-service            # built-in | command string | process object
    env: { MESSAGE: "hello" }
    connect: tproxy              # none | sidecar | tproxy
    upstreams:
      api: 9091                  # local bind port (sidecar mode)
      db: { port: 9092, dc: dc2 }
      billing: { peer: dc1-dc2 }
    check: http                  # http | tcp | none | consul check object
    namespace: default           # Enterprise; a field, not a nesting level (see 9.5)
    register: {}                 # passthrough service definition (tags, meta, weights...)
```

- `run: fake-service` uses a pinned built-in echo server so mesh repros need no user app.
- `connect: tproxy` installs the redirect rules inside the node's netns and runs Envoy as the sidecar. Explicit upstreams become optional.
- At most one `tproxy` service per node, because the redirect rules are per network namespace. Validation enforces this.

**Inheritance for services** (later wins, deep merge):
`consul.defaults.service_template` → `dc.service_template` → `partition.service_template` → the service itself.
`service_template` is a Parker shortcut, unrelated to Consul's `service-defaults` config entry.

**Inheritance for agent config:**
`consul.defaults.agents` → `dc.agents` → `dc.server` or `dc.client` → `partition.client` → `node.config` / server override → overlays → `--set`.

### 3.7 Gateways

Gateways are nodes too, so they live where their scope is: under the dc (OSS) or the partition (Enterprise).

```yaml
dc1:
  gateways:
    mesh:                        # name of the gateway node
      kind: mesh                 # mesh | ingress | terminating | api
      count: 1
      wan: true                  # mesh only: attach to the WAN network
    ingress:
      kind: ingress
      listeners: []              # passthrough config entry listeners
```

Shorthand: `gateways: { mesh: true }` gives one mesh gateway with defaults.

### 3.8 `consul.federation` and `consul.peering`

```yaml
consul:
  federation:
    mode: wan                    # wan (serf WAN) | mesh-gateways
  peering:
    dc1-dc2:
      acceptor: dc2              # dc, or dc/partition
      dialer: dc1
      via_mesh_gateways: true
      exports: { dc2: [api] }    # sugar for the exported-services config entry
```

These stay at the `consul:` level because they connect datacenters rather than belonging to one.

### 3.9 Config entries

A list on the dc or partition, applied in order after the leader is ready, using Consul's own casing so entries paste straight from docs:

```yaml
config_entries:
  - Kind: service-resolver
    Name: api
    Failover: { "*": { Targets: [{ Peer: dc1-dc2 }] } }
  - file: ./entries/proxy-defaults.hcl
```

### 3.10 Low level: `networks`

```yaml
networks:
  dc1-lan: { impair: { delay: 2ms } }        # generated; declare to amend
  wan:
    subnet: 10.255.0.0/24
    impair: { delay: 80ms, jitter: 10ms, loss: 0.5% }
```

`impair` maps to `tc netem`: `delay`, `jitter`, `loss`, `duplicate`, `corrupt`, `reorder`, `rate`. Symmetric by default; `ingress:`/`egress:` for asymmetric cases.

### 3.11 Low level: top-level `nodes` (non-Consul)

For anything Parker doesn't model: a toxiproxy, a Vault server, a client that isn't a Consul agent.

```yaml
nodes:
  toxiproxy:
    networks: { dc1-lan: { ip: 10.1.0.200 } }   # default: next free IP
    namespaces: [net, mnt, uts]                  # default [net, uts]
    processes:
      proxy:
        run: toxiproxy-server -host 0.0.0.0
        ready_when: { port: 8474 }
```

### 3.12 Processes

Semantics follow process-compose. Used in raw nodes and in Consul nodes' `processes:`.

```yaml
processes:
  app:
    run: ./bin/app --port 9000      # string, or list (argv, no shell)
    env: {}
    depends_on:                     # shorthand: [db] means condition: ready
      db: { condition: ready }      # started | ready | completed | completed_successfully
      dc1: { condition: ready }     # dc ready = leader elected + all agents joined
    ready_when:                     # all must pass
      port: 9000
      http: { url: http://localhost:9000/health, status: 200 }
      exec: "curl -sf localhost:9000"
      log: "listening on"
      consul: { leader: true }      # | { members: ">=5" } | { service_healthy: api }
      timeout: 30s
    restart: no                     # no | on-failure | always
```

Built-in processes (consul agent, envoy, fake-service) ship sensible `ready_when`, so users rarely write one.

### 3.13 `scenario`

```yaml
scenario:
  steps:
    - wait: dc1 ready
    - at: +30s                                 # relative to previous step; "T+30s" absolute
      partition: [dc1, dc2]
    - at: +20s
      kill: dc1/leader
    - exec: { on: dc2/server-1, run: "consul operator raft list-peers" }
    - assert: { consul: { leader: true, dc: dc1 }, within: 15s }
    - heal: all
    - snapshot: logs
  on_failure: keep                             # keep | teardown
```

Actions: `wait`, `partition`, `heal`, `impair`, `kill`, `stop`, `start`, `restart`, `pause`, `resume`, `exec`, `assert`, `snapshot`, `sleep`, `upgrade`. Named scenarios (`scenarios: {leader-loss: ...}`) run with `parker run file.yaml leader-loss`.

---

## 4. Selectors and references

Selectors follow the tree, and the partition segment is optional when the node name is unique in the dc (Parker requires node names to be unique per dc, so it always is).

| Selector | Means |
|---|---|
| `dc1` | whole datacenter |
| `dc1/servers`, `dc1/clients`, `dc1/gateways` | role group |
| `dc1/server-2` | one server |
| `dc1/web` | node group (all `web-N` copies) |
| `dc1/web-1` or `dc1/team-a/web-1` | one node |
| `svc:api` / `svc:dc2/api` | every node that runs service `api` (in a dc) |
| `dc1/leader`, `dc1/followers` | raft role, resolved at step time |
| `toxiproxy` | top-level raw node |

**Upstream and failover references** default to the same dc and partition as the caller: `upstreams: { api: 9091 }`. Crossing scope uses explicit fields (`dc:`, `partition:`, `peer:`, `namespace:`), matching Consul's own upstream fields so nothing new has to be learnt.

---

## 5. Extension: variables, overlays, modules

### 5.1 Variables (decided: simple only)

```yaml
vars:
  servers: 3
  consul_version: 1.20.1

versions: { consul: ${vars.consul_version} }
consul:
  datacenters:
    dc1: { servers: ${vars.servers} }
```

`parker up repro.yaml --var servers=5`. Interpolation is `${vars.x}`, `${env.X}`, and runtime values (`${run_dir}`, `${node.ip}`, `${dc1.leader.ip}`). No loops or conditionals; `count:` on servers, nodes and gateways covers repetition.

### 5.2 Overlays and `--set`

Files merge in order; maps deep-merge by key, lists replace (`$patch: append` to opt in to appending).

```bash
parker up base.yaml -f with-acl.yaml -f slow-wan.yaml --set consul.datacenters.dc1.servers=5
```

Because nested paths get long, `--set` also accepts a selector prefix: `--set dc1/web:services.web.port=9000` (see 9.4).

### 5.3 Modules

A module is a YAML fragment with declared inputs, imported by path, git URL, or the built-in library. The importer's values win over the module's.

```yaml
imports:
  - module: parker/patterns/wan-federation
    with: { dcs: [dc1, dc2], via: mesh-gateways }
  - module: ./modules/observability.yaml
```

```yaml
# module file
parker: v1alpha1
kind: Module
inputs:
  via: { type: string, enum: [wan, mesh-gateways], default: wan }
provides:
  consul:
    federation: { mode: ${inputs.via} }
```

A module can also provide a **node template** to reuse one node shape across dcs (the main tool against the repetition nesting causes, see 9.1):

```yaml
consul:
  templates:
    api-node:
      count: 2
      service: { port: 9090, run: fake-service, connect: tproxy }
  datacenters:
    dc1: { nodes: { api: { from: api-node } } }
    dc2: { nodes: { api: { from: api-node, count: 1 } } }   # override fields after `from`
```

Built-in library at launch: `acl-bootstrap`, `auto-tls`, `wan-federation`, `peering`, `mesh-gateways`, `tproxy-mesh`, `ingress`, `observability`.

---

## 6. Validation and tooling

- **Go structs are the source of truth.** JSON Schema is generated from them (`invopop/jsonschema`), with doc comments as descriptions.
- **Editor support:** `# yaml-language-server: $schema=...` (written by `parker init`) gives autocomplete and hover docs. This matters more with nesting, since the editor tells you which level a key belongs to.
- **Positions:** `goccy/go-yaml` keeps line/col for every error, including inside modules.
- **Two phases:**
  1. Structural: types, enums, unknown keys (error outside `config:` / `register:` passthrough), with "did you mean" suggestions. Also catches a key at the wrong level, e.g. `servers:` under a partition ("servers belong to the datacenter").
  2. Semantic: references resolve; node names unique per dc; `nodes:` and `partitions:` not both on one dc; at most one tproxy service per node; subnets don't overlap; partitions/namespaces need `+ent`; peering needs Consul >= 1.14; warning on even server count.
- **CLI:** `parker validate`, `parker expand`, `parker explain consul.datacenters.nodes`, `parker graph`, `parker init --pattern ...`.

---

## 7. Worked examples

### 7.1 Minimal: a 3-server datacenter

```yaml
parker: v1alpha1
versions: { consul: 1.20.1 }
consul:
  datacenters:
    dc1: { servers: 3 }
```

3 netns on `dc1-lan`, `bootstrap_expect=3`, `retry_join` filled in, UI forwarded to `localhost:8500`. `parker up` returns when a leader is elected; `parker shell dc1/server-2` drops you into that netns.

### 7.2 Raft bug: mixed versions, minority partition, kill leader

```yaml
parker: v1alpha1
name: raft-flap
versions: { consul: 1.20.1 }

consul:
  datacenters:
    dc1:
      acl: true
      agents: { log_level: TRACE }
      servers:
        count: 5
        overrides:
          server-5: { version: 1.19.4 }
      server:
        config: { performance: { raft_multiplier: 1 } }
      nodes:
        client: { count: 2 }             # bare client agents, no services

scenario:
  steps:
    - wait: dc1 ready
    - partition: [[dc1/server-1, dc1/server-2], [dc1/server-3, dc1/server-4, dc1/server-5]]
    - at: +30s
      kill: dc1/leader
    - assert: { consul: { leader: true, dc: dc1 }, within: 20s }
    - heal: all
    - snapshot: logs
```

### 7.3 Two DCs, WAN federation, slow WAN, partition fault

```yaml
parker: v1alpha1
name: wan-partition
versions: { consul: 1.20.1 }

consul:
  federation: { mode: wan }
  datacenters:
    dc1:
      primary: true
      servers: 3
      nodes:
        client: {}
    dc2:
      servers: 3
      nodes:
        api:
          service: { port: 9090, run: fake-service }

networks:
  wan: { impair: { delay: 120ms, jitter: 20ms } }

scenario:
  steps:
    - wait: [dc1 ready, dc2 ready]
    - assert:
        exec: { on: dc1/client, run: "consul catalog services -datacenter dc2 | grep api" }
    - at: +30s
      partition: [dc1, dc2]
    - at: +60s
      exec: { on: dc1/server-1, run: "consul members -wan" }
    - heal: all
    - assert: { consul: { members: ">=6", wan: true }, within: 60s }
```

### 7.4 Service mesh: tproxy, peering, mesh gateways, failover

```yaml
parker: v1alpha1
name: mesh-failover
versions: { consul: 1.20.1, envoy: 1.31.2 }

consul:
  defaults:
    acl: true
    tls: true
    agents: { config: { connect: { enabled: true } } }
    service_template: { run: fake-service, connect: tproxy }   # every service inherits

  peering:
    dc1-dc2:
      acceptor: dc2
      dialer: dc1
      via_mesh_gateways: true
      exports: { dc2: [api] }

  datacenters:
    dc1:
      servers: 1
      gateways: { mesh: true }
      nodes:
        web:
          service:
            port: 8080
            env: { UPSTREAM_URIS: "http://api.virtual.consul" }
        api:
          service: { port: 9090 }
      config_entries:
        - Kind: service-intentions
          Name: api
          Sources: [{ Name: web, Action: allow }]
        - Kind: service-resolver
          Name: api
          Failover: { "*": { Targets: [{ Peer: dc1-dc2 }] } }

    dc2:
      servers: 1
      gateways: { mesh: true }
      nodes:
        api:
          service: { port: 9090, env: { NAME: api-dc2 } }

scenario:
  steps:
    - wait: svc:dc1/web healthy
    - assert: { exec: { on: dc1/web, run: "curl -sf http://api.virtual.consul" } }
    - kill: svc:dc1/api
    - assert:
        exec: { on: dc1/web, run: "curl -s http://api.virtual.consul | grep api-dc2" }
        within: 30s
```

Note how `service_template` at the top removes the `run:`/`connect:` repetition from every service.

### 7.5 Enterprise: partitions

```yaml
parker: v1alpha1
versions: { consul: 1.20.1+ent }

consul:
  datacenters:
    dc1:
      servers: 3
      partitions:
        default:
          nodes:
            frontend: { service: { port: 8080 } }
        team-a:
          service_template: { connect: tproxy, namespace: payments }
          gateways: { mesh: true }
          nodes:
            billing: { count: 2, service: { port: 9000 } }
```

---

## 8. Open questions

Decided (2026-11-19): Q1 simple vars only · Q2 under `consul:` · Q4 one agent per netns now, dataplane later.

Still open:
3. **Config entry casing.** Consul PascalCase inside `config_entries`, snake_case elsewhere. OK with the mix?
5. **Scenario timing.** Sequential steps with relative `at:`. Do you need parallel tracks, or is "background processes + sequential steps" enough?
6. **Scenario file.** Same file, plus optionally a separate file that references a topology (`topology: ./wan.yaml`)?
7. **Auto-discovered `parker.yaml`** in cwd, or always explicit?
8. **Enterprise features** first-class in v1 (the nesting now makes partitions natural) or later?
9. **Host access** defaults beyond UI forwarding and `parker shell`?
10. **New: node naming with `count`.** `web` with `count: 2` gives `web-1`, `web-2`, and with `count: 1` gives plain `web`. Or always suffix (`web-1`) for consistency?
11. **New: services spanning dcs.** Is `templates:` + `from:` (section 5.3) the right tool, or do you also want a cross-cutting `services:` view (see 9.1)?

---

## 9. Trade-offs of the nested shape

Nesting wins on locality: you read a dc top to bottom and see everything in it, and defaults set at a level flow down so you don't repeat ACL/TLS/tproxy settings per service. It mirrors how Consul scopes things, which is how Consul people already think. The costs, and how the schema handles each:

**9.1 One logical service in several places is repeated.** `api` running in dc1 and dc2 (failover, peering) is two declarations in two subtrees. In the flat shape it was one entry with two placements. Mitigation: `service_template` for shared fields, and `templates:` + `from:` for whole node shapes. If this keeps hurting in practice, a later read-only `parker services` view could list a service across the tree.

**9.2 Deep indentation.** Worst case is 8 levels to reach a service field (`consul → datacenters → dc → partitions → p → nodes → n → services → s`). Mitigations: `nodes:` directly under a dc for the default partition (most OSS repros), singular `service:` for one-service nodes, and flow style (`{ port: 9090 }`) for short leaves. Examples 7.1 to 7.4 never go deeper than 6.

**9.3 Instances are nodes, not a service field.** "3 instances of api" is `count: 3` on the node, because one netns has one agent. That's honest to the one-agent-per-namespace decision but means "instances" lives one level up from where people might look. Validation can suggest it if someone writes `instances:` on a service.

**9.4 Long override paths.** Overlays and `--set` need the full path (`consul.datacenters.dc1.nodes.web.services.web.port`). Mitigation: selector-prefixed `--set dc1/web:service.port=9000`. Overlay files are unaffected, since YAML nesting writes the path for you.

**9.5 Not everything nests cleanly.** Servers belong to a dc, not a partition. Namespaces scope services, not nodes, so namespace is a service field rather than a level. Federation and peering connect dcs, so they sit above them. Gateways are per partition in Enterprise but per dc in OSS. The schema follows Consul in each case rather than forcing one strict tree, which means a few "why is this here" moments that the validator's wrong-level hints should catch.

**9.6 Moving things is a cut and paste.** Moving a node from the default partition to `team-a`, or from dc1 to dc2, means moving a YAML block and checking that references still resolve. `parker validate` catches broken upstreams and selectors after the move.
