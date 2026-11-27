---
title: Lab builder
description: Draw a lab on a canvas in the web UI, or open an existing lab file, edit it visually, save it and run it.
---

The lab builder in the [web UI](web-ui.md) writes lab files from a
canvas. Open it from **Build** in the sidebar.

```console
$ parker ui
```

## Drawing a lab

- Drag datacenters, partitions, server groups, client and dataplane
  nodes, services, gateways and [Vault clusters](#vault-clusters) in
  from the palette, or start from a pattern: the `parker init`
  starters, mesh federation, peering, or Vault.
- Link a service to the one it calls by dragging from its dot. The
  upstream gets `dc:`, `peer:` or `partition:` from where the two sit.
- Link two datacenters to federate or peer them, and a gateway to the
  services it routes to.
- The inspector edits the selected item. Hover over or click the **i**
  icon beside a field to read what it does.

### Vault clusters

Drag a **Vault cluster** in (three servers on raft, initialised and
unsealed for you), or start from the **Vault** or **Vault as Connect
CA** pattern. Datacenters and Vault clusters share names, so a cluster
can't be called `dc1` while a datacenter is.

Link them, by dragging from a dot or with **Vault link**:

| Link | Makes |
|---|---|
| Datacenter to a Vault cluster | The cluster is the datacenter's [Connect CA](vault.md#vault-as-the-connect-ca). |
| Vault cluster to a datacenter | A choice: [register in](vault.md#registering-in-consul) it, keep the data in it ([Consul storage](vault.md#consul-storage)), or mint its ACL tokens ([Consul secrets engine](vault.md#consul-secrets-engine), which turns ACLs on). |
| Vault cluster to another one | [Transit auto-unseal](vault.md#transit-auto-unseal) with the other one. |

Each link shows as a dashed, labelled arc. A cluster's inspector sets
its servers, dev mode, TLS, storage and links, and its **Scenario**
buttons add `vault:` steps (seal, unseal, step down) and an
`assert: {vault: ...}` check.

The YAML next to the canvas reads like a hand-written file: shared
settings go in `consul.defaults`, and the builder writes the config
entries the links need (local mesh gateways, exports, gateway routes,
and intentions when ACLs are on). It's checked as you go, as
`parker validate` checks it, with problems pointing at their lines.

The builder keeps your work in the browser between visits.

## Editing an existing file

**Open…** takes a path on the machine serving the UI (such as
`~/labs/mesh.yaml`), or an upload. The file is then the source of
truth:

- The canvas shows what the file merges to. An edit on the canvas is
  written into the file's text in place, so its comments, anchors and
  key order stay put. An untouched file comes back byte for byte.
- Typing in the YAML pane updates the canvas.
- **Merged** shows the file with imports and templates applied and
  variables substituted; **Expanded** shows the low-level form
  `parker up` runs.
- What an [import](reuse.md#modules) adds is locked on the canvas: change
  it in the module. A node made from a [template](reuse.md#templates)
  says so, and a value set by a `${var}` is changed through the variable.

## Saving and running

- **Save…** shows the diff before writing the file, or downloads it.
- **Run** brings the lab up through the daemon, shows its progress, and
  opens its dashboard.
- **Down** takes it down.
