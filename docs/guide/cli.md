---
title: Command reference
description: Every parker command and flag, with examples.
---

`parker --help` and `parker COMMAND --help` print the same from the
binary you have.

Commands that act on a running lab take the lab file or the lab's name
(`FILE|LAB`). `NODE` is a [selector](selectors.md) that names one node,
and `SELECTOR` any selector.

## Global flags

These work with every command.

| Flag | Effect |
|---|---|
| `--host ADDR` | The daemon to use: `unix:///path` or `https://host:port`. Default `$PARKER_HOST`, else the local socket. See [remote use](remote.md). |
| `--var NAME=VALUE` | Set a [variable](lab-files.md#variables). Repeatable. |
| `--overlay FILE` | Merge an [overlay](reuse.md#overlays) over the lab file. Repeatable, applied in order. |
| `--set PATH=VALUE` | Set one value over the file and overlays, as `dc1:servers=5` or `dc1/web:service.port=9000`. Repeatable. See [`--set`](reuse.md#set). |
| `-v`, `--version` | Print the version. |

## Writing labs

### parker init

```console
$ parker init [FILE] [flags]
```

Writes a starter lab file, `parker.yaml` by default. Its first line
points editors at the schema. See [starter files](lab-files.md#starter-files).

| Flag | Effect |
|---|---|
| `-p`, `--pattern` | The starter: `minimal` (default), `clients` or `raw`. |
| `-f`, `--force` | Overwrite the file. |
| `--schema` | Schema URL or path for the editor line; default the latest release's. |

### parker validate

```console
$ parker validate FILE
```

Checks a lab file without running anything. See
[validation](lab-files.md#validation).

### parker expand

```console
$ parker expand FILE [flags]
```

Prints the lab's low-level form, which is itself a valid lab file. See
[the expanded form](lab-files.md#the-expanded-form).

| Flag | Effect |
|---|---|
| `--merged` | Print the file with imports, templates, overlays and `--set` applied instead, before expansion. |

### parker schema

```console
$ parker schema > parker.schema.json
```

Prints the JSON Schema for lab files, for [editor support](lab-files.md#editor-support).

## Running labs

### parker up

```console
$ parker up FILE [flags]
```

Creates the lab's networks and starts its processes, and returns when
everything is ready. See [starting and stopping labs](lifecycle.md).

| Flag | Effect |
|---|---|
| `--keep-on-failure` | Leave the lab running when a readiness check fails. |
| `--no-daemon` | Run in this process; nothing supervises the lab after `up` returns. |
| `--no-firewall` | Don't add firewall rules for the lab's bridges; print them instead. |

### parker down

```console
$ parker down FILE|LAB [flags]
```

Stops every process and removes the lab's namespaces, links and
firewall rules. Logs are kept.

| Flag | Effect |
|---|---|
| `--purge` | Also delete the lab's run directory: logs, configs and data. |

### parker status

```console
$ parker status [FILE|LAB] [flags]
```

A lab's nodes and processes, or with no argument, every lab.

| Flag | Effect |
|---|---|
| `-o`, `--output` | `table` (default) or `json`. |

### parker describe

```console
$ parker describe FILE|LAB NODE
```

One node's expanded form: networks, rendered files and processes, and
its ACL tokens.

### parker prune

```console
$ parker prune [flags]
```

Purges every lab that isn't running: deletes their run directories.

| Flag | Effect |
|---|---|
| `-y`, `--yes` | Don't ask first. |

### parker fetch

```console
$ parker fetch FILE
```

Downloads every binary the lab pins, including upgrade targets, into
the cache on this machine, for its own OS and CPU. You rarely need it:
`up` fetches what a lab needs on the machine that runs it.

## Getting in

### parker exec

```console
$ parker exec FILE|LAB NODE [-- COMMAND...] [flags]
```

Runs a command inside a node, or a shell. See
[shells and commands](getting-in.md#shells-and-commands).

| Flag | Effect |
|---|---|
| `--env` | Print export lines that point the `consul` CLI on this machine at the node, instead of running anything. |
| `-T`, `--no-tty` | Don't allocate a terminal (remote exec). |

### parker logs

```console
$ parker logs FILE|LAB NODE[/PROCESS] [flags]
```

Prints a process's log, and changes log levels. See [logs](getting-in.md#logs)
and [log levels](log-levels.md).

| Flag | Effect |
|---|---|
| `-f`, `--follow` | Keep printing new lines. |
| `-n`, `--lines` | Lines to print first; default 50, `-1` for all. |
| `--level` | Change the log level first: `trace`, `debug`, `info`, `warn`, `error` (`critical`, `off`: Envoy), `reset` or `show`. |
| `--logger` | Change one Envoy logger, `NAME=LEVEL`. Repeatable or comma-separated. |
| `--only` | With `--level`: only `consul`, `envoy` or `fake` processes. |

### parker call

```console
$ parker call FILE|LAB NODE [PATH] [flags]
```

Sends a request to a [fake service](fake-service.md) and shows the chain
it went through. See [calling services](getting-in.md#calling-services).

| Flag | Effect |
|---|---|
| `--every` | Send a request every interval, such as `1s`, until stopped. |
| `--count` | With `--every`, stop after this many requests. |
| `-H`, `--header` | Add a request header, `'Name: value'`. Repeatable. |
| `--service` | The fake to call when the node runs several. |
| `--json` | Print the raw answer. |
| `--fact` | Show each hop's space fact. |

### parker port-forward

```console
$ parker port-forward FILE|LAB NODE [LOCAL:]REMOTE [flags]
```

Forwards a local port to a port on a node. See
[port forwarding](getting-in.md#port-forwarding).

| Flag | Effect |
|---|---|
| `--address` | Local address to listen on; default `127.0.0.1`. |

### parker proxy

```console
$ parker proxy [flags]
```

A local SOCKS5 and HTTP proxy into every running lab. See
[proxy](getting-in.md#proxy).

| Flag | Effect |
|---|---|
| `--listen` | Local address; default `127.0.0.1:1080`. |
| `--lab` | The lab for bare node names such as `dc1-server-1`. |

### parker tui

```console
$ parker tui [FILE|LAB]
```

The [terminal UI](tui.md).

### parker ui

```console
$ parker ui [FILE|LAB] [flags]
```

The [web UI](web-ui.md), served locally and opened in your browser.

| Flag | Effect |
|---|---|
| `--no-open` | Print the link instead of opening a browser. |
| `--listen` | Address for the UI; default a free loopback port. |

## Testing and repros

### parker run

```console
$ parker run FILE [SCENARIO] [flags]
```

Brings the lab up fresh and plays its scenario, or a named one. Exits 1
when a step fails. See [scenarios](scenarios.md).

| Flag | Effect |
|---|---|
| `--no-up` | Play against the running lab instead of bringing it up. |
| `--keep-on-failure` | Leave the lab running if a readiness check fails during `up`. |
| `--no-firewall` | As for `parker up`. |

### parker fault

```console
$ parker fault FILE|LAB ACTION [SELECTOR...]
```

Breaks a running lab: `partition`, `heal`, `kill`, `stop`, `start`,
`restart`, `pause` or `resume`. See [faults](faults.md).

### parker fake

```console
$ parker fake FILE|LAB SELECTOR [flags]
```

Changes a running fake service's faults. Only the flags given change;
with none, it shows the faults in force. See
[the fake service](fake-service.md#changing-faults-while-it-runs).

| Flag | Effect |
|---|---|
| `--health` | The code `/health` answers; 503 fails the Consul check. |
| `--error-rate` | Share of requests that fail, 0 to 1. |
| `--error-code` | The code a failed request answers. |
| `--error-type` | `http_error` or `delay`. |
| `--error-delay` | The delay for `--error-type delay`, such as `5s`. |
| `--latency` | Latency percentiles: `p50=200ms,p99=2s`; a bare duration is p50, `0s` clears. |
| `--variance` | Spread each latency draw by up to this percentage either way. |
| `--rate-limit` | Requests per second; 0 turns it off. |
| `--rate-limit-code` | The code for requests over the limit. |
| `--reset` | Go back to the faults the lab started with. |
| `--service` | One fake on the nodes; default all. |

## The daemon

### parker daemon

```console
$ parker daemon [flags]
```

Runs the daemon in the foreground. `parker up` starts one in the
background when needed, so you only run this yourself to serve
[remote clients](remote.md) or under systemd.

| Flag | Effect |
|---|---|
| `--listen` | Also serve remote clients on this TCP address, such as `:7443`, with TLS and a token. |
| `--tls-san` | Extra names or addresses for the self-signed certificate. |

### parker daemon status

Shows whether a daemon is running.

### parker daemon stop

Stops the daemon. Labs keep running (except [rootless](rootless.md) ones).

### parker daemon credentials

Prints what a remote client needs: the token and the certificate's path.
Creates them if they're missing.

## Other

### parker version

Prints the client's version, commit and build date, and the daemon's
when one answers.

### parker completion

```console
$ parker completion bash|zsh|fish|powershell
```

Prints a shell completion script. See
[shell completion](install.md#shell-completion).
