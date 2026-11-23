---
title: Remote use
description: Run labs on a Linux VM and drive them from your laptop, including macOS, with the same commands.
---

Parker needs Linux to run labs, but not to drive them. Run the daemon on
a Linux machine (a VM, a cloud host, a box under your desk) and use
`parker` from your laptop as if the labs were local: `up`, `exec`,
`logs`, `tui`, `ui`, everything. The macOS build of Parker is exactly
this client.

On a Mac, [`parker machine`](macos.md) does all of this for you: it runs
the daemon in a small local Linux VM and connects to it, with nothing on
this page to set up.

## Set up the daemon

On the Linux machine, start the daemon with a TCP listener:

```console
vm$ sudo parker daemon --listen :7443 --tls-san 203.0.113.7
vm$ sudo parker daemon credentials        # the token, and where the certificate is
```

- `--listen` serves the API over TLS 1.3 with a self-signed certificate,
  kept in `/var/lib/parker/tls/`. Every call needs the bearer token.
- `--tls-san` adds names or addresses to the certificate: the address
  your laptop uses to reach the VM.
- To keep it running across reboots, use the systemd unit (see
  [install](install.md#run-the-daemon-as-a-service)) with
  `PARKER_DAEMON_ARGS="--listen :7443"`.

## Point the client at it

Copy the certificate to your laptop, and set three variables:

```console
laptop$ scp vm:/var/lib/parker/tls/cert.pem parker-ca.pem
laptop$ export PARKER_HOST=https://203.0.113.7:7443 PARKER_TOKEN=... PARKER_TLS_CA=$PWD/parker-ca.pem
laptop$ parker up examples/minimal.yaml
laptop$ parker exec minimal dc1/server-1 -- consul members
```

| Variable | Is |
|---|---|
| `PARKER_HOST` | The daemon: `https://HOST:PORT`, or `unix:///path/to/socket`. `--host` sets it for one command. |
| `PARKER_TOKEN` | The token from `parker daemon credentials`. |
| `PARKER_TLS_CA` | The daemon's certificate, to trust it. |

Lab files are read and expanded on the laptop, so relative paths in them
work as usual. An [Enterprise](enterprise.md) license is read on the
laptop too, and sent over TLS.

Consul, Envoy and the other binaries are downloaded on the daemon's
machine, for its OS and CPU, never the laptop's. That holds for `up`,
`run` and the [lab builder](lab-builder.md)'s Run, whether you reach the
daemon over `https://` or a tunnelled socket.

## Reaching the lab from the laptop

```console
laptop$ parker logs minimal dc1-server-1 -f
laptop$ parker port-forward minimal dc1/server-1 8500     # http://127.0.0.1:8500/ui
laptop$ parker proxy &
laptop$ curl --proxy socks5h://127.0.0.1:1080 http://server-1.dc1.minimal.parker:8500/v1/status/leader
laptop$ parker ui
```

All of these tunnel through the daemon. See
[getting into a lab](getting-in.md).

## Safer: tunnel the socket

`parker exec` against a remote daemon is a root shell on that machine,
so the best port to expose is none. Tunnel the daemon's unix socket over
SSH instead, or put the machine on a private network such as Tailscale:

```console
laptop$ ssh -N -L /tmp/parker.sock:/run/parker/parker.sock root@vm &
laptop$ export PARKER_HOST=unix:///tmp/parker.sock
laptop$ parker status
```

This needs no `--listen`, token or certificate.

## The web UI on the daemon

A daemon started with `--listen` also serves the [web UI](web-ui.md) at
`https://HOST:PORT/`. Sign in with the token from
`parker daemon credentials`. Browser sessions can use what the UI needs
(status, topology, logs, describe, faults, scenarios, `down`, the lab
builder and Open links); `exec` stays with the CLI.

`parker ui` on the laptop is usually nicer: it needs no certificate
exception in the browser, and Open links work.

The daemon's certificate covers `*.parker.localhost`, which Open links
use. A certificate made by an older Parker doesn't: delete
`tls/cert.pem` in the daemon's state directory and restart the daemon.

## Managing the daemon

```console
$ parker daemon status          # is one running?
$ parker daemon stop            # stop it; labs keep running
$ parker daemon credentials     # print the token and certificate path
```
