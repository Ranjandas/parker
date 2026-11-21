---
title: Rootless mode
description: Run labs as a normal user without sudo, in a private network namespace that never touches the host's network.
---

Run `parker` as yourself, without `sudo`, and it uses a daemon of your
own that lives in a private user, network and mount namespace. Inside
it you're root over a network stack of your own, so labs work in full
while nothing touches the host's network.

```console
$ parker up examples/minimal.yaml
started parker daemon in rootless mode; reach nodes with parker exec/proxy/port-forward ...
$ parker exec minimal dc1/server-1 -- consul members
$ parker port-forward minimal dc1/server-1 8500     # Consul UI on http://127.0.0.1:8500/ui
$ parker down minimal
```

Every feature works rootless, including the mesh, transparent proxies,
gateways and network partitions.

## Reaching the lab

The host has no route into a rootless lab. These are the ways in:

- [`parker exec`](getting-in.md#shells-and-commands) for a shell or a
  command;
- [`parker logs`](getting-in.md#logs);
- [`parker port-forward`](getting-in.md#port-forwarding) and
  [`parker proxy`](getting-in.md#proxy) for browsers and local tools;
- the [web UI](web-ui.md)'s Open links.

`parker exec --env` doesn't help here, since the host can't reach the
node's address.

## Root or rootless?

| | Root (`sudo parker`) | Rootless (`parker`) |
|---|---|---|
| Needs | root | unprivileged user namespaces |
| Host reaches node addresses | yes | no: use exec, proxy, port-forward |
| Touches the host network | bridges and firewall rules, removed by `down` | nothing |
| Labs survive `parker daemon stop` | yes | no: they live in the daemon's namespaces |

Root and rootless labs are separate, each with its own daemon and list
of labs. Use root when the lab should be reachable from the host
directly, or has to work with other things on the host, such as Docker.

## Where things live

| What | Where |
|---|---|
| Daemon socket | `$XDG_RUNTIME_DIR/parker/`, else `/tmp/parker-<uid>/` |
| Lab run directories | `$XDG_STATE_HOME/parker/run/`, else `~/.local/state/parker/run/` |
| Binaries | `~/.cache/parker/`, as usual. The CLI downloads them, since the daemon has no network. |

## Ubuntu and AppArmor

Rootless mode needs unprivileged user namespaces. On Ubuntu 23.10 and
later, AppArmor blocks them by default. Parker says so, and how to allow
them:

```console
$ sudo sysctl kernel.apparmor_restrict_unprivileged_userns=0
```

Or use `sudo parker` instead.
