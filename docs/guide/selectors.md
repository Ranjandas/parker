---
title: Selectors
description: Pick nodes by datacenter, role, group, service, partition or raft role, for exec, faults, scenarios and --set.
---

Commands and scenario steps that act on nodes take a **selector**: a
short name that follows the lab file's tree. `dc1/web` means "the `web`
nodes in dc1", `dc1/leader` means "whichever server leads dc1 right now".

```console
$ parker exec lab.yaml dc1/leader -- consul operator raft list-peers
$ parker fault lab.yaml stop dc1/web
$ parker logs lab.yaml dc1/servers --level debug
```

## Every selector

| Selector | Matches |
|---|---|
| `dc1` | Every node of the datacenter. |
| `dc1/servers`, `dc1/clients`, `dc1/gateways` | A role. |
| `dc1/server-2`, `dc1/web-1` | One node. |
| `dc1/web` | A node group: every copy of `nodes.web` (`count: N`), in every partition. |
| `dc1/leader`, `dc1/followers` | Raft roles, asked of the running servers at that moment. |
| `svc:api`, `svc:dc2/api` | The nodes that run the service `api`. |
| `dc1/team-a/billing-1`, `dc1/team-a/billing` | A node or a group inside the admin partition `team-a`. |
| `part:team-a`, `part:dc1/team-a` | The client agents in a partition; `part:default` for the rest. |
| `ns:payments`, `ns:dc1/payments` | The nodes that register a service in the namespace `payments`. |
| `meta:zone=a`, `meta:dc1/zone=a` | The nodes whose [metadata](datacenters.md#node-metadata) has `zone` set to `a`. |
| `toolbox`, `dc1-server-1` | A node by its full name, including [low-level](low-level.md) nodes. |

## Where they're used

| Where | Takes |
|---|---|
| `parker exec`, `parker call`, `parker port-forward`, `parker describe` | A selector that matches **one** node. When it matches several, the command lists them. |
| `parker fault`, `parker fake`, `parker logs --level` | Any selector. |
| Scenario steps: `wait`, `exec`, faults, `upgrade`, `fake`, `log_level` | Any selector. |
| `--set SELECTOR:PATH=VALUE` | A datacenter, node or gateway, as `dc1:`, `dc1/web:`, `dc1/team-a/billing:`. See [`--set`](reuse.md#set). |

Partition faults take groups of selectors joined by commas:
`parker fault lab.yaml partition dc1/server-1,dc1/server-2 dc1/server-3`.
