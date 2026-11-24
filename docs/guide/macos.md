---
title: Labs on macOS
description: Run labs on a Mac with parker machine, a small Linux VM with the Parker daemon inside, set up and connected with two commands.
---

Labs need Linux, because Parker builds them from Linux network
namespaces. On a Mac, `parker machine` gives you that Linux without any
setup: it runs a small Linux VM with Apple's Virtualization framework,
installs the Parker daemon in it, and points `parker` on your Mac at that
daemon. After that, every command works as it does on Linux: `up`,
`exec`, `logs`, `tui`, `ui` and the rest.

```console
$ parker machine init
$ parker machine start
$ parker up examples/minimal.yaml
```

There's no Lima, Docker, vfkit or QEMU to install, and no `PARKER_HOST`
to set.

## Requirements

- macOS 13 (Ventura) or later, on Apple silicon or Intel. It's been tested
  on Apple silicon.
- About 3 GB of disk to start: a 590 MB image download, kept in the cache,
  and the VM's disk. The disk is 30 GiB, but only what's used takes space.
- Parker [installed](install.md) on the Mac. The install script is the
  easiest way:

  ```console
  $ curl -fsSL https://ranjandas.github.io/parker/install.sh | sh
  ```

Apple only lets a program start VMs if it's signed with the
virtualization entitlement. Parker's macOS releases are signed with it
(ad-hoc: there's no Apple developer account behind it), so there's nothing
to set up. Two things to know:

- **A tarball downloaded in a browser** is quarantined, and macOS refuses
  to open `parker` from it. Clear the flag once:
  `xattr -d com.apple.quarantine parker`. The install script and `curl`
  downloads aren't affected.
- **A build from source** needs signing too. `make machine` builds
  `bin/parker` and signs it, using Go and the Xcode command line tools
  (`xcode-select --install`). With a plain `go build`, `parker machine
  start` fails on the missing entitlement.

## Create the machine

`parker machine init` downloads the Linux image (Ubuntu 24.04 LTS server
for your Mac's CPU), checks it against its pinned SHA-256 and prepares the
VM's disk. It doesn't start anything.

```console
$ parker machine init
downloading Ubuntu 24.04 (20261017, arm64): done (587 MB)
creating a 30 GiB disk
created parker machine in /Users/you/.parker/machine/default
next: parker machine start
```

The image is cached, so a later `init`, after `parker machine rm`, doesn't
download it again. Size the VM with flags:

```console
$ parker machine init --cpus 6 --memory 8192 --disk 60
```

| Flag | Default | Meaning |
|---|---|---|
| `--cpus` | the Mac's CPUs, at most 4 | Virtual CPUs. |
| `--memory` | `4096` | Memory in MiB. Two peered datacenters with partitions and gateways, 12 nodes, run in the default. |
| `--disk` | `30` | Disk size in GiB. It grows on demand on the Mac. |
| `--mount DIR` | none | Share a directory on the Mac with the VM. See [files on your Mac](#files-on-your-mac). |

To change any of these later, remove the machine and create it again.

## Start it

```console
$ parker machine start
booting parker machine (Ubuntu 24.04 (20261017, arm64); the first boot installs the daemon)
  still booting (10s)
parker machine is up at 192.168.64.2 in 14s (daemon parker 0.11.0 (commit 6257f7a, built 2026-11-23T02:03:05Z) linux/arm64 go1.27.4)
parker on this Mac now uses it: try `parker up examples/minimal.yaml`
```

The first boot sets up the VM and downloads the daemon, which takes 15 to
30 seconds. Later starts take about 6. The VM runs in the background, so
you can close the terminal.

While the machine is running, `parker` on your Mac uses its daemon
whenever no other daemon is chosen. `--host` or `PARKER_HOST` still win,
so you can point at a [remote daemon](remote.md) for one command.

```console
$ parker up examples/minimal.yaml
fetching consul 1.22.7 ...
network: 1 networks, 3 nodes
  started dc1-server-1/consul          pid 1292
  ...
lab "minimal" is up in 7.5s
$ parker exec minimal dc1/leader -- consul members
$ parker ui minimal
```

Consul, Envoy and the other binaries are downloaded inside the VM, for
Linux. Lab files are read on your Mac, so relative paths in them work as
usual. An [Enterprise](enterprise.md) license is read on your Mac too.

### Which Parker runs in the VM

On each start, the VM runs the Parker release that matches the `parker`
you started it with. If yours isn't a release, such as a build from
source, the VM runs the latest release from GitHub. When you upgrade
`parker` on your Mac, the VM upgrades itself on its next start. If GitHub
can't be reached, it keeps the version it has.

To run your own build of Parker in the VM, for working on Parker itself,
point `PARKER_MACHINE_BINARY` at a Linux binary. `make machine` builds
one next to `bin/parker`:

```console
$ PARKER_MACHINE_BINARY=bin/parker-linux parker machine start
```

## Reaching the lab

The VM gets an address on a network private to your Mac, such as
`192.168.64.2`, and your Mac reaches it directly. Lab nodes have addresses
inside the VM, such as `10.1.0.10`, which your Mac doesn't route to. Reach
them through Parker instead, as you would a [remote daemon](remote.md):

- **The web UI.** `parker ui` opens the dashboard, the topology, and
  every Consul and app UI in the lab.
- **Shells and commands.** `parker exec LAB NODE`.
- **One port on your Mac.** `parker port-forward LAB NODE 8500`, then
  `http://127.0.0.1:8500`.
- **Any node address.** `parker proxy`, then use it as a SOCKS proxy.

See [getting into a lab](getting-in.md) for each.

## Files on your Mac

The VM sees none of your files by default. Lab files don't need it,
because `parker up` sends the lab to the daemon.

Some labs point at files by path, such as your own TLS certificates or a
local build pinned as `path:`. The daemon reads those inside the VM. For
those, share the directory at `init`. It appears in the VM at the same
path, so the lab file's paths work unchanged:

```console
$ parker machine init --mount ~/repros/tls-certs --mount ~/src/consul/bin
```

## A shell in the VM

`parker machine ssh` logs you in as the VM's `parker` user, who can
`sudo`. Give it a command to run that instead:

```console
$ parker machine ssh
$ parker machine ssh -- sudo journalctl -u parker -n 20
```

You rarely need it: `parker exec` gets you into lab nodes, and
`parker logs` shows their logs.

## Stop and remove

```console
$ parker machine stop
stopped parker machine
$ parker machine rm
removed parker machine
```

`stop` shuts the VM down cleanly, and labs in it stop with it. After the
next `start`, bring them up again with `parker up`. `rm` deletes the VM
and its disk but keeps the downloaded image. It refuses while the machine
is running; `parker machine rm --force` stops it first.

## Where things live

| Path | Is |
|---|---|
| `~/.parker/machine/default/` | The machine: disk, settings, SSH key and logs. Set `PARKER_MACHINE_DIR` to use another directory. |
| `~/.parker/machine/default/machine.log` | What the VM process logged: starts, stops and errors. |
| `~/.parker/machine/default/console.log` | The VM's console: boot messages and the login prompt. |
| `~/Library/Caches/parker/images/` | Downloaded images. `PARKER_CACHE` moves it. |

## Limits

- One machine per Mac.
- Labs don't survive `parker machine stop` or a restart of your Mac.
  `parker up` brings them back in seconds.
- Lab node addresses aren't reachable from the Mac directly. Use the
  ways in [reaching the lab](#reaching-the-lab).
- If `start` stays at "still booting", see
  [troubleshooting](troubleshooting.md#parker-machine-doesnt-start).
- If the VM stops responding and commands fail with `input/output
  error`, see
  [troubleshooting](troubleshooting.md#parker-machine-stops-responding).
