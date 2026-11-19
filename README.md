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

## Commands

| Command | What it does |
|---|---|
| `parker up FILE` | Clean up any previous run, create the network, start everything, return when all readiness checks pass. Runs through `parker daemon` (started in the background if needed), which keeps supervising the processes afterwards; `--no-daemon` runs it in-process. On failure, prints the failing log tail and tears down (`--keep-on-failure` to inspect). Refuses subnets that clash with the host. If the FORWARD chain drops by default (Docker), adds labelled ACCEPT rules for the lab bridges (`--no-firewall` prints them instead). |
| `parker down FILE\|LAB` | Kill every process in the lab's namespaces, delete the namespaces, labelled links and firewall rules. Idempotent, needs no state file and works without the daemon. Logs are kept. |
| `parker expand FILE` | Print the low-level form (networks, raw nodes, files, processes). It is valid input to `up` and expands to itself. |
| `parker validate FILE` | Structural (unknown keys with line numbers) and semantic checks. |
| `parker exec FILE\|LAB NODE -- CMD` | Run a command (default `$SHELL`) inside a node's netns, with the pinned `consul` on `PATH`. `NODE` is `dc1-server-1` or `dc1/server-1`. Against a remote daemon it is streamed over the API, with a terminal when stdin is one (`-T` to turn that off). |
| `parker status [FILE\|LAB]` | Nodes, IPs, processes, PIDs, state (`ready`/`starting`/`running`/`dead`) and restarts; with no argument, list labs. |
| `parker logs FILE\|LAB NODE[/PROC]` | Last lines of a process log (`-n`, default 50), `-f` to follow. Default process: the node's only one, else `consul`. |
| `parker proxy` | Local SOCKS5 + HTTP proxy (`127.0.0.1:1080`) into every running lab through the daemon, like `kubectl proxy`. Node names resolve as `<node>.<lab>.parker` or `server-1.dc1.<lab>.parker`; only lab addresses are reachable. |
| `parker port-forward FILE\|LAB NODE [LOCAL:]PORT` | Forward a local port to a node's port. |
| `parker daemon [stop\|status\|credentials]` | Run the daemon in the foreground (`--listen :7443` for remote clients), stop it (labs keep running), check it, or print what a remote client needs. |
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
- Low-level `networks:` and `nodes:` (files, processes, `depends_on`,
  `ready_when: port | http | log | consul`, `restart`)

Not yet: partitions, multiple dcs/federation, peering, gateways, Envoy,
ACL/TLS/gossip, impairments, `scenario:`, modules/overlays/`--set`.

## Development

Needs Go 1.27+ and Linux.

```console
$ go build -o bin/parker ./cmd/parker
$ go test ./...
$ hack/rootless-e2e.sh examples/clients.yaml   # no sudo needed
$ make proto                                   # regenerate internal/api (needs buf, protoc-gen-go, protoc-gen-connect-go)
```

`hack/rootless-e2e.sh` runs a full `up` / `exec` / `down` inside
`unshare -Urnm`: a private user + network + mount namespace where an
unprivileged user holds `CAP_NET_ADMIN` over a throwaway network stack.
