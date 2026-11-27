---
title: Getting into a lab
description: Open shells on nodes, read logs, call services, and reach the lab from your browser with a proxy or port forwards.
---

## Shells and commands

`parker exec` runs a command inside a node, or opens a shell when there's
no command:

```console
$ parker exec clients dc1/server-1                 # a shell
$ parker exec clients dc1/leader -- consul members
Node      Address          Status  Type    Build   Protocol  DC   Partition  Segment
server-1  10.1.0.10:8301   alive   server  1.22.7  2         dc1  default    <all>
...
```

The node is a [selector](selectors.md) that names one node:
`dc1/server-1`, `dc1/web-2`, `dc1/leader`, `svc:api` (when one node runs
it), or the full name `dc1-server-1`.

Inside, the command sees the node's network and the host's filesystem,
with:

| Variable | Is |
|---|---|
| `PATH` | The lab's pinned `consul` (and Envoy, once downloaded) first. |
| `CONSUL_HTTP_ADDR`, `CONSUL_HTTP_TOKEN`, `CONSUL_CACERT` | The node's agent, the management token and the CA, when the lab has [ACLs or TLS](acl-and-tls.md). |
| `PARKER_IP` | The node's LAN address. |
| `PARKER_LAB`, `PARKER_NODE` | The lab and node names. |

A shell keeps its history per node, and its prompt names the node, such
as `dc1/web-1 (clients) ~ #`. Bash still reads your `~/.bashrc` first.

To use the `consul` CLI on the host instead, `--env` prints the export
lines (see [ACLs and TLS](acl-and-tls.md#using-the-management-token)):

```console
$ eval "$(parker exec clients dc1/server-1 --env)"
```

Against a [remote daemon](remote.md), `exec` streams the command over
the API, with a terminal when your input is one. `-T` turns the terminal
off, for scripts.

## Logs

```console
$ parker logs clients dc1-server-1 -f         # follow the Consul agent's log
$ parker logs clients dc1-web-1/web -n 100    # the last 100 lines of the web service
$ parker logs clients dc1-web-1/web -n -1     # all of it
```

The argument is `NODE` or `NODE/PROCESS`. Without a process, it's the
node's only process, or else `consul`. `parker status` lists the
processes.

| Flag | Effect |
|---|---|
| `-f`, `--follow` | Keep printing new lines. |
| `-n`, `--lines` | Lines to print first; default 50, `-1` for all. |
| `--level`, `--logger`, `--only` | Change log levels first. See [log levels](log-levels.md). |

The files themselves are in the node's directory under the lab's run
directory, `nodes/<node>/<process>.log`, and stay there after
`parker down`.

## Calling services

`parker call` sends a request to a [fake service](fake-service.md) from
inside its node, and prints the path the request took:

```console
$ parker call mesh dc1/web
web  dc1-web · dc1  200  2.887612ms
└─ api  dc1-api · dc1  200  5.48µs
```

| Flag | Effect |
|---|---|
| `--every 1s` | Keep calling, and show which datacenter and version answered each time. |
| `--count N` | With `--every`, stop after N requests. |
| `-H 'Name: value'` | Add a header, such as `X-Parker-Fault` for a [fault on one request](fake-service.md#faults-on-one-request). |
| `--pad-header NAME=SIZE` | Add a header of that size, such as `Cookie=9KB`, to reach a [header limit](fake-service.md#request-size-limits). The size is the whole `Name: value` line. |
| `--body-size SIZE` | Send a filler body of that size, such as `2MB`, as a POST. |
| `--service NAME` | Which fake to call when the node runs several. |
| `--json` | Print the raw answer. |
| `--fact` | Show each hop's space fact. |

A path after the node calls that path: `parker call mesh dc1/web /orders`.

## Port forwarding

`parker port-forward` makes a node's port available on your machine,
for a browser or a local tool:

```console
$ parker port-forward clients dc1/server-1 8500            # http://127.0.0.1:8500/ui
$ parker port-forward clients dc1/leader 18500:8500        # a different local port
$ parker port-forward clients dc1/web-1 8080 --address 0.0.0.0
```

It runs until you stop it with Ctrl-C, and works the same with a
[rootless](rootless.md) or [remote](remote.md) daemon.

## Proxy

`parker proxy` opens a SOCKS5 and HTTP proxy on `127.0.0.1:1080` that
reaches every node of every running lab, by address or by name:

```console
$ parker proxy &
$ curl --proxy socks5h://127.0.0.1:1080 http://server-1.dc1.clients.parker:8500/v1/status/leader
$ curl --proxy http://127.0.0.1:1080 http://10.1.0.10:8500/ui/
```

Names resolve on the daemon's side:

| Name | Example |
|---|---|
| `<name>.<dc>.<lab>.parker` | `server-1.dc1.clients.parker` |
| `<node>.<lab>.parker` | `dc1-server-1.clients.parker` |
| `<name>.<partition>.<dc>.<lab>.parker` | `billing-1.team-a.dc1.ent.parker` |
| `<node>.parker` | when only one lab has that node |

Point a browser at the proxy to browse the Consul UI and services by
name. `--listen` moves the proxy, and `--lab NAME` lets bare node names
(`dc1-server-1`) work. Only addresses inside lab networks are reachable.

## From the host directly

With a root daemon, the lab's bridges are on the host, so the host
reaches every node's address directly:

```console
$ curl http://10.1.0.10:8500/v1/status/leader
```

Under a [rootless](rootless.md) daemon, use the commands above instead.
