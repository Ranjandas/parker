# Self-hosted CI runner (Fedora)

CI runs on a GitHub Actions runner on our own Fedora machine instead of
GitHub's hosted Ubuntu VMs. This page sets one up from scratch and explains
each step.

## How it works, in one paragraph

The runner is a small program from GitHub (`actions/runner`). It makes an
**outbound** HTTPS connection to GitHub and long-polls for jobs: nothing
connects in, so no port is opened and firewalld needs no change. When a
workflow job asks for `runs-on: [self-hosted, linux, x64]`, GitHub hands it
to an idle runner with those labels. The runner checks out the code into
its work folder, runs the steps as its own Unix user, streams the logs
back, and waits for the next job. One runner runs **one job at a time**;
the others queue.

## Cost

Jobs on self-hosted runners use no GitHub Actions minutes. GitHub
announced a $0.002/minute charge for self-hosted runners in private repos
for March 2026, then postponed it and has not brought it back (as of
November 2026). If that changes, the
[billing docs](https://docs.github.com/en/billing/managing-billing-for-your-products/managing-billing-for-github-actions/about-billing-for-github-actions)
are where it will show. Two things still use the free storage quota:
uploaded artifacts (we only upload e2e logs on failure, kept 7 days) and
the Actions cache (`setup-go` caches Go modules).

## Security: private repos only

A runner executes whatever a workflow tells it to, on this machine. That is
fine while the repo is private, since only people with write access can
push branches or open PRs that run. **If the repo ever goes public, remove
this runner first**: anyone could fork it and open a PR whose workflow runs
their code on this box. GitHub's own guidance says the same.

We also give the runner its own Unix user with no sudo, so a job can't
read your home directory (SSH keys, the Consul license, browser profile)
or change the system.

## What CI needs from the machine

- **Unprivileged user namespaces.** The e2e labs run inside
  `unshare -Urnm`, where an unprivileged user gets `CAP_NET_ADMIN` over a
  private network stack. Fedora allows this by default (Ubuntu needs an
  AppArmor sysctl, which the workflow only runs on GitHub's VMs).
- **`iptables`** (Fedora's `iptables-nft`), used by the tproxy labs and the
  firewall test inside those namespaces.
- **`gcc`** for `go test -race` (the race detector needs cgo).
- **`git`, `make`, `curl`, `tar`**, plus `libicu` for the runner itself.
- **Disk:** about 5–10 GB free in the runner user's home: the runner
  (~1 GB with Go in its tool cache), the work folder, Go's build cache, and
  the Consul/Envoy binaries in `~/.cache/parker`, which stay between jobs.
- No root, no Docker or Podman, no open ports.

Go itself comes from `actions/setup-go`, which downloads the version in
`go.mod` into the runner's tool cache, so you don't install Go for it.

## Setup

Run these on the Fedora box as your normal user (the one with sudo).

### 1. Install what the jobs need

```console
$ sudo dnf install -y git gcc make iptables-nft iproute util-linux curl tar libicu
```

Quick check that user namespaces work for an unprivileged user (prints
`ok`):

```console
$ unshare -Urn sh -c 'ip link set lo up && echo ok'
```

### 2. Create a user for the runner

```console
$ sudo useradd --create-home --shell /bin/bash ghrunner
```

`ghrunner` gets no password and no sudo. Every job runs as this user, so a
job sees only `/home/ghrunner` and world-readable files.

### 3. Download the runner

Switch to that user (`-i` gives a login shell in its home directory):

```console
$ sudo -iu ghrunner
$ mkdir actions-runner && cd actions-runner
$ V=$(curl -s https://api.github.com/repos/actions/runner/releases/latest | sed -n 's/.*"tag_name": "v\([^"]*\)".*/\1/p')
$ echo "$V"
$ curl -fLo runner.tar.gz "https://github.com/actions/runner/releases/download/v$V/actions-runner-linux-x64-$V.tar.gz"
$ tar xzf runner.tar.gz && rm runner.tar.gz
```

The first `curl` asks GitHub for the newest runner version; the second
downloads it. (The repo's *Settings → Actions → Runners → New self-hosted
runner* page shows the same commands with the version filled in, plus a
checksum to verify.) The runner updates itself after this, so you only do
this once.

### 4. Register it with the repo

In the browser, open the repo's **Settings → Actions → Runners → New
self-hosted runner**, pick **Linux / x64**, and copy the token from the
`./config.sh` line. It is a one-time registration token that expires after
an hour. Then, still as `ghrunner`:

```console
$ ./config.sh --url https://github.com/Ranjandas/parker --token <TOKEN> --name fedora --unattended
```

This trades the short token for the runner's own long-lived credentials
(stored in `.credentials` and `.runner` in this folder) and registers it
with the default labels `self-hosted`, `Linux` and `X64`, which is exactly
what the workflows ask for. `--name` is how it shows up in the runners
list.

### 5. Try it in the foreground

```console
$ ./run.sh
```

Wait for `Listening for Jobs`. The runner now shows as **Idle** on the
Runners page. Press Ctrl-C to stop it, then `exit` to get back to your own
user.

### 6. Run it as a systemd service

So it starts at boot and restarts if it crashes. The runner ships a helper
that writes and enables a systemd unit for you:

```console
$ cd /home/ghrunner/actions-runner
$ sudo ./svc.sh install ghrunner
$ sudo ./svc.sh start
$ sudo ./svc.sh status
```

`install ghrunner` writes `/etc/systemd/system/actions.runner.<repo>.fedora.service`
with `User=ghrunner`, so the service runs as that user, not root. `start`
and `status` are thin wrappers around `systemctl`.

**If `status` shows `status=203/EXEC` / `Permission denied`**, SELinux is
blocking systemd from running a script that lives in a home directory. Mark
the service's entry script as a program and start again:

```console
$ sudo chcon -t bin_t /home/ghrunner/actions-runner/runsvc.sh
$ sudo ./svc.sh start && sudo ./svc.sh status
```

(`chcon` changes the file's SELinux label; `bin_t` is the label for
ordinary executables. The change survives reboots but not a full SELinux
relabel; `sudo semanage fcontext -a -t bin_t '/home/ghrunner/actions-runner/runsvc.sh' && sudo restorecon -v /home/ghrunner/actions-runner/runsvc.sh`
makes it permanent.)

### 7. Check it on GitHub

The Runners page shows `fedora` as **Idle**. Re-run a workflow (Actions →
the run → Re-run jobs) or push a commit, and the job log's first lines say
`Runner name: 'fedora'`.

## Day to day

```console
$ systemctl list-units 'actions.runner.*'                  # the unit's full name
$ journalctl -u 'actions.runner.*' -f                      # follow the runner's log
$ sudo systemctl stop|start|restart 'actions.runner.*'     # or sudo ./svc.sh stop|start
$ ls /home/ghrunner/actions-runner/_diag/                  # detailed runner and job logs
```

- **The machine has to be on.** Jobs wait in the queue while the runner is
  offline and start when it comes back; a job that waits more than 24 hours
  is cancelled. The nightly full run is at 03:00 Melbourne time.
- **Jobs run one after another.** A PR that touches core code runs all ~23
  labs in sequence, so a full run takes a while. For more parallelism,
  register a second runner in another folder (`~/actions-runner-2`, a
  different `--name`) and install its service the same way. The labs are
  isolated in their own namespaces, so two can run side by side.
- **Disk.** Check with `sudo du -sh /home/ghrunner`. Safe to clear:
  `~/.cache/go-build` (Go's build cache) and `~/.cache/parker` (downloaded
  Consul/Envoy, fetched again on demand).

## Troubleshooting

- **A cancelled job left processes behind.** The runner kills the job's
  processes at the end, but a lab's Consul or Envoy can survive inside its
  namespace. Since only CI runs as `ghrunner`, this is safe when no job is
  running: `sudo pkill -u ghrunner -x consul; sudo pkill -u ghrunner -x envoy`.
- **tproxy labs fail with `iptables ... Protocol not supported` or
  `Couldn't load target`.** A kernel module can't be loaded from inside a
  user namespace. Load them at boot:
  `printf '%s\n' nf_tables nft_compat nft_chain_nat nft_redir xt_REDIRECT xt_mark xt_multiport xt_comment | sudo tee /etc/modules-load.d/parker-ci.conf && sudo systemctl restart systemd-modules-load`.
- **`unshare: write failed /proc/self/uid_map`.** User namespaces are off:
  `sysctl user.max_user_namespaces` must be above 0.

## Removing it, or going back to GitHub's runners

To use GitHub-hosted runners again (say, when minutes reset), no code
change is needed: set the repository variable `CI_RUNS_ON` (Settings →
Secrets and variables → Actions → Variables) to `"ubuntu-24.04"`, quotes
included, since it's read as JSON. Delete the variable to come back.

To remove the runner:

```console
$ cd /home/ghrunner/actions-runner
$ sudo ./svc.sh stop && sudo ./svc.sh uninstall
$ sudo -u ghrunner ./config.sh remove --token <TOKEN>   # token from the Runners page, under Remove
$ sudo userdel -r ghrunner                              # deletes the user and its home
```
