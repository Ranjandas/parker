---
title: Scenarios
description: Script a repro as steps that parker run plays and checks, so anyone can replay it with one command.
---

A scenario turns a repro into something anyone can replay: bring the lab
up, wait for it, do the things that trigger the issue, and check what
happens. Add it to the lab file under `scenario:`:

```yaml
parker: v1alpha1
name: scenario
versions: { consul: 1.22.7 }
consul:
  datacenters:
    dc1:
      servers: 3
      nodes:
        web:
          count: 2
          service: { port: 8080, run: fake-service }

scenario:
  steps:
    - wait: dc1 ready
    - exec: { on: dc1/leader, run: "consul operator raft list-peers" }
    - assert: { consul: { leader: true, peers: ">=3", members: ">=5" }, within: 10s }
    - name: web answers
      assert:
        exec: { on: dc1/web, run: "curl -sf http://127.0.0.1:8080/", output: web }
        within: 10s
    - snapshot: logs
```

```console
$ parker run scenario.yaml
...
[    0s] step 1: wait dc1
[    0s] step 2: exec on dc1/leader
...
[  1.2s] PASS: 5 steps
```

`parker run` brings the lab up fresh, as `parker up` does, then plays the
steps on the daemon, so the scenario keeps going if your terminal goes
away. It exits 1 when a step fails.

```console
$ parker run lab.yaml                  # the scenario: block
$ parker run lab.yaml leader-loss      # a named one
$ parker run --no-up lab.yaml          # against the lab that's already running
```

`parker run` also takes `up`'s `--keep-on-failure` and `--no-firewall`.
The [TUI](tui.md) (`:run`) and the [web UI](web-ui.md) play scenarios
too.

## Steps

Each step does one thing. Any step can also have a `name:`, shown in the
output, and an `at:` to time it.

| Step | Does |
|---|---|
| `wait: SELECTOR` | Wait until the nodes are ready: `wait: dc1 ready`, `wait: dc2/web`. |
| `sleep: 10s` | Wait a while. |
| `exec:` | Run a command on nodes. |
| `assert:` | Check a condition, retrying until `within:`. |
| `snapshot: logs` | Copy every log aside, to `<run dir>/snapshots/<time>/`. |
| [Faults](#faults) | `partition`, `heal`, `kill`, `stop`, `start`, `restart`, `pause`, `resume`. |
| `fake:` | Change [fake service](fake-service.md) faults. |
| `log_level:` | Change [log levels](log-levels.md). |
| `upgrade:` | A [rolling upgrade](upgrades.md). |

### exec

Runs a command on every node a selector matches. A string goes through
`sh -c`, so pipes and quotes work; a list runs as is. A non-zero exit
fails the step, unless `allow_failure: true`.

```yaml
- exec: { on: dc1/server-1, run: "consul intention create -deny web api" }
- exec: { on: dc1/web, run: "curl -s localhost:8080 | head -c 200", allow_failure: true }
```

The command runs as [`parker exec`](getting-in.md#shells-and-commands)
would, with the Consul address, token and CA set.

### assert

An assert checks a condition, and with `within:` retries it until it
holds or time runs out.

```yaml
- assert: { consul: { leader: true, peers: ">=3" }, within: 30s }
- assert:
    exec: { on: dc1/web, run: "curl -sf -m 2 http://127.0.0.1:9091/", output: api }
    within: 60s
- assert:
    call: { on: dc1/web, reaches: dc2/api, code: 200 }
    within: 60s
```

`consul:` checks the cluster through a datacenter's servers:

| Key | Holds when |
|---|---|
| `leader: true` | The datacenter has a raft leader. |
| `members: ">=5"` | That many LAN members are alive. |
| `peers: ">=3"` | That many voting raft peers. |
| `service_healthy: api` | At least one instance of the service passes its checks. |
| `dc: dc2` | Which datacenter; default the only one. |

`exec:` holds when the command (on `on:`, `run:`) exits 0, and its
output contains `output:` when that's set. Start the command with `!` to
check that something fails:

```yaml
- assert:
    exec: { on: dc1/web, run: "! curl -sf -m 2 http://127.0.0.1:9091/" }
    within: 30s
```

`call:` sends one request to a [fake service](fake-service.md), like
`parker call`, and checks the answer and the path it took:

| Key | Meaning |
|---|---|
| `on` | The node whose fake answers. |
| `service` | Which fake, when the node runs several. |
| `path`, `header` | What to request. |
| `code` | The status the answer must have; default 200. |
| `reaches` | A service the request must pass through: `api`, `dc2/api`, or a node name. |
| `body` | Text the answer must contain. |

### Timing with at

`at:` delays a step: `+30s` after the previous step finished, or
`T+30s` after the scenario started.

```yaml
- kill: dc1/leader
- at: +30s
  assert: { consul: { leader: true } }
```

### Faults

Fault steps are the same as [`parker fault`](faults.md):

```yaml
- partition: [[dc1/server-1, dc1/server-2], [dc1/server-3]]   # or [dc1, dc2]
- partition: [dc1/leader]          # one group: cut off from the rest
- heal: all                        # or a selector
- kill: dc1/leader
- stop: dc1/web
- start: dc1/servers
- restart: dc1/web
- pause: dc1/leader
- resume: dc1/servers
- fake: { on: dc1/api, errors: { rate: 0.5, code: 503 } }
- fake: { on: dc1/api, reset: true }
```

A `fake:` step takes `on`, `service`, `health`, `latency`, `errors`,
`rate_limit`, `limits` and `reset`, as in the [`fake:` block](fake-service.md#the-fake-block).

## When a step fails

The scenario stops at the first failing step, and `parker run` exits 1.
`on_failure:` says what happens to the lab:

| `on_failure` | Effect |
|---|---|
| `keep` (default) | Leave the lab up to look at. |
| `teardown` | Take it down. |

```yaml
scenario:
  on_failure: teardown
  steps: [...]
```

## Named scenarios

A lab can carry several scenarios under `scenarios:`, each played by
name:

```yaml
scenarios:
  leader-loss:
    steps:
      - wait: dc1 ready
      - kill: dc1/leader
      - assert: { consul: { leader: true }, within: 30s }
  split-brain:
    on_failure: keep
    steps:
      - wait: dc1 ready
      - partition: [[dc1/server-1], [dc1/server-2, dc1/server-3]]
      - assert: { consul: { leader: true }, within: 30s }
```

```console
$ parker run lab.yaml leader-loss
```

More examples are in the repository's
[examples](https://github.com/Ranjandas/parker/tree/main/examples)
folder: almost every example ends with a scenario that checks it works.
