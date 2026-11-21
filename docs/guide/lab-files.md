---
title: Lab files
description: The shape of a lab file, versions, variables, starter files, validation, editor support and the expanded form.
---

A lab file is YAML. Most labs use only a few top-level keys: the schema
version, the versions to run, and the `consul:` tree.

```yaml
parker: v1alpha1                      # schema version, always v1alpha1 for now
name: payments-repro                  # optional; default: the file name
versions: { consul: 1.22.7, envoy: 1.35.12 }
vars: { servers: 3 }                  # optional
consul:
  defaults: { acl: true }             # settings every datacenter gets
  datacenters:
    dc1:
      servers: ${vars.servers}
      nodes:
        web: { service: { port: 8080, run: fake-service } }
scenario:                             # optional: steps for parker run
  steps:
    - wait: dc1 ready
```

| Key | What it's for | Page |
|---|---|---|
| `parker` | Schema version: `v1alpha1`. | |
| `name` | Lab name: lowercase letters, digits and `-`, at most 24 characters. Default: the file name. | [Concepts](concepts.md#labs) |
| `versions` | Binaries to run: `consul`, `envoy`, `consul-dataplane`. | [Concepts](concepts.md#versions-and-binaries) |
| `license` | Consul Enterprise license file. | [Enterprise](enterprise.md) |
| `vars` | Values to substitute with `${vars.NAME}`. | [below](#variables) |
| `imports` | Modules to merge in. | [Reuse](reuse.md#modules) |
| `consul` | Datacenters, nodes, services, federation, peering. | [Datacenters](datacenters.md) |
| `networks`, `nodes` | Low-level networks and nodes, for anything that isn't Consul. | [Low-level nodes](low-level.md) |
| `scenario`, `scenarios` | Steps `parker run` plays. | [Scenarios](scenarios.md) |

The [lab file reference](lab-file-reference.md) lists every key.

## Starter files

`parker init` writes a starter file. Pick a pattern with `--pattern`:

| Pattern | What you get |
|---|---|
| `minimal` (default) | One datacenter with three servers. |
| `clients` | Three servers, two nodes running the fake service, and one bare client. |
| `raw` | A low-level node running a process, no Consul. |

```console
$ parker init                       # writes parker.yaml
$ parker init repro.yaml --pattern clients
$ parker init repro.yaml --force    # overwrite
```

The [lab builder](lab-builder.md) in the web UI writes lab files too,
from a canvas.

## Variables

`vars:` holds values to reuse. `${vars.NAME}` substitutes one anywhere in
the file, and `--var NAME=VALUE` overrides it from the command line.
`${env.NAME}` reads an environment variable.

```yaml
vars:
  consul: 1.22.7
  servers: 3
versions: { consul: "${vars.consul}" }
consul:
  datacenters:
    dc1: { servers: ${vars.servers} }
```

```console
$ parker up lab.yaml --var consul=1.21.5 --var servers=5
```

Every command that reads a file takes `--var`, along with `--overlay` and
`--set`, which change the file without editing it (see
[overlays](reuse.md#overlays)).

## Validation

`parker validate` checks a file without running anything. Every command
that reads a file runs the same checks first. Errors point at their line
and say what was probably meant:

```console
$ parker validate bad.yaml
error: bad.yaml:
  - line 9: consul.datacenters.dc1.nodes.web.servers: unknown key (servers belongs under consul.datacenters.<dc>)
  - line 10: consul.datacenters.dc1.nodes.web.service.prot: unknown key (did you mean "port"?)
$ parker validate examples/minimal.yaml
ok: 1 networks, 3 nodes
```

Validation also warns about combinations that won't work, such as an
Envoy version the pinned Consul doesn't support.

## Editor support

Files that start with this line get autocomplete, hover docs and inline
errors in any editor with
[yaml-language-server](https://github.com/redhat-developer/yaml-language-server)
(VS Code's YAML extension, Neovim, Helix, and others):

```yaml
# yaml-language-server: $schema=https://github.com/Ranjandas/parker/releases/latest/download/parker.schema.json
```

`parker init` writes it for you. To work offline, or to match the exact
version you run, save the schema next to your files:

```console
$ parker schema > parker.schema.json
$ parker init lab.yaml --schema ./parker.schema.json
```

## The expanded form

Parker turns the `consul:` tree into a low-level form before it runs
anything: networks, nodes, the files each node gets (such as
`consul.json`) and the processes each runs. `parker expand` prints it.
It's a good way to see exactly what Parker will do, and the output is
itself a valid lab file.

```console
$ parker expand examples/minimal.yaml            # the low-level form
$ parker expand --merged lab.yaml --overlay acl.yaml   # the file after overlays, imports and --set
$ parker describe minimal dc1/server-1           # one node of a running lab
```
