---
title: Install
description: Install Parker on Linux with one command, from a package, or by hand, and set up the daemon and shell completion.
---

Parker is a single static binary. Labs run on **Linux** (amd64 or arm64).
On a Mac, [`parker machine`](macos.md) runs them in a small Linux VM it
sets up for you, or Parker can drive labs on another Linux machine; see
[remote use](remote.md).

## Requirements

- A Linux machine or VM with a recent kernel.
- Either root (`sudo`), or unprivileged user namespaces for
  [rootless mode](rootless.md).
- Network access to `releases.hashicorp.com` the first time a lab pins a
  version, to download Consul, Envoy and consul-dataplane. Downloads are
  checked against HashiCorp's checksums and cached.

There's nothing else to install: no Docker, no Kubernetes, and no Consul.

## Install script

The quickest way. The script downloads the latest release for your OS and
CPU, checks it against the release's `SHA256SUMS`, and installs `parker`
into `/usr/local/bin` (with `sudo` if that directory isn't writable):

```console
$ curl -fsSL https://ranjandas.github.io/parker/install.sh | sh
$ parker version
```

Two environment variables change what it does:

| Variable | Default | Meaning |
|---|---|---|
| `PARKER_VERSION` | latest release | Install this version instead, e.g. `0.9.0`. |
| `PARKER_INSTALL_DIR` | `/usr/local/bin` | Install the binary here. |

```console
$ curl -fsSL https://ranjandas.github.io/parker/install.sh | PARKER_VERSION=0.9.0 PARKER_INSTALL_DIR=~/bin sh
```

## Packages

Each release also has `.deb` and `.rpm` packages for amd64 and arm64. They
install `/usr/bin/parker` and a systemd unit for the daemon. Download one
from the [releases page](https://github.com/Ranjandas/parker/releases), then:

```console
$ sudo apt install ./parker_<version>_linux_amd64.deb    # Debian, Ubuntu
$ sudo dnf install ./parker_<version>_linux_amd64.rpm    # Fedora, RHEL
```

## By hand

```console
$ V=0.9.0 ARCH=amd64        # or arm64
$ curl -LO https://github.com/Ranjandas/parker/releases/download/v$V/parker_${V}_linux_$ARCH.tar.gz
$ curl -LO https://github.com/Ranjandas/parker/releases/download/v$V/SHA256SUMS
$ sha256sum --ignore-missing -c SHA256SUMS
$ tar xzf parker_${V}_linux_$ARCH.tar.gz
$ sudo install -m 0755 parker /usr/local/bin/parker
```

For a Mac, download `parker_<version>_darwin_arm64.tar.gz` (or `amd64`)
the same way, with `shasum -a 256 -c` in place of `sha256sum -c`. If you
download it in a browser instead, clear macOS's quarantine flag before the
first run: `xattr -d com.apple.quarantine parker`. Then run labs on the
Mac with [`parker machine`](macos.md), or point it at a daemon on a Linux
host as described in [remote use](remote.md).

## Run the daemon as a service

You don't need to: `parker up` starts a daemon in the background when none
is running. Run it as a systemd service when you want it up after a
reboot, or when other machines connect to it.

The packages install the unit already. From a tarball, install the unit
that comes in the archive:

```console
$ sudo install -m 0644 contrib/systemd/parker.service /etc/systemd/system/
$ sudo systemctl daemon-reload
```

Then enable it. To serve [remote clients](remote.md), set the daemon's
flags in `/etc/default/parker` first:

```console
$ echo 'PARKER_DAEMON_ARGS="--listen :7443"' | sudo tee /etc/default/parker   # optional
$ sudo systemctl enable --now parker
```

Stopping the service stops only the daemon. Labs keep running, and
`parker down` cleans them up with or without it.

## Shell completion

```console
$ parker completion bash | sudo tee /etc/bash_completion.d/parker   # bash
$ parker completion zsh > "${fpath[1]}/_parker"                     # zsh
$ parker completion fish > ~/.config/fish/completions/parker.fish   # fish
```

## Upgrade and uninstall

Upgrade by running the install script again, or installing the newer
package. Labs started by an older version keep running; bring them up
again to use the new one.

To uninstall, take your labs down, stop the daemon and delete the binary:

```console
$ parker status                  # lists labs
$ parker down <lab> --purge      # for each one
$ parker daemon stop
$ sudo rm /usr/local/bin/parker
```

Parker keeps downloaded binaries in `~/.cache/parker` and lab files under
`/var/lib/parker` (root) or `~/.local/state/parker` (rootless); remove
those too for a clean slate.
