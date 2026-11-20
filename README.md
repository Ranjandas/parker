<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/assets/logo/parker-logo-dark.svg">
    <img alt="Parker" src="docs/assets/logo/parker-logo-light.svg" width="320">
  </picture>
</p>
<p align="center"><strong>Consul repros at probe speed.</strong></p>
<p align="center"><a href="https://ranjandas.github.io/parker/">Website</a> · <a href="https://ranjandas.github.io/parker/start.html">Get started</a></p>

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

The install script picks the latest release for your OS and CPU, checks it
against `SHA256SUMS` and installs `parker` into `/usr/local/bin`:

```console
$ curl -fsSL https://ranjandas.github.io/parker/install.sh | sh
```

Or do the same by hand: download a release (static binaries for Linux amd64/arm64; the macOS build is
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
| `parker down FILE\|LAB` | Kill every process in the lab's namespaces, delete the namespaces, labelled links and firewall rules. Idempotent, needs no state file and works without the daemon. Logs are kept; `--purge` also deletes the lab's run dir (logs, configs, data) and says how much it freed. |
| `parker prune [-y]` | Purge every lab that isn't running, after asking (`-y` skips the question). |
| `parker expand FILE` | Print the low-level form (networks, raw nodes, files, processes). It is valid input to `up` and expands to itself. |
| `parker validate FILE` | Structural and semantic checks. Unknown keys come with line numbers, "did you mean" suggestions, and where a misplaced key belongs (e.g. `servers` under a node). |
| `parker schema` | Print the JSON Schema for topology files. |
| `parker run FILE [SCENARIO]` | Bring the lab up fresh and play its `scenario:` (or a named one from `scenarios:`); exits 1 when a step fails. `--no-up` plays against the running lab. See [Scenarios](#scenarios). |
| `parker fault FILE\|LAB ACTION [SELECTOR...]` | Break a running lab on purpose: `partition`, `heal`, `kill`, `stop`, `start`, `restart`, `pause`, `resume`. See [Faults](#faults). |
| `parker exec FILE\|LAB NODE -- CMD` | Run a command (default `$SHELL`) inside a node's netns, with the pinned `consul` on `PATH`. `NODE` is a [selector](#selectors) that names one node, e.g. `dc1/server-1` or `dc1/leader`. Against a remote daemon it is streamed over the API, with a terminal when stdin is one (`-T` to turn that off). |
| `parker status [FILE\|LAB]` | Nodes, IPs, processes, PIDs, state (`ready`/`starting`/`running`/`dead`), restarts and Consul raft role (`leader`/`follower`/`client`); `-o json` for scripts; with no argument, list labs as `running` or `down` (a downed lab stays listed, with its logs, until it is purged). |
| `parker ui [FILE\|LAB]` | Web UI in your browser, for a local or `--host` daemon: labs, a live node and process table by datacenter with Consul health and leaders, problems called out, node pages with processes, health checks, `describe` and a streaming log viewer (filter, warn/error), faults (restart, stop/start, pause/resume, partition, kill, heal), scenario runs and `down`, and a lab builder that writes topology YAML from a canvas or edits an existing file in place, and runs it. Serves on a loopback port and opens a one-time sign-in link; `--no-open` prints it. See [Web UI](#web-ui). |
| `parker tui [FILE\|LAB]` | Terminal UI (k9s-style) over the daemon, local or `--host`: live node/process table grouped by datacenter and partition (servers, then clients) with state, role and a CONSUL column (each agent's alive/known LAN members, each service's catalog health, dataplane nodes included). A lab with several datacenters or admin partitions opens on a datacenter level (nodes, processes up, leader, members, service health; `enter` opens one, `:dc [NAME[/PARTITION]]` jumps), and `:svc` lists the catalog's services with each instance's health and its sidecar's (`enter` goes to the node). `←`/`→` fold and unfold a node (`space` toggles, `z` all; nodes fold on their own when the lab overflows the screen, unless something is wrong on them), `s` shell into a node, `l` follow a log (`f` follow, `w` wrap, `/` filter), `d` describe, `/` filter rows, `:lab NAME`, `:q`. Faults on the selected node: `K` kill, `R` restart, `S` stop/start, `P` pause/resume, `x` partition it from the rest, `H` heal (disruptive ones ask first), or `:kill SELECTOR`, `:partition GROUP...` etc.; `:run [SCENARIO]` plays a scenario and streams its steps; `:pf [LOCAL:]REMOTE` port-forwards from the selected node while the TUI runs. `NO_COLOR` or `PARKER_TUI_STATIC=1` keeps the logo still. |
| `parker describe FILE\|LAB NODE` | A node's expanded topology: networks, rendered files (`consul.json`), processes with command and checks. |
| `parker logs FILE\|LAB NODE[/PROC]` | Last lines of a process log (`-n`, default 50), `-f` to follow. Default process: the node's only one, else `consul`. |
| `parker proxy` | Local SOCKS5 + HTTP proxy (`127.0.0.1:1080`) into every running lab through the daemon, like `kubectl proxy`. Node names resolve as `<node>.<lab>.parker`, `server-1.dc1.<lab>.parker` or, inside an admin partition, `billing-1.team-a.dc1.<lab>.parker` (the short form works while only one partition has that name); only lab addresses are reachable. |
| `parker port-forward FILE\|LAB NODE [LOCAL:]PORT` | Forward a local port to a node's port. |
| `parker daemon [stop\|status\|credentials]` | Run the daemon in the foreground (`--listen :7443` for remote clients), stop it (labs keep running), check it, or print what a remote client needs. |
| `parker version` | Client version, commit and build date, plus the daemon's when one answers. |
| `parker fetch FILE` | Download pinned binaries into the cache. |

`--var name=value` sets `${vars.name}`.

### Overlays and `--set`

Any command that reads a FILE also takes overlays and single values on
top of it, so one base file covers many variants of a repro:

```bash
parker up repro.yaml --overlay with-acl.yaml --overlay slow-wan.yaml \
  --set dc1:servers=5 --set dc1/web:service.port=9000
```

- `--overlay FILE` merges in order. Maps merge by key, `key: null`
  removes a key, and lists replace unless they start with
  `{$patch: append}`. Unknown keys are reported against the overlay that
  has them. A `vars:` block in an overlay sets vars for every file, and
  `--var` beats them all.
- `--set PATH=VALUE` comes last. PATH is dot-separated from the top
  (`consul.datacenters.dc1.servers=5`), or rooted at a
  [selector](#selectors): `dc1:` (the datacenter), `dc1/web:` (a node or
  gateway), or `dc1/team-a/billing:` (one inside a partition). VALUE is
  YAML, so `5`, `true`, `[a, b]` and `{port: 9000}` keep their types, and
  `null` removes the key.
- `parker expand --merged FILE ...` prints the merged file before
  expansion. Plain `expand` shows the expanded result.

See [examples/overlays/acl.yaml](examples/overlays/acl.yaml).

### Templates and modules

`consul.templates` holds reusable node shapes. A node takes one with
`from: NAME`, and its own fields win, so one shape covers every place a
service runs:

```yaml
consul:
  templates:
    api-node: { count: 2, service: { port: 9090, run: fake-service } }
  datacenters:
    dc1: { nodes: { api: { from: api-node } } }
    dc2: { nodes: { api: { from: api-node, count: 1 } } }
```

Templates can build on other templates with `from:`, and partition nodes
can use them too.

A module is a file with `kind: Module`, typed `inputs:` and a
`provides:` block, which can hold any part of a topology. `imports:`
merges modules in order under the importing file, so the importer's
values win:

```yaml
imports:
  - module: modules/acl.yaml          # relative to this file
    with: { dc: dc1, servers: 5 }
```

`${inputs.NAME}` reads an input. An input's `type` (`string`, `number`
or `bool`), `enum` and `default` are checked, and an input without a
default is required. Unknown keys in a module are reported at their line
in the module file. Modules may import modules, and overlays may import
them too. `parker expand --merged` shows the result. See
[examples/templates.yaml](examples/templates.yaml) and
[examples/modules/acl.yaml](examples/modules/acl.yaml).

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

## Scenarios

A scenario is a list of steps that `parker run` plays against the lab, on
the daemon, so it keeps running if your terminal goes away:

```yaml
scenario:
  steps:
    - wait: dc1 ready
    - exec: { on: dc1/leader, run: "consul operator raft list-peers" }
    - assert: { consul: { leader: true, peers: ">=3" }, within: 10s }
    - at: +30s                               # after the previous step; T+30s = after the start
      assert:
        exec: { on: dc1/web, run: "curl -sf localhost:8080", output: web }
        within: 10s
    - snapshot: logs                         # copied to <run dir>/snapshots/<time>/
  on_failure: keep                           # keep (default) | teardown
```

Steps: `wait` (a selector, optionally followed by `ready`), `sleep`,
`exec` (on every node a selector matches; non-zero fails the step unless
`allow_failure: true`), `assert` (`consul: {leader, members, peers,
service_healthy, dc}` or `exec: {on, run, output}`, retried until
`within:`) and `snapshot: logs`. A string `run:` goes through `sh -c`.
Named scenarios live under `scenarios:` and run with `parker run FILE NAME`.
See `examples/scenario.yaml`.

### Faults

```yaml
    - partition: [[dc1/server-1, dc1/server-2], [dc1/server-3]]   # or [dc1, dc2]
    - partition: [dc1/leader]          # one group: cut off from the rest of the lab
    - heal: all                        # or a selector: every cut to or from its nodes
    - kill: dc1/leader                 # SIGKILL; the process's restart policy applies
    - stop: dc1/web                    # SIGTERM (SIGKILL after 10s), stays down until start
    - start: dc1/servers               # starts what isn't running, waits for readiness
    - restart: dc1/web                 # stop, then start
    - pause: dc1/leader                # SIGSTOP until resume (SIGCONT)
```

A partition drops packets between the sides both ways, as a cut link would:
it adds blackhole routes inside the node namespaces, so it needs no iptables,
works rootless, and goes away with `heal` or `parker down`. `parker status`
shows `stopped` and `paused` processes. The same faults work by hand on a
running lab: `parker fault FILE kill dc1/leader`, `parker fault FILE partition
dc1/server-1,dc1/server-2 dc1/server-3`, `parker fault FILE heal`. See
`examples/faults.yaml`.

### Rolling upgrades

```yaml
    - upgrade: { on: dc1/servers, to: 1.22.7 }     # binary: consul (default)
```

One node at a time: servers first, dc by dc with the raft leader last, then
clients and gateways. Each node's Consul is stopped, switched to the new
binary and started again; after a server, the step waits (up to 2 minutes)
for autopilot to report every server healthy and the upgraded one on the new
version. Processes that depend on the agent keep running. `parker fetch`
also downloads upgrade targets. See `examples/upgrade.yaml`.

## Selectors

`exec`, `port-forward`, `fault` and scenarios (and later `--set`) pick nodes
with selectors that follow the YAML tree:

| Selector | Means |
|---|---|
| `dc1` | every node of the datacenter |
| `dc1/servers`, `dc1/clients`, `dc1/gateways` | a role group |
| `dc1/server-2`, `dc1/web-1` | one node (`dc1/default/web-1` works too) |
| `dc1/web` | a node group: every copy of `nodes.web` (`count: N`), in every partition |
| `svc:api`, `svc:dc2/api` | the nodes that run service `api` |
| `dc1/team-a/billing-1`, `dc1/team-a/billing` | a node or group inside admin partition `team-a` |
| `part:team-a`, `part:dc1/team-a` | the client agents in a partition (`part:default` for the rest) |
| `ns:payments`, `ns:dc1/payments` | the nodes that register a service in namespace `payments` |
| `dc1/leader`, `dc1/followers` | raft roles, asked of the running servers |
| `toxiproxy`, `dc1-server-1` | a node by its expanded name |

A command that needs one node refuses a selector that matches several,
and lists them.

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
   `versions.envoy: 1.35.12` fetches HashiCorp's Envoy build the same way
   (`parker fetch`, and `parker exec` puts it on `PATH` once cached), and
   `validate` / `up` warn when the pinned Consul doesn't support that Envoy
   (from Consul's own supported-versions list, 1.16 to 1.22).

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

Run directory: `/var/lib/parker/run/<lab>/` (`PARKER_RUN_DIR` to change),
with `topology.yaml` (expanded), `nodes/<node>/{consul.json,consul.log,data/}`
and `state.json`. It is on disk because Consul servers preallocate 64 MB
Raft WAL segments, which soon fill a `/run` tmpfs; `up` warns when the run
dir looks too small. `daemon.log` sits in the run base and the socket in
`/run/parker/` (in the run base with `PARKER_RUN_DIR`).

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
- Socket: `$XDG_RUNTIME_DIR/parker/` (else `/tmp/parker-<uid>/`). Lab run
  dirs: `$XDG_STATE_HOME/parker/run/` (else `~/.local/state/parker/run/`),
  on disk rather than the runtime tmpfs.
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

## Web UI

`parker ui` opens the dashboard in your browser. It works against the local
daemon and a remote one alike: the CLI serves the UI on a loopback port and
forwards its API calls to whatever daemon the CLI uses, so a remote daemon
needs no browser certificate exception.

```console
$ parker ui examples/mesh-federation.yaml
parker web UI on http://127.0.0.1:40121 (daemon: /run/parker/parker.sock)
open: http://127.0.0.1:40121/#token=...&lab=meshfed
```

The link's sign-in token sits in the URL fragment, which browsers never send
to a server. The page trades it for an HttpOnly, SameSite=Strict session
cookie and drops it from the address bar. The link only works while that
`parker ui` runs.

The topology view (`#/lab/NAME/topology`) draws the lab's architecture:
datacenters and partitions, each node with its processes, which services
call which and through which mesh or terminating gateways, where ingress
and API gateways route, and WAN federation and peering links. Health comes
from the same live status as the overview, so when a sidecar fails, the
paths through it turn red and say why. Pick a node for its processes,
checks, connections and fault actions; drag to pan, scroll to zoom.

Nodes that serve a web UI get **Open** links, on the node page and in the
topology view's panel: the Consul UI of each server (over HTTPS with the
lab CA when the lab has TLS, with a *Copy token* button for the ACL
management token) and each service's port. The daemon proxies them from
inside the lab, so they work against a remote daemon too. Each app gets an
origin of its own, `<node>-<port>--<lab>.parker.localhost` on the UI's
port, which browsers resolve to loopback: apps with absolute paths work,
and their scripts can't reach the Parker UI or its session. The link
carries a one-time ticket that signs the browser in to that app only.
Because `*.localhost` names point at the browser's own machine, use
`parker ui` (or a browser on the daemon host) for app links.

The lab builder (`#/build`, under *Build* in the sidebar) writes a
topology file from a canvas. Drag datacenters, partitions, server groups,
client and dataplane nodes, services and gateways in from the palette, or
start from a pattern (the `parker init` ones, mesh federation, peering).
Link a service to the one it calls by dragging from its dot: the upstream
gets `dc:`, `peer:` or `partition:` from where the two sit. Link two
datacenters to federate or peer them, and a gateway to the services it
routes to. The inspector edits the selected item, with the field docs
from `parker schema`. The YAML next to it reads like the examples: shared
settings go in `consul.defaults`, and the builder writes the config
entries the links need (local mesh gateways, exports, gateway routes and,
with ACLs on, intentions). It is checked as `parker validate` checks it as
it changes, with problems pointing at their lines. The builder keeps your
work in the browser.

The builder also opens files: *Open…* takes a path on the machine serving
the page (`~/labs/mesh.yaml`; relative imports resolve there) or an
upload. The file is then the source of truth. The canvas shows what it
merges to, and an edit on the canvas is spliced into the file's text, so
its comments, anchors and key order stay where they were; an untouched
file round-trips byte for byte. Type in the YAML pane and the canvas
follows. *Merged* shows the file with imports and templates applied and
vars substituted, *Expanded* the low-level form `parker up` runs. What an
import adds is locked on the canvas (rename or remove it in the module),
a node made from a template says so, and a value set by a `${var}` is
changed through the var. *Save…* shows the diff before it writes the file
(or downloads it); *Run* brings the lab up through the daemon, streaming
its progress, and opens its dashboard; *Down* takes it down.

A daemon started with `--listen` also serves the UI itself at
`https://HOST:PORT/`: sign in with the token from `parker daemon
credentials`. Browser sessions reach only the calls the UI needs (status,
the topology graph, validate, logs, describe, scenarios and faults, down, and the
app links above), plus the builder's: open and save `.yaml` files on that
host and bring a lab up. Exec and dial stay with the CLI. Its certificate
covers `*.parker.localhost` when Parker creates it; an older certificate
(`tls/cert.pem` in the daemon's state directory) doesn't, so delete it and
restart the daemon, or use `parker ui`.

To work on the UI without a lab, `go run ./hack/uidemo` serves it against a
fake daemon.

## Consul Enterprise

Pin an Enterprise build with `+ent` (`versions: { consul: 1.22.7+ent }`)
and give Parker a license. It looks, in order, at `license: { file: PATH }`
in the lab file (absolute, `~/...`, or relative to the file), then the file
at `CONSUL_LICENSE_PATH`, then `CONSUL_LICENSE` itself. `parker up` stops
before fetching or starting anything when a lab needs a license and has
none, and says which pin needs it.

The license only reaches the agents. The client reads it and sends it with
`up` (over TLS to a remote daemon); the daemon writes it to one `0600` file
in the lab's run dir, hands agents its path as `CONSUL_LICENSE_PATH`, and
removes it on `down` or when `up` fails. It never goes into `parker expand`
output, the saved topology, `describe` or logs. See
[examples/enterprise.yaml](examples/enterprise.yaml).

### Admin partitions and namespaces

A datacenter can split its nodes and gateways into `partitions:` (the
default one is `default`; without other partitions, nodes stay straight
under the dc). Each partition takes `client`, `service_defaults`, `nodes`,
`gateways` and `config_entries`, and a service picks its namespace with
`namespace:`:

```yaml
dc1:
  servers: 3
  partitions:
    default:
      nodes:
        frontend:
          service:
            port: 8080
            connect: sidecar
            upstreams: { billing: { port: 9091, partition: team-a, namespace: payments } }
    team-a:
      service_defaults: { connect: sidecar, namespace: payments }
      gateways: { mesh: true }
      nodes:
        billing: { count: 2, service: { port: 9000 } }
    team-b:          # the same billing again, to fail over to
      service_defaults: { connect: sidecar, namespace: payments }
      gateways: { mesh: true }
      nodes:
        billing: { service: { port: 9000 } }
```

Parker creates the partitions and namespaces as soon as the leader is up
(the `tenancy` process on `server-1`), joins each agent to its partition,
registers services, checks and sidecars in their namespace, and writes a
partition's `config_entries` into it. With ACLs each partition gets its own
agent token. Partitions can reuse a node name, as Consul allows: nodes
outside the default partition expand to `<dc>-<partition>-<name>`, so
`billing-1` above is `dc1-team-a-billing-1` (and `billing-1` to Consul).
`dc1/team-a/billing-1` or `billing-1.team-a.dc1.<lab>.parker` name it. Partitions and namespaces need an
Enterprise pin, and can't be combined with federation or peering yet. See
[examples/partitions.yaml](examples/partitions.yaml): frontend calls
team-a's billing and fails over to team-b's when team-a's is stopped.
`make e2e-ent` runs it too.

## Supported so far

- `consul.defaults` / dc-level `agents`, `server`, `client` (`config`
  passthrough deep-merged, `log_level`), `service_defaults`
- `servers: N` or `{count, overrides: {server-N: {version, config}}}`
- `nodes:` with `count`, `agent: client|dataplane|none`, `config`, `meta`,
  `service:` / `services:` (`port`, `run`, `env`, `check: http|tcp|none`,
  `register` passthrough), `processes:`
- `agent: dataplane` (needs `versions.consul-dataplane` and
  `versions.envoy`): the node runs no Consul agent, as on Kubernetes.
  A `dataplane-health` process on the dc's server-1 registers its services
  and sidecars in the catalog and probes them every second, as the
  consul-k8s endpoints controller does; each sidecar is `consul-dataplane`,
  which finds the servers over gRPC (8502, or 8503 with `tls: true`, where
  servers don't ask dataplanes for a client certificate) and logs in with
  the agent token under ACLs. Dataplane nodes can't use `connect: tproxy`
  yet (see `examples/dataplane.yaml`)
- `run: fake-service`: built-in fake service (`parker fake-service`). It answers with its name, node, dc and version, and on each request calls its upstreams (by default the service's `upstreams:`, through its sidecar), nesting their answers under `upstream_calls` with the time each hop took. The `fake:` block sets the message, version, upstreams, latency, error and rate-limit faults, health and ready codes, and `protocol: http|grpc|tcp`; nicholasjackson/fake-service's env vars (`MESSAGE`, `UPSTREAM_URIS`, ...) work too. `parker fake` changes faults while it runs, `parker call` prints a request's path as a tree, and the fake serves its own page with fault controls at `/ui`. See [docs/fake-service.md](docs/fake-service.md) and the failover demo in [`examples/fake-chain.yaml`](examples/fake-chain.yaml).
- Several datacenters, each on its own LAN (`10.<n>.0.0/24` by default);
  `primary: true` picks the primary (default: first by name)
- `consul.federation: { mode: wan }`: servers also join a generated `wan`
  network (`10.255.0.0/24`, dc1 at `.10+`, dc2 at `.30+`, ...; declare
  `networks: { wan: { subnet: ... } }` to move it) with serf WAN, and
  `up` waits until every server sees all servers over the WAN
  (see `examples/federation.yaml`)
- `gateways: { mesh: true }` on a datacenter (or `{kind: mesh, count,
  wan}` under any name): mesh gateway nodes, each a client agent plus
  `consul connect envoy -gateway=mesh -register` on 8443. They join the
  `wan` network (at `.200+`, `.210+`, ...) when the datacenters are
  federated; selector `dc1/gateways`
- `kind: ingress | terminating | api` gateways, with `entry:` holding the
  gateway's config entry in Consul casing (`Listeners`, `Services`);
  Parker sets its Kind and Name and applies it after `config_entries`.
  `parker exec` sets `PARKER_IP` to the node's LAN address, handy for
  `curl http://$PARKER_IP:8080/` on a gateway (see `examples/gateways.yaml`)
- `consul.federation: { mode: mesh-gateways }` (needs `tls: true` and a
  mesh gateway per datacenter): servers keep serf on their LAN address,
  which other datacenters can't route to, and reach each other only
  through the gateways; secondaries start once the primary's gateway is
  ready and `up` waits until the primary reaches every secondary (see
  `examples/mesh-federation.yaml`)
- `consul.peering: { dc1-dc2: { acceptor: dc2, dialer: dc1, exports: {
  dc2: [api] } } }` (Consul 1.14+, a mesh gateway in each side): the
  acceptor's server-1 generates the token, the dialer's establishes the
  peering and `up` waits until it is active; the name is the peer's name
  on both sides (`upstreams: { api: { port: 9091, peer: dc1-dc2 } }`).
  `exports` writes the exported-services entry. `via_mesh_gateways: true`
  sends the control plane through the gateways; without it the servers
  join the wan and the token names their wan gRPC addresses (see
  `examples/peering.yaml`)
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
- `connect: sidecar` on a service (or `service_defaults`): Parker turns
  Connect on, registers a sidecar and runs `consul connect envoy` for it
  (needs `versions.envoy`). `upstreams: { api: 9091 }` binds an upstream
  on `127.0.0.1:9091`; the long form takes `{port, dc, peer, partition,
  namespace}` (see `examples/mesh.yaml`).
- `connect: tproxy`: a transparent proxy (one per node). Parker redirects
  the node's TCP through Envoy with iptables and its DNS to the local
  agent, so `curl http://api.virtual.consul:9090/` works from `parker
  exec` with no upstreams declared (see `examples/tproxy.yaml`). Envoy's
  own traffic is told apart by a socket mark, not a UID, so it works in
  rootless labs. Inbound TCP to the node goes through Envoy too, except
  Consul's ports, so `port-forward` to the app is refused like any
  non-mesh caller.
- Low-level `networks:` and `nodes:` (files, processes, `depends_on`,
  `ready_when: port | http | log | consul`, `restart`)

- `scenario:` / `scenarios:` with waits, execs, asserts, faults and rolling
  upgrades (see [Scenarios](#scenarios))

Not yet: partitions, impairments, modules/overlays/`--set`.

## Development

Needs Go 1.27+ and Linux.

```console
$ go build -o bin/parker ./cmd/parker
$ go test ./...
$ make cover                                   # unit test coverage
$ hack/rootless-e2e.sh examples/clients.yaml   # no sudo needed
$ make e2e-rootless                            # rootless mode as a normal user
$ CONSUL_LICENSE_PATH=~/consul.hclic make e2e-ent   # Enterprise; skips without a license
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
`make e2e` runs every lab in `hack/e2e/suite.txt` in turn. CI runs each
lab as its own parallel job. On a PR it runs only the labs the change
touches (`hack/e2eplan` maps paths to labs: docs and unit-test-only changes
run none, core code runs them all). Every push to main and a nightly run
cover the whole suite. New labs go in `suite.txt`. Enterprise labs need a license,
so they aren't in the suite: `make e2e-ent` (`hack/ent-e2e.sh`) runs them
where one is, and skips cleanly where there's none.
