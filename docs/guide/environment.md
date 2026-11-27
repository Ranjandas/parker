---
title: Environment variables
description: The environment variables Parker reads, and the ones it sets for the commands you run inside a lab.
---

## Variables Parker reads

| Variable | Effect |
|---|---|
| `PARKER_HOST` | The daemon to use: `unix:///path` or `https://host:port`, as `--host`. See [remote use](remote.md). |
| `PARKER_TOKEN` | The token for a remote daemon, from `parker daemon credentials`. |
| `PARKER_TLS_CA` | The remote daemon's certificate, to trust it. |
| `PARKER_RUN_DIR` | Where labs' run directories go, instead of `/var/lib/parker/run`. The daemon's socket and log move there too. |
| `PARKER_STATE_DIR` | Where the daemon keeps its TLS certificate and token, instead of `/var/lib/parker`. |
| `PARKER_CACHE` | Where downloaded binaries are cached, instead of `~/.cache/parker`. [`parker machine`](macos.md) keeps its images in `images/` under it. |
| `PARKER_MACHINE_DIR` | Where [`parker machine`](macos.md) keeps the VM, instead of `~/.parker/machine/default`. |
| `PARKER_MACHINE_BINARY` | A Linux `parker` binary for the [machine's VM](macos.md#which-parker-runs-in-the-vm) to run instead of a release. |
| `PARKER_RELEASES_URL` | Where to download binaries from, instead of `https://releases.hashicorp.com`: a mirror with the same layout. |
| `CONSUL_LICENSE_PATH`, `CONSUL_LICENSE` | The Consul Enterprise license, a file or the license itself. See [Enterprise](enterprise.md#the-license). |
| `NO_COLOR`, `PARKER_TUI_STATIC` | Keep the [TUI](tui.md)'s logo still. |
| `XDG_RUNTIME_DIR`, `XDG_STATE_HOME` | Where [rootless](rootless.md) mode puts its socket and run directories. |

Lab files can read any variable with `${env.NAME}` (see
[variables](lab-files.md#variables)).

## Variables Parker sets

Inside a lab, for [`parker exec`](getting-in.md#shells-and-commands)
commands and scenario `exec` steps:

| Variable | Is |
|---|---|
| `CONSUL_HTTP_ADDR` | The node's Consul agent over HTTPS, `https://127.0.0.1:8501`, when TLS is on. |
| `CONSUL_HTTP_TOKEN` | The management token, when ACLs are on. |
| `CONSUL_CACERT` | The lab's CA, when TLS is on. |
| `VAULT_ADDR`, `VAULT_TOKEN` | On a [Vault](vault.md) server: its API, `http://127.0.0.1:8200` (`https://` with TLS), and the cluster's root token. |
| `VAULT_CACERT` | On a Vault server with TLS: the lab CA, so the `vault` CLI trusts the server. |
| `PARKER_VAULT_UNSEAL_KEY` | On a Vault server: the cluster's unseal key, once it's initialised. |
| `PARKER_IP` | The node's LAN address. |
| `PARKER_LAB`, `PARKER_NODE` | The lab and the node. |

Processes the lab runs (services, [low-level](low-level.md#processes)
processes) get `PATH`, `HOME` (the node's directory), `TMPDIR` (their
own `tmp/<process>` directory under it), `PARKER_LAB`, `PARKER_NODE` and
their own `env:`, but not your shell's environment.
