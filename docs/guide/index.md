---
title: Introduction
description: What Parker is, what it's good at, and how this guide is organised.
---

Parker turns a few lines of YAML into a running Consul cluster in seconds.
You describe the shape you need (datacenters, servers, services, the mesh,
gateways, federation, peering, ACLs, TLS) and Parker brings it up with the
real Consul and Envoy binaries, on one Linux machine, without containers or
images.

```yaml
parker: v1alpha1
versions: { consul: 1.22.7 }
consul:
  datacenters:
    dc1: { servers: 3 }
```

```console
$ parker up minimal.yaml
lab "minimal" is up in 5.6s
$ parker exec minimal dc1/leader -- consul members
```

## What Parker is for

- **Reproducing an issue.** Write the setup from a bug report as a lab
  file, bring it up, and look. When you've found the trigger, add it to
  the file as a [scenario](scenarios.md) so anyone can replay it with one
  command.
- **Trying something out.** A new Consul version, a config entry, a
  failover policy, a peering setup: build it in seconds, break it, throw it
  away.
- **Learning and demos.** The [web UI](web-ui.md) draws the lab's topology
  and shows traffic and health live, and the built-in
  [fake service](fake-service.md) makes every request's path visible.

## How it works, briefly

Each node in a lab is a Linux network namespace: its own network stack,
addresses and routes, but sharing the host's kernel and filesystem.
Parker creates a bridge for each network, plugs the nodes into it, writes
each agent's configuration, and starts the processes in the right order,
waiting on readiness checks. A lab of a dozen agents starts in seconds and
costs little more memory than the agents themselves.

A background **daemon** supervises the processes after `parker up`
returns, restarts the ones that should restart, and serves the
[terminal UI](tui.md), the [web UI](web-ui.md), and remote clients. You
rarely start it yourself: the first command that needs it starts it.

## How this guide is organised

| Section | What's in it |
|---|---|
| **Getting started** | [Install Parker](install.md), take the [quick start](quick-start.md), and learn the [core concepts](concepts.md). |
| **Writing labs** | Everything a lab file can say, one topic per page: [datacenters](datacenters.md), [services](nodes-and-services.md), the [mesh](service-mesh.md), [gateways](gateways.md), [multiple datacenters](multiple-datacenters.md), [security](acl-and-tls.md), [Enterprise](enterprise.md) and more. |
| **Running labs** | Bring labs up and down, get a shell, read logs, and use the [terminal UI](tui.md), [web UI](web-ui.md) and [lab builder](lab-builder.md). |
| **Testing and repros** | Pick nodes with [selectors](selectors.md), [break things](faults.md), script repros as [scenarios](scenarios.md), and test [upgrades](upgrades.md). |
| **Reference** | Every [command](cli.md), every [lab file key](lab-file-reference.md), every [ACL token](acl-tokens.md), and [troubleshooting](troubleshooting.md). |

New to Parker? Start with the [quick start](quick-start.md). It takes about
ten minutes.
