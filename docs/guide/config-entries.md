---
title: Config entries
description: Apply Consul config entries when a lab starts, inline in YAML or from HCL and JSON files.
---

`config_entries:` on a datacenter lists Consul config entries to apply
once its servers (and ACLs) are ready. Each one is either written inline,
in Consul's own casing so you can paste it from Consul's docs, or read
from a file:

```yaml
dc1:
  servers: 1
  config_entries:
    - Kind: proxy-defaults
      Name: global
      Config:
        protocol: http
    - Kind: service-defaults
      Name: web
      Protocol: http
    - file: entries/web-intentions.hcl
  nodes:
    web:
      service: { port: 8080, run: fake-service }
```

```hcl
# entries/web-intentions.hcl
Kind = "service-intentions"
Name = "web"
Sources = [
  {
    Name   = "api"
    Action = "allow"
  },
  {
    Name   = "*"
    Action = "deny"
  },
]
```

- Entries are applied in the order they're listed, so put the ones
  others depend on (such as `proxy-defaults` or a service's protocol)
  first.
- `file:` takes HCL or JSON, relative to the lab file.
- `parker up` waits until they are all applied. When Consul rejects one,
  the error names the entry and its line in the lab file. The
  `config-entries` process on the datacenter's first server applies
  them, and its log (`parker logs LAB dc1-server-1/config-entries`) shows
  each one.
- [Gateways'](gateways.md) own entries (`entry:`) are applied after
  these.

## Sharing entries between datacenters

Each datacenter applies its own list. When several need the same entry,
a YAML anchor saves repeating it:

```yaml
datacenters:
  dc1:
    servers: 1
    config_entries:
      - &local-gateways { Kind: proxy-defaults, Name: global, MeshGateway: { Mode: local } }
  dc2:
    servers: 1
    config_entries:
      - *local-gateways
```

## In admin partitions

> **Enterprise:** a partition's own `config_entries:` are written into
> that partition, after the datacenter's. See
> [Enterprise](enterprise.md#admin-partitions).

## Changing entries while the lab runs

Use the `consul` CLI inside the lab. `parker exec` sets the address,
token and CA for it:

```console
$ parker exec lab.yaml dc1/server-1 -- consul config list -kind service-defaults
$ parker exec lab.yaml dc1/server-1 -- consul config write /path/to/entry.hcl
$ parker exec lab.yaml dc1/server-1 -- consul config delete -kind service-defaults -name web
```

Node shells share the host's filesystem, so `consul config write` reads
files from wherever you keep them.
