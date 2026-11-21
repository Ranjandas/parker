---
title: Log levels
description: Raise or lower the log level of Consul agents, Envoys and fake services while the lab runs, without a restart.
---

Debug logs are what you need right when something goes wrong, and too
much the rest of the time. Parker changes log levels on running
processes, without restarting them, from the CLI, a scenario, the TUI
or the web UI.

```console
$ parker logs mesh dc1-web/web-sidecar --level debug -f   # raise it, then follow
$ parker logs mesh dc1 --level debug --only consul        # every agent in dc1
$ parker logs mesh dc1-web --logger upstream=trace        # one Envoy logger
$ parker logs mesh dc1 --level reset                      # back to the lab's levels
$ parker logs mesh dc1 --level show                       # just print them
```

The target is a node, `NODE/PROCESS`, or any [selector](selectors.md)
(`dc1/web`, `dc1`). After the change, Parker prints the level in force on
each process:

```console
$ parker logs clients dc1/web --level debug
dc1-web-1/consul (consul): debug
dc1-web-1/web (fake): debug
dc1-web-2/consul (consul): debug
dc1-web-2/web (fake): debug
```

| Flag | Effect |
|---|---|
| `--level LEVEL` | `trace`, `debug`, `info`, `warn` or `error`; Envoy also takes `critical` and `off`. `reset` goes back to the lab's levels, `show` changes nothing. |
| `--logger NAME=LEVEL` | Set one Envoy logger, such as `upstream=trace,http=debug`. Repeatable. |
| `--only KIND` | Only `consul`, `envoy` or `fake` processes. |
| `-f` | Follow the log afterwards. |

## What changes, and how

| Process | How |
|---|---|
| Consul agent | Parker sets `log_level` in the node's `consul.json` and reloads the agent. It stays in force across agent restarts, until a reset or the next `up`. |
| Envoy (sidecars, gateways, a dataplane's Envoy) | Through Envoy's admin `/logging` endpoint. |
| [Fake service](fake-service.md) | Through its control socket. |

consul-dataplane's own log level is fixed when it starts; its Envoy's
can change.

## In a scenario

The `log_level` step does the same in a [scenario](scenarios.md), for
example just before the step that triggers a problem:

```yaml
scenario:
  steps:
    - log_level: { on: dc1/web, level: debug }                  # agent, Envoys and fakes
    - log_level: { on: dc1/web, only: envoy, loggers: { upstream: trace, http: debug } }
    - log_level: { on: dc1-web-1, process: web-sidecar, level: trace }
    - log_level: { on: dc1, reset: true }
    - log_level: { on: dc1 }                                    # just show them
```

| Key | Meaning |
|---|---|
| `on` | A selector. |
| `level` | The level to set. |
| `only` | `consul`, `envoy` or `fake`. |
| `process` | One process on the nodes, by name. |
| `loggers` | Single Envoy loggers. |
| `reset` | Go back to the lab's levels. |

## In the TUI and web UI

In the [TUI](tui.md), `L` puts the selected node's agent, Envoys and
fakes at debug (or, in a log view, just that process), and `L` again
resets them. `:level LEVEL|reset|show [SELECTOR]` sets any level. The
[web UI](web-ui.md) has the same control above each process's log.

## The starting level

A lab's own levels come from its file: `log_level` under
[`agents`, `server` or `client`](datacenters.md#agent-configuration) for
Consul, and `fake: { log_level: ... }` for fake services.
