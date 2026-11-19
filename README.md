# Parker

Parker reproduces Consul issues fast with Linux network namespaces: no
containers, no images. A YAML file describes the architecture; Parker wires
namespaces (one bridge per network, one veth per node), renders Consul agent
configs, and starts the agents in dependency order with readiness checks.

```yaml
# examples/minimal.yaml
parker: v1alpha1
versions: { consul: 1.22.7 }
consul:
  datacenters:
    dc1: { servers: 3 }
```

```console
$ sudo parker up examples/minimal.yaml
network: 1 networks, 3 nodes
  started dc1-server-1/consul          pid 43324
  ...
  ready   dc1-server-1/consul          5.5s
lab "minimal" is up in 5.6s
$ sudo parker exec examples/minimal.yaml dc1/server-2 -- consul members
$ sudo parker down examples/minimal.yaml
```

Status: **prototype**. Schema is `v1alpha1`, see the schema design doc (v0.2).

## Commands

| Command | What it does |
|---|---|
| `parker up FILE` | Clean up any previous run, create the network, start everything, return when all readiness checks pass. Processes keep running after it returns. On failure, prints the failing log tail and tears down (`--keep-on-failure` to inspect). |
| `parker down FILE\|LAB` | Kill every process in the lab's namespaces, delete the namespaces and labelled links. Idempotent, needs no state file. Logs are kept. |
| `parker expand FILE` | Print the low-level form (networks, raw nodes, files, processes). It is valid input to `up` and expands to itself. |
| `parker validate FILE` | Structural (unknown keys with line numbers) and semantic checks. |
| `parker exec FILE NODE -- CMD` | Run a command (default `$SHELL`) inside a node's netns, with the pinned `consul` on `PATH`. `NODE` is `dc1-server-1` or `dc1/server-1`. |
| `parker status FILE\|LAB` | List the lab's namespaces. |
| `parker fetch FILE` | Download pinned binaries into the cache. |

`--var name=value` sets `${vars.name}`.

## How it works

1. **Load**: `${vars.x}` / `${env.X}` substitution, strict YAML decode
   (goccy/go-yaml, errors with line and column), validation.
2. **Expand** (`internal/expand`): `consul.datacenters` becomes `networks:`
   (`dc1-lan`, default `10.<n>.0.0/24`, gateway `.1` on the host bridge) and
   `nodes:` (`dc1-server-1` at `.10+`, client nodes at `.100+`), each with a
   rendered `consul.json` and processes with `depends_on` and `ready_when`.
   Runtime variables (`${node_dir}`, `${run_dir}`, `${parker}`) are left for
   `up` to fill in.
3. **Network** (`internal/netsetup`, vishvananda/netlink + netns): bridge
   `pk-<hash>` per network, named netns `parker-<lab>-<node>`, veth into each
   (`eth0`, default route via the bridge). Host links get alias
   `parker:<lab>`; `down` finds everything by name and alias, and finds
   processes by matching `/proc/*/ns/net` inodes, so cleanup works after a
   crash.
4. **Supervise** (`internal/supervisor`): a goroutine per process waits on
   its dependencies (`started` / `ready`), forks it on a thread switched into
   the node's netns (`LockOSThread` + `setns`), in its own session so it
   outlives `parker up`. Readiness checks also run inside the netns:
   `consul: {leader: true}` for servers, `consul: {members: ">=N"}` for
   clients, `port` + `consul: {service_healthy}` for services.
5. **Binaries** (`internal/fetch`): `releases.hashicorp.com`, SHA256SUMS
   verified, cached in `~/.cache/parker/<product>/<version>/` (the invoking
   user's cache under sudo). `versions.consul: path:/abs/consul` uses a local
   build; `servers.overrides.server-3.version` gives mixed-version clusters.

Run directory: `/run/parker/<lab>/` (`PARKER_RUN_DIR` to change), with
`nodes/<node>/{consul.json,consul.log,data/}` and `state.json`.

## Supported so far

- `consul.defaults` / dc-level `agents`, `server`, `client` (`config`
  passthrough deep-merged, `log_level`), `service_defaults`
- `servers: N` or `{count, overrides: {server-N: {version, config}}}`
- `nodes:` with `count`, `agent: client|none`, `config`, `meta`,
  `service:` / `services:` (`port`, `run`, `env`, `check: http|tcp|none`,
  `register` passthrough), `processes:`
- `run: fake-service`: built-in echo HTTP server (`parker fake-service`)
- Low-level `networks:` and `nodes:` (files, processes, `depends_on`,
  `ready_when: port | http | log | consul`)

Not yet: partitions, multiple dcs/federation, peering, gateways, Envoy,
ACL/TLS/gossip, impairments, `scenario:`, modules/overlays/`--set`, restart
policies (there is no resident supervisor), UI forwarding to localhost.

## Development

Needs Go 1.27+ and Linux.

```console
$ go build -o bin/parker ./cmd/parker
$ go test ./...
$ hack/rootless-e2e.sh examples/clients.yaml   # no sudo needed
```

`hack/rootless-e2e.sh` runs a full `up` / `exec` / `down` inside
`unshare -Urnm`: a private user + network + mount namespace where an
unprivileged user holds `CAP_NET_ADMIN` over a throwaway network stack.
