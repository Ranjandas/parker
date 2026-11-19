<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/assets/logo/parker-logo-dark.svg">
    <img alt="Parker" src="docs/assets/logo/parker-logo-light.svg" width="320">
  </picture>
</p>
<p align="center"><strong>Consul repros at probe speed.</strong></p>

Parker is named after the Parker Solar Probe, the fastest object humans have
ever built. Speed is the point: a Consul topology should go from a few lines
of YAML to a running cluster in seconds (a 3-server datacenter is up in
about 6s), so reproducing an issue costs less than describing it.

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

[![ci](https://github.com/Ranjandas/parker/actions/workflows/ci.yml/badge.svg)](https://github.com/Ranjandas/parker/actions/workflows/ci.yml)

Status: **prototype**. Schema is `v1alpha1`, see the [schema design doc](docs/schema-design.md) (v0.2).

## Install

Download a release (static binaries for Linux amd64/arm64; the macOS build is
a client for a daemon on a Linux host) and check it against `SHA256SUMS`:

```console
$ V=0.1.0 ARCH=amd64   # or arm64
$ curl -LO https://github.com/Ranjandas/parker/releases/download/v$V/parker_${V}_linux_$ARCH.tar.gz
$ curl -LO https://github.com/Ranjandas/parker/releases/download/v$V/SHA256SUMS
$ sha256sum --ignore-missing -c SHA256SUMS
$ tar xzf parker_${V}_linux_$ARCH.tar.gz && sudo install -m 0755 parker /usr/local/bin/parker
$ parker version
```

To keep the daemon running across reboots, install the systemd unit from the
archive (`contrib/systemd/parker.service`):

```console
$ sudo install -m 0644 contrib/systemd/parker.service /etc/systemd/system/
$ echo 'PARKER_DAEMON_ARGS="--listen :7443"' | sudo tee /etc/default/parker   # optional: remote clients
$ sudo systemctl daemon-reload && sudo systemctl enable --now parker
```

Stopping the unit stops only the daemon. Labs keep running, and
`parker down` cleans them up either way. Or build from source:
`go install github.com/Ranjandas/parker/cmd/parker@latest`.

## Commands

| Command | What it does |
|---|---|
| `parker init [FILE]` | Write a starter file (`--pattern minimal\|clients\|raw`) whose first line points your editor at the schema for autocomplete and hover docs. |
| `parker up FILE` | Clean up any previous run, create the network, start everything, return when all readiness checks pass. Runs through `parker daemon` (started in the background if needed), which keeps supervising the processes afterwards; `--no-daemon` runs it in-process. On failure, prints the failing log tail and tears down (`--keep-on-failure` to inspect). Refuses subnets that clash with the host. If the FORWARD chain drops by default (Docker), adds labelled ACCEPT rules for the lab bridges (`--no-firewall` prints them instead). |
| `parker down FILE\|LAB` | Kill every process in the lab's namespaces, delete the namespaces, labelled links and firewall rules. Idempotent, needs no state file and works without the daemon. Logs are kept. |
| `parker expand FILE` | Print the low-level form (networks, raw nodes, files, processes). It is valid input to `up` and expands to itself. |
| `parker validate FILE` | Structural and semantic checks. Unknown keys come with line numbers, "did you mean" suggestions, and where a misplaced key belongs (e.g. `servers` under a node). |
| `parker schema` | Print the JSON Schema for topology files. |
| `parker exec FILE\|LAB NODE -- CMD` | Run a command (default `$SHELL`) inside a node's netns, with the pinned `consul` on `PATH`. `NODE` is `dc1-server-1` or `dc1/server-1`. Against a remote daemon it is streamed over the API, with a terminal when stdin is one (`-T` to turn that off). |
| `parker status [FILE\|LAB]` | Nodes, IPs, processes, PIDs, state (`ready`/`starting`/`running`/`dead`), restarts and Consul raft role (`leader`/`follower`/`client`); `-o json` for scripts; with no argument, list labs. |
| `parker tui [FILE\|LAB]` | Terminal UI (k9s-style) over the daemon, local or `--host`: live node/process table with state and raft role, `s` shell into a node, `l` follow a log (`f` follow, `w` wrap, `/` filter), `d` describe, `/` filter rows, `:lab NAME`, `:dc NAME`, `:q`. |
| `parker describe FILE\|LAB NODE` | A node's expanded topology: networks, rendered files (`consul.json`), processes with command and checks. |
| `parker logs FILE\|LAB NODE[/PROC]` | Last lines of a process log (`-n`, default 50), `-f` to follow. Default process: the node's only one, else `consul`. |
| `parker proxy` | Local SOCKS5 + HTTP proxy (`127.0.0.1:1080`) into every running lab through the daemon, like `kubectl proxy`. Node names resolve as `<node>.<lab>.parker` or `server-1.dc1.<lab>.parker`; only lab addresses are reachable. |
| `parker port-forward FILE\|LAB NODE [LOCAL:]PORT` | Forward a local port to a node's port. |
| `parker daemon [stop\|status\|credentials]` | Run the daemon in the foreground (`--listen :7443` for remote clients), stop it (labs keep running), check it, or print what a remote client needs. |
| `parker version` | Client version, commit and build date, plus the daemon's when one answers. |
| `parker fetch FILE` | Download pinned binaries into the cache. |

`--var name=value` sets `${vars.name}`.

### Editor support

Files that start with

```yaml
# yaml-language-server: $schema=https://github.com/Ranjandas/parker/releases/latest/download/parker.schema.json
```

get autocomplete, hover docs and inline errors in any editor using
[yaml-language-server](https://github.com/redhat-developer/yaml-language-server)
(VS Code's YAML extension, Neovim, Helix, ...). `parker init` writes that
line. Until releases publish the schema (and while the repository is private), save it locally:
`parker schema > parker.schema.json` and `parker init --schema ./parker.schema.json`.

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
   `pk-<hash>` per network, named netns `parker-<lab>_<node>`, veth into each
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

6. **Daemon** (`internal/daemon`, API in `api/parker/v1/parker.proto`):
   `parker daemon` serves the Parker API with ConnectRPC on
   `/run/parker/parker.sock` (mode 0600), so the same service speaks gRPC
   and plain HTTP/JSON:
   ```console
   $ sudo curl --unix-socket /run/parker/parker.sock -H 'Content-Type: application/json' \
       -d '{"lab":"minimal"}' http://parker/parker.v1.ParkerService/Status
   ```
   The CLI expands the file locally and sends the low-level topology, so
   relative paths resolve on the client. The daemon is the parent of the
   lab's processes: it reaps them and applies `restart: no | on-failure |
   always` (backoff 1s to 30s) once the lab is up. Lab state still comes
   from namespaces, link aliases and `state.json`, so stopping or
   restarting the daemon never loses a lab; a new daemon reports such labs
   as not supervised until the next `up`.

Run directory: `/run/parker/<lab>/` (`PARKER_RUN_DIR` to change), with
`topology.yaml` (expanded), `nodes/<node>/{consul.json,consul.log,data/}`
and `state.json`. The daemon's socket and `daemon.log` live in
`/run/parker/`.

## Rootless mode (no sudo)

Run parker as a normal user and it uses a per-user daemon that lives in its
own user + network + mount namespace, where you are root over a private
network stack. Nothing touches the host's network, and no sudo is needed:

```console
$ parker up examples/minimal.yaml
started parker daemon in rootless mode; reach nodes with parker exec/proxy/port-forward ...
$ parker exec minimal dc1/server-1 -- consul members
$ parker port-forward minimal dc1/server-1 8500     # Consul UI on http://127.0.0.1:8500/ui
$ parker down minimal
```

- The host has no route into the lab, so `exec` (over the API), `logs`,
  `proxy` and `port-forward` are the ways in.
- Socket and run dirs: `$XDG_RUNTIME_DIR/parker/` (else `/tmp/parker-<uid>/`).
- Binaries are fetched by the CLI (the daemon has no network) into the usual
  cache.
- Labs live in the daemon's namespaces, so `parker daemon stop` takes them
  down with it.
- Needs unprivileged user namespaces. On Ubuntu 23.10+ AppArmor blocks them;
  parker says so and how to allow it
  (`sudo sysctl kernel.apparmor_restrict_unprivileged_userns=0`).
- Use `sudo parker ...` instead when the lab should be reachable from the
  host directly (bridges on the host, Docker interplay and so on).

## Remote use

Run Parker on a VM and drive it from your laptop, with the same binary and
the same commands, `exec` included:

```console
vm$ sudo parker daemon --listen :7443 --tls-san 203.0.113.7
vm$ sudo parker daemon credentials        # token + where the certificate is
laptop$ scp vm:/var/lib/parker/tls/cert.pem parker-ca.pem
laptop$ export PARKER_HOST=https://203.0.113.7:7443 PARKER_TOKEN=... PARKER_TLS_CA=$PWD/parker-ca.pem
laptop$ parker up examples/minimal.yaml   # the file is read and expanded on the laptop
laptop$ parker exec minimal dc1/server-1 -- consul members
laptop$ parker logs minimal dc1-server-1 -f
laptop$ parker proxy &                     # then browse the Consul UI on the VM's lab:
laptop$ curl --proxy socks5h://127.0.0.1:1080 http://server-1.dc1.minimal.parker:8500/v1/status/leader
laptop$ parker port-forward minimal dc1/server-1 8500   # http://127.0.0.1:8500/ui
```

The TCP listener is off unless `--listen` is given, uses TLS 1.3 with a
self-signed certificate (kept in `/var/lib/parker`, `PARKER_STATE_DIR`), and
requires the bearer token on every call. Remote exec is a root shell on the
VM, so prefer not exposing the port at all: tunnel the unix socket instead,
or put the VM on Tailscale.

```console
laptop$ ssh -N -L /tmp/parker.sock:/run/parker/parker.sock root@vm &
laptop$ PARKER_HOST=unix:///tmp/parker.sock parker status
```

## Supported so far

- `consul.defaults` / dc-level `agents`, `server`, `client` (`config`
  passthrough deep-merged, `log_level`), `service_defaults`
- `servers: N` or `{count, overrides: {server-N: {version, config}}}`
- `nodes:` with `count`, `agent: client|none`, `config`, `meta`,
  `service:` / `services:` (`port`, `run`, `env`, `check: http|tcp|none`,
  `register` passthrough), `processes:`
- `run: fake-service`: built-in echo HTTP server (`parker fake-service`)
- Several datacenters, each on its own LAN (`10.<n>.0.0/24` by default);
  `primary: true` picks the primary (default: first by name)
- `consul.federation: { mode: wan }`: servers also join a generated `wan`
  network (`10.255.0.0/24`, dc1 at `.10+`, dc2 at `.30+`, ...; declare
  `networks: { wan: { subnet: ... } }` to move it) with serf WAN, and
  `up` waits until every server sees all servers over the WAN
  (see `examples/federation.yaml`)
- `acl: true` (under `consul.defaults` or a dc): ACLs with default deny
  plus gossip encryption. Parker generates the management token, an agent
  token and the gossip key; `parker exec` (and the TUI shell) sets
  `CONSUL_HTTP_TOKEN` to the management token (see `examples/acl.yaml`).
  Federated dcs replicate tokens from the primary.
- `tls: true`: a CA per lab signs the servers' certificates
  (`server.<dc>.consul`, node IPs, their `parker proxy` names) and is the
  Connect root, so clients' `auto_encrypt` certificates chain to it too.
  RPC is mutual TLS and agent APIs are HTTPS only on 8501; `parker exec`
  sets `CONSUL_HTTP_ADDR` and `CONSUL_CACERT` (see `examples/tls.yaml`)
- `config_entries:` on a datacenter: applied in order once its servers
  (and ACLs) are ready, inline in Consul's own casing or
  `file: entries/x.hcl` (HCL or JSON); a failure names the entry and its
  YAML line (see `examples/config-entries.yaml`)
- Low-level `networks:` and `nodes:` (files, processes, `depends_on`,
  `ready_when: port | http | log | consul`, `restart`)

Not yet: partitions, mesh-gateway federation, peering, gateways, Envoy,
impairments, `scenario:`, modules/overlays/`--set`.

## Development

Needs Go 1.27+ and Linux.

```console
$ go build -o bin/parker ./cmd/parker
$ go test ./...
$ make cover                                   # unit test coverage
$ hack/rootless-e2e.sh examples/clients.yaml   # no sudo needed
$ make e2e-rootless                            # rootless mode as a normal user
$ go generate ./internal/spec                  # regenerate the JSON Schema after changing internal/spec
$ goreleaser release --snapshot --clean       # release build into dist/ (tags v* release from CI)
$ make proto                                   # regenerate internal/api (needs buf, protoc-gen-go, protoc-gen-connect-go)
```

Every change comes with unit tests for what it touches; the e2e scripts
below cover the whole flow on top. Tests that need a private network
namespace are skipped unless their env var is set (`PARKER_LIVE_IPTABLES`,
`PARKER_LIVE_NETNS`; see the CI e2e job for how to run them without sudo).

`hack/rootless-e2e.sh` runs a full `up` / `exec` / `down` inside
`unshare -Urnm`: a private user + network + mount namespace where an
unprivileged user holds `CAP_NET_ADMIN` over a throwaway network stack.
