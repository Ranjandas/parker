---
title: Reuse and variants
description: Keep lab files short with templates and modules, and make variants of a lab with overlays and --set instead of copies.
---

Repros come in families: the same lab with ACLs on, with five servers,
with an older Consul. Parker has four tools so you don't copy files:

| Tool | Use it for |
|---|---|
| [Variables](lab-files.md#variables) | Values you change often: `${vars.servers}`, `--var servers=5`. |
| [Templates](#templates) | One node shape, used in many places. |
| [Modules](#modules) | A reusable piece of a lab, with inputs, shared between files. |
| [Overlays and `--set`](#overlays) | Changing a lab from the command line without editing it. |

`parker expand --merged FILE` prints the file after all of them are
applied, so you can always see what you'll get.

## Templates

`consul.templates` holds node shapes. A node takes one with `from:`, and
its own fields win:

```yaml
consul:
  templates:
    api-node:
      count: 2
      service: { port: 9090, run: fake-service }
  datacenters:
    dc1:
      servers: 3
      nodes:
        api: { from: api-node }
    dc2:
      servers: 1
      nodes:
        api: { from: api-node, count: 1 }   # one copy here
```

A template takes any [node setting](nodes-and-services.md#node-settings).
Templates can build on other templates with `from:`, and nodes in
[partitions](enterprise.md#admin-partitions) can use them too.

## Modules

A module is a file that provides part of a lab, with typed inputs. It
starts with `kind: Module`:

```yaml
# modules/acl.yaml
parker: v1alpha1
kind: Module
description: ACLs on, with an odd number of servers in one datacenter
inputs:
  dc: { description: the datacenter to size, default: dc1 }
  servers: { type: number, enum: [1, 3, 5], default: 3 }
provides:
  consul:
    defaults: { acl: true }
    datacenters:
      ${inputs.dc}: { servers: ${inputs.servers} }
```

A lab file imports it under `imports:`, with values for its inputs:

```yaml
parker: v1alpha1
versions: { consul: 1.22.7 }
imports:
  - module: modules/acl.yaml          # relative to this file
    with: { dc: dc1, servers: 5 }
consul:
  datacenters:
    dc1:
      nodes:
        web: { service: { port: 8080, run: fake-service } }
```

- `provides:` can hold any part of a lab file.
- Imports merge in order, under the importing file: the importer's own
  values win.
- `${inputs.NAME}` reads an input. An input's `type` (`string`, `number`
  or `bool`), `enum` and `default` are checked, and an input without a
  `default` is required.
- Modules can import other modules, and overlays can import modules too.
- Mistakes in a module are reported at their line in the module file.

## Overlays

An overlay is a partial lab file merged over the base with `--overlay`.
Keep the variants of a repro as small overlays next to one base file:

```yaml
# overlays/acl.yaml
consul:
  defaults: { acl: true }
  datacenters:
    dc1: { servers: 5 }
```

```console
$ parker up lab.yaml --overlay overlays/acl.yaml
$ parker up lab.yaml --overlay overlays/acl.yaml --overlay overlays/old-consul.yaml
```

How overlays merge:

- Maps merge key by key; the overlay wins.
- `key: null` removes a key.
- Lists replace the base's list, unless the overlay's list starts with
  `{$patch: append}`:

```yaml
consul:
  datacenters:
    dc1:
      config_entries:
        - { $patch: append }
        - { Kind: service-defaults, Name: web, Protocol: http }
```

Overlays apply in the order given. A `vars:` block in an overlay sets
variables for every file, and `--var` beats them all. Unknown keys are
reported against the overlay that has them.

## `--set` {#set}

`--set PATH=VALUE` changes one value, after the overlays. PATH is
dot-separated from the top of the file, or starts at a
[selector](selectors.md) followed by `:`:

```console
$ parker up lab.yaml --set consul.datacenters.dc1.servers=5
$ parker up lab.yaml --set dc1:servers=5                 # the same, from the datacenter
$ parker up lab.yaml --set dc1/web:service.port=9000     # a node or gateway
$ parker up lab.yaml --set dc1/team-a/billing:count=3    # a node in a partition
$ parker up lab.yaml --set dc1/web:meta=null             # remove a key
```

VALUE is YAML, so `5`, `true`, `[a, b]` and `{port: 9000}` keep their
types.

Every command that reads a lab file takes `--overlay`, `--set` and
`--var`: `up`, `run`, `validate`, `expand`, `fetch` and the rest.

## Seeing the result

```console
$ parker expand --merged lab.yaml --overlay overlays/acl.yaml --set dc1:servers=3
```

prints the lab file with imports, templates, overlays and `--set`
applied and variables substituted. Plain `parker expand` goes one step
further, to the [expanded form](lab-files.md#the-expanded-form).

## YAML anchors

Plain YAML anchors work too, and are handy inside one file:

```yaml
partitions:
  team-a:
    service_defaults: &billing { run: fake-service, connect: sidecar, namespace: payments }
  team-b:
    service_defaults: *billing
```
