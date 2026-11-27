---
title: Terminal UI
description: Browse labs, nodes and services, open shells, follow logs, inject faults and run scenarios from a k9s-style terminal UI.
---

`parker tui` is a live view of your labs in the terminal, in the style of
k9s. It works with a local or a [remote](remote.md) daemon.

```console
$ parker tui                       # every lab
$ parker tui examples/mesh.yaml    # straight to one lab
```

The main table lists the lab's nodes, grouped by datacenter, partition
and [Vault cluster](vault.md), servers first, with each process's state
and role, and a HEALTH column:

- A Consul agent's ROLE is its raft role (`server ★` leads), and its
  HEALTH its alive and known LAN members. A service shows its health in
  the catalog.
- A Vault server's ROLE is `active ★`, `standby`, `perf standby`,
  `sealed` or `uninitialized`, and its HEALTH the cluster's raft voters.

A lab with several datacenters, partitions or clusters opens on a list of
them (nodes, processes up, leader or active server, members or raft
voters, service health); `enter` opens one.

## Keys

| Key | Does |
|---|---|
| `enter` | Open the selected lab, datacenter or node. |
| `esc` | Back. |
| `/` | Filter rows. |
| `←` `→`, `space`, `z` | Fold and unfold a node, toggle it, fold or unfold all. |
| `s` | Open a shell on the node. |
| `l` | Follow a process's log: `f` follow, `w` wrap, `/` filter. |
| `d` | Describe the node. |
| `L` | Debug logs on the node (or the open log's process); again to reset. |
| `q` | Quit. |

Nodes fold on their own when the lab doesn't fit the screen, unless
something is wrong on them.

### Faults

On the selected node. The disruptive ones ask `y/n` first.

| Key | Does |
|---|---|
| `K` | Kill its processes. |
| `R` | Restart them. |
| `S` | Stop, or start again. |
| `P` | Pause, or resume. |
| `x` | Partition it from the rest of the lab. |
| `H` | Heal all partitions. |

## Commands

Type `:` for a command:

| Command | Does |
|---|---|
| `:labs`, `:lab NAME` | List labs, or open one. |
| `:dc [NAME[/PARTITION]]` | Jump to a datacenter or partition. |
| `:svc` | The catalog's services, with each instance's health and its sidecar's; `enter` goes to the node. |
| `:run [SCENARIO]` | Play a [scenario](scenarios.md) and watch its steps; `esc` stops it. |
| `:kill`, `:stop`, `:start`, `:restart`, `:pause`, `:resume` `[SELECTOR]` | A [fault](faults.md) on any selector. |
| `:partition GROUP...`, `:heal [SELECTOR]` | Cut or heal the network. |
| `:pf [LOCAL:]REMOTE` | Port-forward from the selected node while the TUI runs; `:pf` lists them, `:pf stop` stops them. |
| `:level LEVEL\|reset\|show [SELECTOR]` | Change [log levels](log-levels.md). |
| `:q` | Quit. |

`NO_COLOR` or `PARKER_TUI_STATIC=1` keeps the logo still.
