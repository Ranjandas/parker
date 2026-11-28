// Lab builder model: what the canvas edits, and the YAML it writes.
// Plain JS with no DOM, so node can run it too (the Go tests generate the
// builder's fixtures with it; see webui/builder_test.go).
//
// The YAML is idiomatic, like the examples: settings every service shares
// go in consul.defaults.service_template, an upstream says dc: or peer: or
// partition: from where its two ends sit, and the config entries that
// make those calls work (proxy-defaults with local mesh gateways, exports,
// intentions under ACLs, gateway routes) are written for you.
"use strict";

(function (root) {
  // The newest release pins Parker knows (compat.Releases[0]); the daemon's
  // /ui/versions lists the rest.
  const VERSIONS = { consul: "2.1.0", envoy: "1.39.1", dataplane: "2.1.0", vault: "1.21.1" };
  const KINDS = ["mesh", "ingress", "terminating", "api"];
  // A gateway's default node name per kind (api gateways are often "edge").
  const GW_NAME = { mesh: "mesh", ingress: "ingress", terminating: "terminating", api: "edge" };
  const NAME_RE = /^[a-z0-9]+(-[a-z0-9]+)*$/;

  let seq = 0;
  const uid = (p) => p + Date.now().toString(36).slice(-4) + (++seq).toString(36);

  // ---------- the model ----------
  // lab: {name, consul, envoy, dataplane, mesh, tls, acl, fed, dcs, peerings}
  //   mesh: services get sidecars (service_template connect: sidecar)
  //   fed: "" | "wan" | "mesh-gateways"
  // dc: {id, name, servers, ui, parts: [{id, name}], nodes, gws, ces}
  //   ces: part id ("" the default) -> the user's own config entries, as
  //        written (Consul casing, or {file: path}); one with the Kind,
  //        Namespace and Name of an entry the canvas makes wins over it
  // node: {id, name, part, agent, count, svc}
  //   part: a part id, "" for the default partition
  //   agent: "client" | "dataplane"
  //   svc: null (a bare client) or {port, connect, ups: [{to, port}]},
  //        held: ports of upstreams the canvas doesn't show
  //        ns: its own namespace ("" for the default, or inherited)
  //        inhNs: the namespace a file's service_template gives it
  //        fake: the service's own fake: block, as the file writes it
  //     connect: "" (from service_template) | "none" | "sidecar" | "tproxy"
  //     to: the called node's id
  // gw: {id, name, kind, part, to: [node ids], ns}
  //   ns: the namespace a terminating, ingress or API gateway registers in
  // ext: {id, name, port, ns, in: [{dc, part}]}, an external service:
  //   one node outside Consul, registered in the catalog of each side in
  //   in (dc id, part id). Calls and gateways name it by id.
  // fail: {id, svc, dc, part, to: [{dc, part}], timeout}, a failover: a
  //   service-resolver in side (dc, part) sending the service svc (a node
  //   or ext id) to the same service in each side of to, in order.
  // scenario: null or the file's scenario: as is ({steps: [...], ...}).
  //   named: the file has scenarios: too (the builder edits scenario: only)
  // vault: {id, name, servers, dev, tls, storage, store, reg, transit, secrets}
  //   a Vault cluster. storage: "raft" | "file" | "consul" (store: the dc
  //   id that keeps its data); reg: dc id it registers in; transit: the
  //   vault id it auto-unseals with; secrets: dc id of its Consul secrets
  //   engine. dc.ca: the vault id that is the dc's Connect CA.
  //   repl: the primary's vault id when it's a replication secondary,
  //   replMode "performance" | "dr"; replFilter: its paths_filter, as
  //   the file has it (performance only; edited in the YAML).
  // peering: {id, name, acceptor, dialer, accPart, dialPart, mesh}
  //   acceptor, dialer: dc ids; accPart, dialPart: part ids ("" = default)
  //   name "" = dialer-acceptor
  // lab.fakeDef: the fake: block of consul.defaults.service_template,
  //   which every service starts from (field by field)
  function lab(name) {
    return { name: name || "lab", consul: VERSIONS.consul, envoy: VERSIONS.envoy, dataplane: VERSIONS.dataplane, vault: VERSIONS.vault, mesh: true, tls: false, acl: false, fed: "", dcs: [], peerings: [], exts: [], fails: [], vaults: [] };
  }
  function vault(name, servers) { return { id: uid("v"), name, servers: servers || 1, dev: false, tls: false, storage: "raft", store: "", reg: "", transit: "", secrets: "", repl: "", replMode: "", replFilter: null }; }
  const vaultById = (m, id) => (m.vaults || []).find((v) => v.id === id);
  function dc(name, servers) { return { id: uid("d"), name, servers: servers || 1, ui: false, parts: [], nodes: [], gws: [] }; }
  function node(name, port, opts) {
    const o = opts || {};
    return { id: uid("n"), name, part: o.part || "", agent: o.agent || "client", count: o.count || 1, svc: port ? { port, connect: o.connect || "", ups: [] } : null };
  }
  function gw(kind, name, part) { return { id: uid("g"), name: name || GW_NAME[kind], kind, part: part || "", to: [] }; }
  function ext(name, port) { return { id: uid("x"), name, port: port || 0, ns: "", in: [] }; }

  // ---------- lookups ----------
  function find(m, id) {
    for (const d of m.dcs) {
      if (d.id === id) return { t: "dc", dc: d };
      for (const p of d.parts) if (p.id === id) return { t: "part", dc: d, part: p };
      for (const n of d.nodes) if (n.id === id) return { t: "node", dc: d, node: n };
      for (const g of d.gws) if (g.id === id) return { t: "gw", dc: d, gw: g };
    }
    for (const p of m.peerings) if (p.id === id) return { t: "peering", peering: p };
    for (const x of m.exts || []) if (x.id === id) return { t: "ext", ext: x };
    for (const x of m.fails || []) if (x.id === id) return { t: "fail", fail: x };
    for (const v of m.vaults || []) if (v.id === id) return { t: "vault", vault: v };
    return null;
  }
  const dcById = (m, id) => m.dcs.find((d) => d.id === id);
  const partName = (d, pid) => (pid ? (d.parts.find((p) => p.id === pid) || { name: "default" }).name : "default");
  // sideName is a peering side as the YAML writes it: dc or dc/partition.
  const sideName = (m, dcId, pid) => { const d = dcById(m, dcId) || {}; return pid ? `${d.name}/${partName(d, pid)}` : d.name; };
  const peeringName = (m, p) => p.name || `${sideName(m, p.dialer, p.dialPart).replace("/", "-")}-${sideName(m, p.acceptor, p.accPart).replace("/", "-")}`;
  const peeringOf = (m, a, b) => m.peerings.find((p) => (p.acceptor === a && p.dialer === b) || (p.acceptor === b && p.dialer === a));
  // peerOf finds the peering between two sides (dc id and part id each).
  const peerOf = (m, a, ap, b, bp) => m.peerings.find((p) => {
    const acc = [p.acceptor, p.accPart || ""], dial = [p.dialer, p.dialPart || ""];
    const x = [a, ap || ""], y = [b, bp || ""];
    const eq = (u, v) => u[0] === v[0] && u[1] === v[1];
    return (eq(acc, x) && eq(dial, y)) || (eq(acc, y) && eq(dial, x));
  });
  // Namespaces: "" is the default one.
  const normNs = (x) => (x && x !== "default" ? String(x) : "");
  const nsOf = (n) => (n && n.svc ? normNs(n.svc.ns || n.svc.inhNs) : "");
  const gwNs = (g) => (g && g.kind !== "mesh" ? normNs(g.ns) : "");
  // fillInh gives every service the namespace its scope's service_template
  // set in an opened file (d.inh: part id -> namespace), new ones too.
  function fillInh(m) {
    for (const d of m.dcs) for (const n of d.nodes) if (n.svc) {
      const x = d.inh && d.inh[n.part || ""];
      if (x) n.svc.inhNs = x; else delete n.svc.inhNs;
    }
  }
  const usesNs = (m) => m.dcs.some((d) => d.nodes.some((n) => nsOf(n)) || d.gws.some((g) => gwNs(g))) || (m.exts || []).some((x) => extNs(m, x));
  const ent = (m) => m.dcs.some((d) => d.parts.length > 0) || usesNs(m);
  // Every name in a dc: nodes, gateways and the external services it
  // registers share one namespace (m is optional).
  const namesIn = (d, m) => new Set([...d.nodes.map((n) => n.name), ...d.gws.map((g) => g.name), ...(m ? m.exts || [] : []).filter((x) => x.in.some((r) => r.dc === d.id)).map((x) => x.name)]);

  // ---------- external services ----------
  // An external service's namespace: its own, else consul.defaults'.
  const extNs = (m, x) => normNs(x.ns || m.defNs);
  const regAt = (x, dcId, pid) => x.in.find((r) => r.dc === dcId && (r.part || "") === (pid || ""));
  // extNode is an external service as one side registers it, shaped like
  // a node so calls and gateways treat it as one: {td, t}.
  function extNode(m, x, r) {
    const td = dcById(m, r.dc);
    return td && { td, t: { id: x.id, name: x.name, part: r.part || "", ext: x, svc: { port: x.port, ns: extNs(m, x), connect: "none", ups: [] } } };
  }
  // extFor picks where a caller in dc sd, partition part reaches x: its
  // own partition, else its datacenter, else a peered side, else the first.
  function extFor(m, x, sd, part) {
    const r = regAt(x, sd.id, part) || x.in.find((y) => y.dc === sd.id) || x.in.find((y) => peerOf(m, sd.id, part, y.dc, y.part)) || x.in[0];
    return r ? extNode(m, x, r) : null;
  }
  // targetsOf lists the services a gateway routes to, nodes and external
  // services alike (as the gateway's side registers them).
  function targetsOf(m, d, g) {
    const out = [];
    for (const id of g.to) {
      const f = find(m, id);
      if (f && f.t === "node" && f.node.svc) out.push(f.node);
      else if (f && f.t === "ext") { const r = regAt(f.ext, d.id, g.part); const e = r && extNode(m, f.ext, r); if (e) out.push(e.t); }
    }
    return out;
  }
  // register adds a side to x's in, or throws why it can't.
  function register(m, x, d, pid) {
    if (regAt(x, d.id, pid)) throw new Error(`${x.name} is registered in ${sideName(m, d.id, pid)} already.`);
    if ([...d.nodes, ...d.gws].some((y) => (y.part || "") === (pid || "") && y.name === x.name)) throw new Error(`${sideName(m, d.id, pid)} has a ${x.name} already, and Consul node names are unique in a partition.`);
    x.in.push({ dc: d.id, part: pid || "" });
    return `${x.name} is registered in ${sideName(m, d.id, pid)}'s catalog, on an external node Parker health-checks.`;
  }

  // How an upstream from node src (in dc sd) reaches node t (in dc td).
  function route(m, sd, src, td, t) {
    if (sd.id !== td.id) {
      const p = peerOf(m, sd.id, src.part, td.id, t.part);
      if (p) return { kind: "peer", peer: peeringName(m, p), peering: p };
      // Peers belong to a partition: a peering of other partitions of
      // these dcs doesn't carry this call.
      const parts = sd.parts.length || td.parts.length;
      return { kind: m.fed && !parts ? "dc" : "none", dc: td.name, peerParts: !!parts };
    }
    if ((src.part || "") !== (t.part || "")) return { kind: "partition", partition: partName(td, t.part) };
    return { kind: "local" };
  }

  // ---------- failover ----------
  // svcAt is service id (a node or an ext) as side (dcId, part) has it:
  // {td, t}, or null when that side has no such service.
  function svcAt(m, id, dcId, part) {
    const f = find(m, id), d = dcById(m, dcId);
    if (!f || !d) return null;
    if (f.t === "ext") { const r = regAt(f.ext, dcId, part); return r ? extNode(m, f.ext, r) : null; }
    if (f.t !== "node" || !f.node.svc) return null;
    const ns = nsOf(f.node);
    const t = d.nodes.find((n) => n.svc && n.name === f.node.name && (n.part || "") === (part || "") && nsOf(n) === ns);
    return t ? { td: d, t } : null;
  }
  // foSides lists where service id could fail over to from side (dc, part):
  // every other side that has a service of that name.
  function foSides(m, id, dcId, part) {
    const out = [];
    for (const d of m.dcs) for (const p of [{ id: "" }, ...d.parts]) {
      if (d.id === dcId && p.id === (part || "")) continue;
      if (svcAt(m, id, d.id, p.id)) out.push({ dc: d.id, part: p.id });
    }
    return out;
  }
  // foTarget is one failover target as the resolver writes it, or null
  // when nothing joins the two sides.
  function foTarget(m, fo, to) {
    const src = svcAt(m, fo.svc, fo.dc, fo.part), dst = svcAt(m, fo.svc, to.dc, to.part);
    if (!src || !dst) return null;
    const ns = nsOf(dst.t) !== nsOf(src.t) || (nsOf(dst.t) && to.dc !== fo.dc) ? { Namespace: nsOf(dst.t) || "default" } : {};
    if (to.dc === fo.dc) return Object.assign({ Partition: partName(dst.td, to.part) }, ns);
    const p = peerOf(m, fo.dc, fo.part, to.dc, to.part);
    // Consul takes Peer with Namespace, never with Partition.
    if (p) return Object.assign({ Peer: peeringName(m, p) }, ns);
    const sd = dcById(m, fo.dc);
    if (m.fed && !sd.parts.length && !dst.td.parts.length) return Object.assign({ Datacenter: dst.td.name }, ns);
    return null;
  }
  // failover makes a failover from service id (a node, or an ext as side
  // dc/part registers it) to the same service in side (toDc, toPart).
  function failover(m, id, dcId, part, toDc, toPart) {
    part = part || ""; toPart = toPart || "";
    const src = svcAt(m, id, dcId, part), dst = svcAt(m, id, toDc, toPart);
    if (!src) throw new Error("Nothing to fail over from there.");
    if (!dst) throw new Error(`${sideName(m, toDc, toPart)} has no ${src.t.name} to fail over to.`);
    let fo = m.fails.find((x) => x.svc === id && x.dc === dcId && (x.part || "") === part);
    if (fo && fo.to.some((t) => t.dc === toDc && (t.part || "") === toPart)) throw new Error(`${src.t.name} in ${sideName(m, dcId, part)} fails over to ${sideName(m, toDc, toPart)} already.`);
    if (!fo) { fo = { id: uid("f"), svc: id, dc: dcId, part, to: [], timeout: "" }; m.fails.push(fo); }
    fo.to.push({ dc: toDc, part: toPart });
    const ok = foTarget(m, fo, { dc: toDc, part: toPart });
    return `${src.t.name} in ${sideName(m, dcId, part)} fails over to ${sideName(m, toDc, toPart)} (a service-resolver), which exports it and allows its callers.` +
      (ok ? "" : ` ${joinHint(m, dcById(m, dcId), part, dcById(m, toDc), toPart)}`);
  }
  // pruneFails drops failovers (and targets) whose service is gone from
  // their side.
  function pruneFails(m) {
    m.fails = (m.fails || []).filter((fo) => svcAt(m, fo.svc, fo.dc, fo.part));
    for (const fo of m.fails) fo.to = fo.to.filter((t) => svcAt(m, fo.svc, t.dc, t.part));
    m.fails = m.fails.filter((fo) => fo.to.length);
  }
  // foCalls are the calls a failover may carry: each call to the service
  // in the failover's side, again to each target. They bring the
  // exports, intentions and mesh gateway mode a call there would.
  function foCalls(m, base) {
    const out = [];
    for (const fo of m.fails || []) {
      const at = svcAt(m, fo.svc, fo.dc, fo.part);
      if (!at) continue;
      const callers = base.filter((c) => c.td.id === fo.dc && (c.t.part || "") === (fo.part || "") && c.t.name === at.t.name && nsOf(c.t) === nsOf(at.t));
      for (const to of fo.to) {
        const dst = svcAt(m, fo.svc, to.dc, to.part);
        if (!dst || !foTarget(m, fo, to)) continue;
        for (const c of callers) out.push({ sd: c.sd, src: c.src, td: dst.td, t: dst.t, port: c.port, r: route(m, c.sd, c.src, dst.td, dst.t), fo });
      }
    }
    return out;
  }

  // Every call in the lab, resolved: [{sd, src, td, t, port, r}]. With
  // fo, the calls failovers may carry too (marked fo).
  function calls(m, withFo) {
    fillInh(m);
    const out = [];
    for (const sd of m.dcs) for (const src of sd.nodes) for (const u of (src.svc && src.svc.ups) || []) {
      const f = find(m, u.to);
      if (f && f.t === "node" && f.node.svc) out.push({ sd, src, td: f.dc, t: f.node, port: u.port, r: route(m, sd, src, f.dc, f.node) });
      else if (f && f.t === "ext") { const e = extFor(m, f.ext, sd, src.part); if (e) out.push({ sd, src, td: e.td, t: e.t, port: u.port, r: route(m, sd, src, e.td, e.t) }); }
    }
    return withFo ? out.concat(foCalls(m, out)) : out;
  }

  // The connect mode a node's service ends up with.
  function connectOf(m, n) {
    if (!n.svc) return "none";
    if (n.svc.connect) return n.svc.connect;
    return inherited(m, n);
  }
  // What a service gets when it says no connect: of its own. A file can
  // set it for a dc or partition (n.svc.inh); else the lab's mesh setting.
  const inherited = (m, n) => (n.svc && n.svc.inh) || (m.mesh ? "sidecar" : "none");

  // ---------- edits ----------
  // uniqueName picks base, base-2, ... not taken in dc d.
  function uniqueName(d, base, m) {
    const used = namesIn(d, m);
    if (!used.has(base)) return base;
    for (let i = 2; ; i++) if (!used.has(`${base}-${i}`)) return `${base}-${i}`;
  }
  function nextDCName(m) {
    const used = new Set(m.dcs.map((d) => d.name));
    for (let i = 1; ; i++) if (!used.has("dc" + i)) return "dc" + i;
  }
  function nextPort(m, from) {
    const used = new Set();
    m.dcs.forEach((d) => d.nodes.forEach((n) => { if (n.svc) { used.add(n.svc.port); n.svc.ups.forEach((u) => used.add(u.port)); (n.svc.held || []).forEach((p) => used.add(p)); } }));
    (m.exts || []).forEach((x) => used.add(x.port));
    for (let p = from; ; p++) if (!used.has(p)) return p;
  }
  function nextUpPort(n) {
    const used = new Set([n.svc.port, ...n.svc.ups.map((u) => u.port), ...(n.svc.held || [])]);
    for (let p = 9091; ; p++) if (!used.has(p)) return p;
  }

  // ---------- versions ----------
  // vt is /ui/versions: {releases: [{consul, envoy, dataplane}], envoy:
  // {"2.1": ["1.39", ...]}}.
  const minorOf = (v) => { const x = String(v || "").replace(/^v/, "").split(/[+-]/)[0].split("."); return x.length > 1 && /^\d+$/.test(x[0]) && /^\d+$/.test(x[1]) ? `${x[0]}.${x[1]}` : ""; };
  // envoyNote says when the lab's Envoy isn't one its Consul supports.
  function envoyNote(m, vt) {
    const sup = vt && vt.envoy && vt.envoy[minorOf(m.consul)];
    if (!sup || !minorOf(m.envoy) || sup.includes(minorOf(m.envoy))) return "";
    return `Consul ${minorOf(m.consul)} supports Envoy ${sup.join(", ")}, not ${minorOf(m.envoy)}.`;
  }
  // setConsul pins Consul v and, when the Envoy pin no longer fits, the
  // Envoy and consul-dataplane paired with that Consul minor.
  function setConsul(m, v, vt) {
    m.consul = v;
    if (!vt || !envoyNote(m, vt)) return;
    const r = (vt.releases || []).find((x) => minorOf(x.consul) === minorOf(v));
    if (r) { m.envoy = r.envoy; if (r.dataplane) m.dataplane = r.dataplane; }
  }

  // link joins two things on the canvas and says what it made, or throws
  // with why it can't.
  function link(m, fromId, toId) {
    const a = find(m, fromId), b = find(m, toId);
    if (!a || !b || fromId === toId) throw new Error("Pick two different things to link.");
    if (a.t === "vault" || b.t === "vault") return linkVault(m, a, b);
    if (a.t === "ext" && b.t === "ext") throw new Error("External services don't call each other through Consul.");
    // An external service with a datacenter or partition: it registers there.
    if ((a.t === "ext" && (b.t === "dc" || b.t === "part")) || (b.t === "ext" && (a.t === "dc" || a.t === "part"))) {
      const [x, s] = a.t === "ext" ? [a.ext, b] : [b.ext, a];
      return register(m, x, s.dc, s.t === "part" ? s.part.id : "");
    }
    if (a.t === "ext") throw new Error(`${a.ext.name} is outside Consul and calls nothing through it. Link a service or a terminating gateway to ${a.ext.name} instead.`);
    if (a.t === "node" && b.t === "ext") {
      const x = b.ext;
      if (!a.node.svc) throw new Error(`${a.node.name} runs no service, so it can't call anything. Give it a service first.`);
      if (connectOf(m, a.node) === "none") throw new Error(`${a.node.name} is outside the mesh (connect: none), and upstreams go through a sidecar. Set its Connect to sidecar first.`);
      if (a.node.svc.ups.some((u) => u.to === x.id)) throw new Error(`${a.node.name} already calls ${x.name}.`);
      if (a.node.svc.ups.some((u) => { const f = find(m, u.to); return f && (f.node || f.ext).name === x.name; })) throw new Error(`${a.node.name} already calls a service named ${x.name}.`);
      let note = "";
      if (!x.in.length) note = " " + register(m, x, a.dc, a.node.part);
      const u = { to: x.id, port: nextUpPort(a.node) };
      a.node.svc.ups.push(u);
      const e = extFor(m, x, a.dc, a.node.part);
      const r = route(m, a.dc, a.node, e.td, e.t);
      const how = { local: "", partition: ` in partition ${r.partition}`, dc: ` in ${e.td.name}`, none: ` in ${e.td.name}`, peer: ` over the peering ${r.peer}` }[r.kind];
      const tgw = e.td.gws.some((g) => g.kind === "terminating" && (g.part || "") === e.t.part && g.to.includes(x.id));
      return `${a.node.name} calls ${x.name}${how} on 127.0.0.1:${u.port}.${note}` + (tgw ? "" : ` Link a terminating gateway in ${sideName(m, e.td.id, e.t.part)} to ${x.name} so the mesh can reach it.`);
    }
    if (a.t === "gw" && b.t === "ext") {
      const x = b.ext;
      if (a.gw.kind !== "terminating") throw new Error(`Only a terminating gateway reaches an external service; the ${a.gw.kind} gateway routes to services in the mesh.`);
      if (a.gw.to.includes(x.id)) throw new Error(`${a.gw.name} already routes to ${x.name}.`);
      const note = regAt(x, a.dc.id, a.gw.part) ? "" : " " + register(m, x, a.dc, a.gw.part);
      a.gw.to.push(x.id);
      return `${a.gw.name} fronts ${x.name}. Services reach it with an upstream to ${x.name}.${note}`;
    }
    // A service to the same service elsewhere: it fails over there.
    if (a.t === "node" && b.t === "node" && a.node.svc && b.node.svc && a.node.name === b.node.name && nsOf(a.node) === nsOf(b.node)) {
      return failover(m, a.node.id, a.dc.id, a.node.part, b.dc.id, b.node.part);
    }
    if (a.t === "node" && b.t === "node") {
      if (!a.node.svc) throw new Error(`${a.node.name} runs no service, so it can't call anything. Give it a service first.`);
      if (!b.node.svc) throw new Error(`${b.node.name} runs no service to call.`);
      if (connectOf(m, a.node) === "none") throw new Error(`${a.node.name} is outside the mesh (connect: none), and upstreams go through a sidecar. Set its Connect to sidecar first.`);
      if (a.node.svc.ups.some((u) => u.to === b.node.id)) throw new Error(`${a.node.name} already calls ${b.node.name}.`);
      if (a.node.svc.ups.some((u) => { const f = find(m, u.to); return f && f.node && f.node.name === b.node.name; })) throw new Error(`${a.node.name} already calls a service named ${b.node.name}.`);
      const u = { to: b.node.id, port: nextUpPort(a.node) };
      a.node.svc.ups.push(u);
      const r = route(m, a.dc, a.node, b.dc, b.node);
      const how = { local: "", partition: ` in partition ${r.partition}`, dc: ` in ${b.dc.name}`, none: ` in ${b.dc.name}`, peer: ` over the peering ${r.peer}` }[r.kind];
      return `${a.node.name} calls ${b.node.name}${how} on 127.0.0.1:${u.port}.` + (r.kind === "none" ? ` ${joinHint(m, a.dc, a.node.part, b.dc, b.node.part)}` : "");
    }
    if (a.t === "gw" && b.t === "node") {
      if (a.gw.kind === "mesh") throw new Error("Mesh gateways carry traffic between datacenters and partitions; link the services themselves.");
      if (a.dc.id !== b.dc.id || (a.gw.part || "") !== (b.node.part || "")) throw new Error(`The ${a.gw.kind} gateway can only reach services in its own datacenter and partition.`);
      if (!b.node.svc) throw new Error(`${b.node.name} runs no service.`);
      if (a.gw.to.includes(b.node.id)) throw new Error(`${a.gw.name} already routes to ${b.node.name}.`);
      a.gw.to.push(b.node.id);
      if (a.gw.kind === "terminating") {
        b.node.svc.connect = "none";
        return `${a.gw.name} fronts ${b.node.name}, now outside the mesh. Services reach it with an upstream to ${b.node.name}.`;
      }
      return `${a.gw.name} routes to ${b.node.name}.`;
    }
    const da = a.t === "dc" ? a.dc : null, db = b.t === "dc" ? b.dc : null;
    if (da && db) {
      if (peerOf(m, da.id, "", db.id, "")) throw new Error(`${da.name} and ${db.name} are peered already.`);
      return "pick"; // the canvas asks: federation or peering
    }
    // A partition with a partition (or a datacenter's default partition)
    // in another datacenter: they peer.
    if ((a.t === "dc" || a.t === "part") && (b.t === "dc" || b.t === "part")) {
      const ap = a.t === "part" ? a.part.id : "", bp = b.t === "part" ? b.part.id : "";
      if (a.dc.id === b.dc.id) throw new Error(`Consul can't peer two partitions of one datacenter. Link their services instead: the builder exports them to the other partition.`);
      if (peerOf(m, a.dc.id, ap, b.dc.id, bp)) throw new Error(`${sideName(m, a.dc.id, ap)} and ${sideName(m, b.dc.id, bp)} are peered already.`);
      return joinDCs(m, a.t === "part" ? a.part.id : a.dc.id, b.t === "part" ? b.part.id : b.dc.id, "peering");
    }
    throw new Error("Link a service to a service it calls, a gateway to a service, an external service to where it registers, or two datacenters (or partitions in two datacenters).");
  }

  // linkVault links a Vault cluster: to another one, it asks how
  // ("rpick": auto-unseal or replication); a datacenter to a cluster
  // makes it the dc's Connect CA; a cluster to a datacenter asks how it
  // uses it ("vpick").
  function linkVault(m, a, b) {
    const dcOf = (x) => (x.t === "dc" || x.t === "part" ? x.dc : null);
    if (a.t === "vault" && b.t === "vault") return "rpick";
    if (dcOf(a) && b.t === "vault") return joinVault(m, b.vault.id, dcOf(a).id, "ca");
    if (a.t === "vault" && dcOf(b)) return "vpick";
    throw new Error("Link a Vault cluster to another one (auto-unseal or replication), to a datacenter (Consul storage, registration or the Consul secrets engine), or a datacenter to a Vault cluster (its Connect CA).");
  }
  const REPL_NAME = { performance: "performance", dr: "DR" };
  // replRole is a cluster's part in replication: "" or e.g. "DR
  // secondary of east", "perf + DR primary".
  function replRole(m, v) {
    const p = v.repl && vaultById(m, v.repl);
    if (p) return `${v.replMode === "dr" ? "DR" : "perf"} secondary of ${p.name}`;
    const modes = ["performance", "dr"].filter((md) => (m.vaults || []).some((x) => x.repl === v.id && x.replMode === md));
    return modes.length ? `${modes.map((md) => (md === "dr" ? "DR" : "perf")).join(" + ")} primary` : "";
  }
  // vaultEnt makes the lab's Vault Enterprise, which replication needs.
  const entVault = (m) => /\+(.*\.)?ent(\.|$)/.test(m.vault || "");
  // joinVaults links cluster a to cluster b: how is transit (a
  // auto-unseals with b), performance or dr (a replicates b, its
  // primary), or "" (a stops replicating).
  function joinVaults(m, aId, bId, how) {
    const v = vaultById(m, aId), t = vaultById(m, bId);
    if (!v || (how && !t)) throw new Error("Pick two Vault clusters.");
    if (!how) { v.repl = ""; v.replMode = ""; v.replFilter = null; return `${v.name} doesn't replicate any more.`; }
    if (v === t) throw new Error("Link a Vault cluster to another one.");
    const secOf = (x) => x.repl && vaultById(m, x.repl);
    if (how === "transit") {
      if (t.transit === v.id) throw new Error(`${t.name} auto-unseals with ${v.name}, so ${v.name} can't auto-unseal with ${t.name}.`);
      if (secOf(v)) throw new Error(`${v.name} is a replication secondary; Parker unseals secondaries with the primary's keys, not transit.`);
      if (secOf(t)) throw new Error(`${t.name} is a replication secondary, whose transit keys activating it would wipe; auto-unseal with ${secOf(t).name}.`);
      v.transit = t.id;
      return `${v.name} auto-unseals with a key in ${t.name}'s Transit secrets engine.`;
    }
    if (how !== "performance" && how !== "dr") throw new Error("unknown link " + how);
    const R = REPL_NAME[how];
    if (v.dev || t.dev) throw new Error(`${(v.dev ? v : t).name} runs in dev mode; replication needs a cluster that keeps its data.`);
    if (secOf(t)) throw new Error(`${t.name} is a secondary of ${secOf(t).name}; Parker doesn't chain replication, so link ${v.name} to ${secOf(t).name}.`);
    if ((m.vaults || []).some((x) => x.repl === v.id)) throw new Error(`${v.name} is a primary; Parker doesn't chain replication.`);
    const other = (m.vaults || []).find((x) => x !== v && x.replMode === how && x.repl && x.repl !== t.id);
    if (other) throw new Error(`The lab's ${R} primary is ${vaultById(m, other.repl).name}; ${R} replication has one primary.`);
    // Activation replaces the secondary's data with the primary's.
    const ca = m.dcs.find((d) => d.ca === v.id);
    if (ca) throw new Error(`${v.name} is ${ca.name}'s Connect CA, whose keys activating it as a secondary would wipe; use ${t.name}.`);
    const user = (m.vaults || []).find((x) => x.transit === v.id);
    if (user) throw new Error(`${user.name} auto-unseals with ${v.name}, whose keys activating it as a secondary would wipe.`);
    if (v.transit) throw new Error(`${v.name} auto-unseals with transit; Parker unseals secondaries with the primary's keys. Turn auto-unseal off first.`);
    if (v.secrets) throw new Error(`${v.name} runs the Consul secrets engine, which activating it as a secondary would wipe; set it on ${t.name}.`);
    if (how === "dr" || v.replMode !== "performance") v.replFilter = null;
    v.repl = t.id; v.replMode = how;
    let note = "";
    if (!entVault(m)) { m.vault = (m.vault || VERSIONS.vault).replace(/\+.*$/, "") + "+ent"; note = " Replication is Vault Enterprise, so that set versions.vault to +ent: it needs a license (VAULT_LICENSE_PATH)."; }
    return how === "dr"
      ? `${v.name} keeps a DR copy of ${t.name}, tokens and leases too, and serves nothing until promoted.${note}`
      : `${v.name} is a performance secondary of ${t.name}: it serves reads and keeps its own tokens.${note}`;
  }
  // joinVault wires a Vault cluster to a datacenter: how is ca (the dc's
  // Connect CA), store (Consul storage), reg (registers in the dc) or
  // secrets (the Consul secrets engine).
  function joinVault(m, vId, dcId, how) {
    const v = vaultById(m, vId), d = dcById(m, dcId);
    if (!v || !d) throw new Error("Pick a Vault cluster and a datacenter.");
    if (how === "ca") {
      if (v.storage === "consul" && v.store === d.id) throw new Error(`${v.name} keeps its data in ${d.name}, so it can't be ${d.name}'s Connect CA too: each would wait for the other.`);
      if (v.repl) throw new Error(`${v.name} is a replication secondary, whose keys activation wiped; make its primary ${d.name}'s Connect CA.`);
      d.ca = v.id; return `${d.name}'s Connect CA is now ${v.name}, through Vault's PKI secrets engine.`; }
    if (how === "store") {
      if (d.ca === v.id) throw new Error(`${v.name} is ${d.name}'s Connect CA, so it can't keep its data in ${d.name} too: each would wait for the other.`);
      if (v.dev) throw new Error(`${v.name} runs in dev mode, in memory; turn dev off first.`);
      v.storage = "consul"; v.store = d.id; v.reg = "";
      return `${v.name} keeps its data in ${d.name}, through a Consul client agent beside each server. That registers it in ${d.name} too.`;
    }
    if (how === "reg") {
      if (v.storage === "consul") throw new Error(`${v.name} keeps its data in Consul, which registers it already.`);
      v.reg = d.id;
      return `${v.name} registers in ${d.name} as the vault service, tagged active or standby.`;
    }
    if (how === "secrets") {
      if (v.repl) throw new Error(`${v.name} is a replication secondary; set the Consul secrets engine on its primary.`);
      v.secrets = d.id;
      const note = m.acl ? "" : " The engine mints ACL tokens, so that turned ACLs on.";
      m.acl = true;
      return `${v.name} mints ${d.name}'s ACL tokens at consul/creds/management.${note}`;
    }
    throw new Error("unknown link " + how);
  }

  // joinHint says how to let a call between two sides through.
  function joinHint(m, sd, sp, td, tp) {
    if (sd.parts.length || td.parts.length) return `Peer ${sideName(m, sd.id, sp)} with ${sideName(m, td.id, tp)} so the call can go through: peers belong to a partition.`;
    return `Federate or peer ${sd.name} and ${td.name} so the call can go through.`;
  }

  // side resolves a dc or part id to [dc, part id].
  function sideOf(m, id) {
    const f = find(m, id);
    if (f && f.t === "part") return [f.dc, f.part.id];
    return [dcById(m, id), ""];
  }
  // addMesh puts a mesh gateway in a dc's partition unless it has one.
  function addMesh(d, pid) {
    pid = pid || "";
    if (d.gws.some((g) => g.kind === "mesh" && (g.part || "") === pid)) return;
    // Partitions may reuse a name, as Consul allows.
    const used = new Set([...d.nodes, ...d.gws].filter((x) => (x.part || "") === pid).map((x) => x.name));
    let name = "mesh";
    for (let i = 2; used.has(name); i++) name = `mesh-${i}`;
    d.gws.push(gw("mesh", name, pid));
  }
  // peeringGateways adds what a peering needs: a mesh gateway in each side's
  // partition for service traffic and, through gateways, one in each dc's
  // default partition for the control plane (Consul only peers through
  // the default partition's gateway).
  function peeringGateways(m, p) {
    for (const [dcId, pid] of [[p.acceptor, p.accPart], [p.dialer, p.dialPart]]) {
      const d = dcById(m, dcId);
      if (!d) continue;
      addMesh(d, pid);
      if (p.mesh && pid) addMesh(d, "");
    }
  }

  // joinDCs federates the lab or peers two dcs (how is wan, mesh-gateways
  // or peering). For a peering, an id can be a partition's.
  function joinDCs(m, aId, bId, how) {
    if (how === "peering") {
      // The side that calls dials; the other accepts (it exports).
      const [a, ap] = sideOf(m, aId), [b, bp] = sideOf(m, bId);
      const p = { id: uid("p"), name: "", acceptor: b.id, dialer: a.id, accPart: bp, dialPart: ap, mesh: true };
      m.peerings.push(p);
      peeringGateways(m, p);
      const where = ap || bp ? "each side's partition" : "each";
      return `Peered ${sideName(m, a.id, ap)} (dialer) with ${sideName(m, b.id, bp)} (acceptor) as ${peeringName(m, p)}, with a mesh gateway in ${where}.`;
    }
    const a = dcById(m, aId), b = dcById(m, bId);
    m.fed = how;
    if (how === "mesh-gateways") {
      m.tls = true;
      for (const d of m.dcs) if (!d.gws.some((g) => g.kind === "mesh" && !g.part)) d.gws.push(gw("mesh", uniqueName(d, "mesh")));
      return "Federated every datacenter through mesh gateways; that turned TLS on and added a mesh gateway where one was missing.";
    }
    return "Federated every datacenter over the WAN.";
  }

  // remove deletes a thing and everything that points at it.
  function remove(m, id) {
    const f = find(m, id);
    if (!f) return;
    const drop = new Set([id]);
    if (f.t === "dc") {
      m.dcs = m.dcs.filter((d) => d.id !== id);
      f.dc.nodes.forEach((n) => drop.add(n.id));
      m.peerings = m.peerings.filter((p) => p.acceptor !== id && p.dialer !== id);
      if (m.dcs.length < 2) m.fed = "";
    } else if (f.t === "part") {
      f.dc.parts = f.dc.parts.filter((p) => p.id !== id);
      f.dc.nodes.filter((n) => n.part === id).forEach((n) => drop.add(n.id));
      f.dc.nodes = f.dc.nodes.filter((n) => n.part !== id);
      f.dc.gws = f.dc.gws.filter((g) => g.part !== id);
      m.peerings = m.peerings.filter((p) => p.accPart !== id && p.dialPart !== id);
    } else if (f.t === "node") {
      f.dc.nodes = f.dc.nodes.filter((n) => n.id !== id);
    } else if (f.t === "gw") {
      f.dc.gws = f.dc.gws.filter((g) => g.id !== id);
    } else if (f.t === "peering") {
      m.peerings = m.peerings.filter((p) => p.id !== id);
    } else if (f.t === "ext") {
      m.exts = m.exts.filter((x) => x.id !== id);
    } else if (f.t === "fail") {
      m.fails = m.fails.filter((x) => x.id !== id);
    } else if (f.t === "vault") {
      m.vaults = m.vaults.filter((v) => v.id !== id);
      for (const v of m.vaults) {
        if (v.transit === id) v.transit = "";
        if (v.repl === id) { v.repl = ""; v.replMode = ""; v.replFilter = null; }
      }
      for (const d of m.dcs) if (d.ca === id) d.ca = "";
    }
    // Vault's links to a removed datacenter go with it.
    if (f.t === "dc") for (const v of m.vaults || []) {
      if (v.store === id) { v.storage = "raft"; v.store = ""; }
      if (v.reg === id) v.reg = "";
      if (v.secrets === id) v.secrets = "";
    }
    // Registrations in a removed datacenter or partition go with it.
    if (f.t === "dc" || f.t === "part") for (const x of m.exts || []) x.in = x.in.filter((r) => r.dc !== id && r.part !== id);
    pruneFails(m);
    for (const d of m.dcs) {
      for (const n of d.nodes) if (n.svc) n.svc.ups = n.svc.ups.filter((u) => !drop.has(u.to));
      for (const g of d.gws) g.to = g.to.filter((t) => !drop.has(t));
    }
  }

  // rename checks a new name and applies it. Upstreams follow by id.
  function rename(m, id, name) {
    const f = find(m, id);
    name = String(name || "").trim();
    const before = sel(m, id), oldName = f && (f.dc && f.t === "dc" ? f.dc : f.node || f.gw || f.ext || f.part || f.vault || {}).name;
    renameOnly(m, f, name);
    const after = sel(m, id);
    retarget(m, before, after, (f.t === "node" && f.node.svc) || f.t === "ext" ? [oldName, name] : null);
    // A partition's nodes: dc1/team-a/web.
    if (f.t === "part") retarget(m, `${f.dc.name}/${oldName}`, `${f.dc.name}/${name}`);
  }
  function renameOnly(m, f, name) {
    if (!NAME_RE.test(name)) throw new Error("Use lowercase letters, digits and single hyphens.");
    // Datacenters and Vault clusters share one name space.
    const taken = (self) => m.dcs.some((d) => d !== self && d.name === name) || (m.vaults || []).some((v) => v !== self && v.name === name);
    if (f.t === "vault") {
      if (taken(f.vault)) throw new Error(`There's a ${name} already; datacenters and Vault clusters share names.`);
      f.vault.name = name;
    } else if (f.t === "dc") {
      if (taken(f.dc)) throw new Error(`There's a ${name} already.`);
      f.dc.name = name;
    } else if (f.t === "part") {
      if (name === "default" || f.dc.parts.some((p) => p !== f.part && p.name === name)) throw new Error(`There's a partition ${name} already.`);
      f.part.name = name;
    } else if (f.t === "node" || f.t === "gw") {
      const self = f.node || f.gw;
      if (self.name !== name && namesIn(f.dc, m).has(name)) throw new Error(`${f.dc.name} has a ${name} already.`);
      self.name = name;
    } else if (f.t === "peering") {
      f.peering.name = name;
    } else if (f.t === "ext") {
      if (f.ext.name === name) return;
      if (m.exts.some((x) => x !== f.ext && x.name === name)) throw new Error(`There's an external service ${name} already.`);
      for (const r of f.ext.in) { const d = dcById(m, r.dc); if (d && [...d.nodes, ...d.gws].some((y) => (y.part || "") === (r.part || "") && y.name === name)) throw new Error(`${sideName(m, r.dc, r.part)} has a ${name} already.`); }
      f.ext.name = name;
    }
  }

  // ---------- scenario ----------
  // sel is the scenario selector for a thing on the canvas ("srv:" + dc id
  // is the dc's servers).
  function sel(m, id) {
    if (id.startsWith("srv:")) { const d = dcById(m, id.slice(4)); return d ? `${d.name}/servers` : ""; }
    const f = find(m, id);
    if (!f) return "";
    if (f.t === "ext") return f.ext.name;
    if (f.t === "vault") return f.vault.name;
    if (f.t === "dc") return f.dc.name;
    if (f.t === "part") return `part:${f.dc.name}/${f.part.name}`;
    const x = f.node || f.gw;
    if (!x) return "";
    return x.part ? `${f.dc.name}/${partName(f.dc, x.part)}/${x.name}` : `${f.dc.name}/${x.name}`;
  }
  // FAULTS are the steps that act on a selector, in menu order.
  const FAULTS = ["stop", "start", "restart", "kill", "pause", "resume", "partition"];
  // stepFor makes a step for a thing: a fault, or a check (wait, call,
  // healthy, exec). It returns null when the kind doesn't fit the thing.
  function stepFor(m, id, kind) {
    const s = sel(m, id), f = find(m, id.replace(/^srv:/, ""));
    if (!s) return null;
    const svcName = f && f.t === "node" && f.node.svc ? f.node.name : f && f.t === "ext" ? f.ext.name : "";
    const dcName = id.startsWith("srv:") ? dcById(m, id.slice(4)).name : f && f.dc ? f.dc.name : f && f.t === "ext" && f.ext.in.length ? (dcById(m, f.ext.in[0].dc) || {}).name || "" : "";
    switch (kind) {
      case "stop": case "start": case "restart": case "kill": case "pause": case "resume": return { [kind]: s };
      case "partition": return { partition: [s] };
      case "heal": return { heal: "all" };
      case "wait": return { wait: `${dcName || s} ready` };
      case "sleep": return { sleep: "10s" };
      case "call": {
        if (!(f && f.t === "node" && f.node.svc)) return null;
        const up = f.node.svc.ups.map((u) => find(m, u.to)).filter(Boolean)[0];
        const c = { on: s };
        if (up) c.reaches = (up.node || up.ext).name;
        return { assert: { call: c, within: "60s" } };
      }
      case "healthy": return svcName ? { assert: { consul: Object.assign(m.dcs.length > 1 && dcName ? { dc: dcName } : {}, { service_healthy: svcName }), within: "60s" } } : null;
      case "exec": return f && (f.t === "node" || f.t === "gw") ? { assert: { exec: { on: s, run: "true", output: "" }, within: "30s" } } : null;
      case "seal": return f && f.t === "vault" && !f.vault.dev ? { vault: { seal: `${s}/active` } } : null;
      case "unseal": return f && f.t === "vault" && !f.vault.dev ? { vault: { unseal: s } } : null;
      case "stepdown": return f && f.t === "vault" && f.vault.servers > 1 ? { vault: { step_down: `${s}/active` } } : null;
      case "unsealed": return f && f.t === "vault" ? { assert: { vault: { on: `${s}/active`, sealed: false }, within: "30s" } } : null;
      case "promote": return f && f.t === "vault" && f.vault.repl ? { vault: { promote: s } } : null;
      case "demote": {
        if (!(f && f.t === "vault")) return null;
        const modes = ["dr", "performance"].filter((md) => m.vaults.some((x) => x.repl === f.vault.id && x.replMode === md));
        return modes.length ? { vault: { demote: s, replication: modes[0] } } : null;
      }
      case "replicating": return f && f.t === "vault" && f.vault.repl ? { assert: { vault: { on: s, replication: { [f.vault.replMode]: "secondary", state: "stream-wals" } }, within: "60s" } } : null;
      case "fault": return f && f.t === "node" && f.node.svc ? { fake: { on: s, health: { code: 503 } } } : null;
      case "unfault": return f && f.t === "node" && f.node.svc ? { fake: { on: s, reset: true } } : null;
      case "limits": {
        if (!(f && f.t === "node" && f.node.svc)) return null;
        const cur = profileOf(m, f.node);
        return { fake: { on: s, limits: { profile: cur.own || cur.inh ? (cur.own || cur.inh) === "none" ? "tomcat" : "none" : "tomcat" } } };
      }
    }
    return null;
  }
  // nameStep sets (or, with "", clears) a step's name, written first.
  function nameStep(st, name) {
    const rest = Object.entries(st).filter(([k]) => k !== "name");
    Object.keys(st).forEach((k) => delete st[k]);
    if (name) st.name = name;
    rest.forEach(([k, v]) => { st[k] = v; });
  }
  // addStep appends a step to the scenario, making one when there's none.
  function addStep(m, st) {
    if (!m.scenario) m.scenario = { steps: [] };
    if (!Array.isArray(m.scenario.steps)) m.scenario.steps = [];
    m.scenario.steps.push(st);
    return m.scenario.steps.length - 1;
  }
  // stepText is a one-line summary of a step.
  function stepText(st) {
    if (!isMap(st)) return String(st);
    for (const k of FAULTS.concat(["heal", "wait", "sleep", "snapshot"])) if (k in st) return `${k} ${Array.isArray(st[k]) ? flow(st[k]) : st[k]}`;
    if (isMap(st.fake)) return `fake ${st.fake.on || ""}${st.fake.reset ? " reset" : ""}${st.fake.health ? ` health ${st.fake.health.code}` : ""}${isMap(st.fake.limits) ? ` limits ${st.fake.limits.profile || Object.keys(st.fake.limits).join(", ")}` : ""}`;
    if (isMap(st.upgrade)) return `upgrade ${st.upgrade.on || ""} to ${st.upgrade.to || ""}`;
    if (isMap(st.exec)) return `exec on ${st.exec.on || ""}`;
    const kv = (o) => Object.entries(o).map(([k, v]) => (isMap(v) ? `${k} ${kv(v)}` : `${k} ${v}`)).join(", ");
    if (isMap(st.vault)) return `vault ${kv(st.vault)}`;
    if (isMap(st.assert)) {
      const a = st.assert, w = a.within ? ` within ${a.within}` : "";
      if (isMap(a.call)) return `call ${a.call.on || ""}${a.call.reaches ? ` reaches ${a.call.reaches}` : ""}${a.call.code ? ` code ${a.call.code}` : ""}${w}`;
      if (isMap(a.exec)) return `exec on ${a.exec.on || ""}${a.exec.output ? ` shows ${a.exec.output}` : ""}${w}`;
      if (isMap(a.consul)) return `consul ${Object.entries(a.consul).map(([k, v]) => `${k} ${v}`).join(", ")}${w}`;
      if (isMap(a.vault)) return `vault ${kv(a.vault)}${w}`;
    }
    return Object.keys(st).filter((k) => k !== "name" && k !== "at").join(", ");
  }
  // Step keys whose values are selectors.
  const SEL_KEYS = new Set([...FAULTS, "heal", "wait", "on", "seal", "unseal", "step_down", "promote", "demote", "disable", "update_primary"]);
  // retarget makes the scenario's selectors follow a rename: from and to
  // are selectors (dc1, dc1/web); bare is a service's old and new name.
  function retarget(m, from, to, bare) {
    if (!m.scenario || from === to) return;
    const esc = (x) => x.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
    const re = new RegExp(`(^|[\\s,:])${esc(from)}(?=$|[\\s,/])`, "g");
    const fix = (v) => (typeof v === "string" ? v.replace(re, `$1${to}`) : Array.isArray(v) ? v.map(fix) : v);
    const walk = (o) => {
      if (Array.isArray(o)) { o.forEach(walk); return; }
      if (!isMap(o)) return;
      for (const [k, v] of Object.entries(o)) {
        if (SEL_KEYS.has(k) || (k === "dc" && !from.includes("/"))) o[k] = fix(v);
        else if (bare && (k === "reaches" || k === "service_healthy") && v === bare[0]) o[k] = bare[1];
        else walk(v);
      }
    };
    walk(m.scenario.steps);
  }

  // ---------- YAML ----------
  // toDoc builds the topology as plain objects, keys in the examples' order.
  // ---------- fake service ----------
  // The app servers limits.profile names (internal/fake/profiles.go).
  const FAKE_PROFILES = ["tomcat", "nginx", "apache", "node", "envoy", "go", "alb", "kestrel", "gunicorn"];
  const clone = (v) => JSON.parse(JSON.stringify(v));
  // fakeGet reads a dotted key ("limits.codes.header") of a fake: block.
  function fakeGet(fk, key) {
    let v = fk;
    for (const k of key.split(".")) { if (!isMap(v) || !(k in v)) return undefined; v = v[k]; }
    return v;
  }
  // fakeSet sets a dotted key of the fake: block at holder[field] (made
  // when missing); undefined or "" removes the key, and maps left empty
  // go with it.
  function fakeSet(holder, field, key, value) {
    const path = key.split(".");
    if (!isMap(holder[field])) holder[field] = {};
    const chain = [holder[field]];
    for (const k of path.slice(0, -1)) {
      const cur = chain[chain.length - 1];
      if (!isMap(cur[k])) { if (value === undefined || value === "") return; cur[k] = {}; }
      chain.push(cur[k]);
    }
    const last = path[path.length - 1];
    if (value === undefined || value === "" || (Array.isArray(value) && !value.length)) delete chain[chain.length - 1][last];
    else chain[chain.length - 1][last] = value;
    for (let i = chain.length - 1; i > 0; i--) if (!Object.keys(chain[i]).length) delete chain[i - 1][path[i - 1]];
    if (!Object.keys(holder[field]).length) delete holder[field];
  }
  // fakeValue turns what was typed into a value: a plain number is a
  // number, anything else ("8KB", "20s") stays text.
  const fakeValue = (s) => { const t = String(s).trim(); return /^-?\d+(\.\d+)?$/.test(t) ? Number(t) : t; };
  // profileOf is a service's limits profile: own (its fake: block) and
  // inh (from service_template).
  const profileOf = (m, n) => ({ own: (n && n.svc && fakeGet(n.svc.fake, "limits.profile")) || "", inh: fakeGet(m.fakeDef, "limits.profile") || "" });

  function toDoc(m) {
    fillInh(m);
    const doc = { parker: "v1alpha1", name: m.name };
    const nodes = m.dcs.flatMap((d) => d.nodes);
    const anyEnvoy = nodes.some((n) => connectOf(m, n) !== "none") || m.dcs.some((d) => d.gws.length);
    const vaults = m.vaults || [];
    // A lab of Vault clusters alone has no consul: section.
    const hasConsul = m.dcs.length > 0 || !vaults.length;
    const versions = hasConsul ? { consul: m.consul + (ent(m) && !/\+ent$/.test(m.consul) ? "+ent" : "") } : {};
    if (anyEnvoy) versions.envoy = m.envoy;
    if (nodes.some((n) => n.agent === "dataplane")) versions["consul-dataplane"] = m.dataplane;
    if (vaults.length) versions.vault = (m.vault || VERSIONS.vault) + (vaults.some((v) => v.repl) && !entVault(m) ? "+ent" : "");
    doc.versions = versions;

    const consul = {};
    const defs = {};
    if (m.tls) defs.tls = true;
    if (m.acl) defs.acl = true;
    const exts = m.exts || [];
    if (nodes.some((n) => n.svc) || exts.length) {
      defs.service_template = m.mesh ? { run: "fake-service", connect: "sidecar" } : { run: "fake-service" };
      if (isMap(m.fakeDef) && Object.keys(m.fakeDef).length) defs.service_template.fake = clone(m.fakeDef);
    }
    if (Object.keys(defs).length) consul.defaults = defs;
    if (m.fed && m.dcs.length > 1) consul.federation = { mode: m.fed };
    if (exts.length) {
      consul.external = {};
      for (const x of exts) {
        const sv = { port: x.port };
        if (extNs(m, x) !== normNs(m.defNs)) sv.namespace = x.ns || "default";
        consul.external[x.name] = { service: sv, in: x.in.map((r) => sideName(m, r.dc, r.part)) };
      }
    }

    const all = calls(m, true);
    if (m.peerings.length) {
      consul.peering = {};
      for (const p of m.peerings) {
        const a = dcById(m, p.acceptor), d = dcById(m, p.dialer);
        if (!a || !d) continue;
        const pe = { acceptor: sideName(m, a.id, p.accPart), dialer: sideName(m, d.id, p.dialPart) };
        if (p.mesh) pe.via_mesh_gateways = true;
        const exp = {};
        for (const c of all) if (c.r.kind === "peer" && c.r.peering === p) {
          const k = sideName(m, c.td.id, c.t.part);
          const name = nsOf(c.t) ? `${nsOf(c.t)}/${c.t.name}` : c.t.name;
          (exp[k] = exp[k] || []).includes(name) || exp[k].push(name);
        }
        if (Object.keys(exp).length) pe.exports = exp;
        consul.peering[peeringName(m, p)] = pe;
      }
    }

    consul.datacenters = {};
    for (const d of m.dcs) {
      const out = { servers: d.servers };
      if (d.ui) out.ui = true;
      const ca = d.ca && vaultById(m, d.ca);
      if (ca) out.connect = { ca: { vault: ca.name } };
      const scopes = [{ id: "", name: "default" }, ...d.parts];
      const bodies = scopes.map((s) => scopeBody(m, d, s, all));
      if (d.parts.length) {
        out.partitions = {};
        scopes.forEach((s, i) => { out.partitions[s.name] = bodies[i]; });
      } else Object.assign(out, bodies[0]);
      consul.datacenters[d.name] = out;
    }
    if (hasConsul) doc.consul = consul;
    if (vaults.length) {
      const clusters = {};
      const dn = (id) => (dcById(m, id) || {}).name;
      for (const v of vaults) {
        const c = {};
        if (v.servers > 1) c.servers = v.servers;
        if (v.dev) c.dev = true;
        if (v.tls) c.tls = true;
        if (v.storage === "file") c.storage = "file";
        if (v.storage === "consul" && dn(v.store)) c.storage = { consul: dn(v.store) };
        if (v.reg && dn(v.reg) && v.storage !== "consul") c.register = dn(v.reg);
        const t = v.transit && vaultById(m, v.transit);
        if (t) c.seal = { transit: t.name };
        if (v.secrets && dn(v.secrets)) c.secrets = { consul: dn(v.secrets) };
        clusters[v.name] = c;
      }
      doc.vault = { clusters };
      const repl = {};
      for (const md of ["performance", "dr"]) {
        const secs = vaults.filter((v) => v.repl && v.replMode === md && vaultById(m, v.repl));
        if (!secs.length) continue;
        const set = { primary: vaultById(m, secs[0].repl).name, secondaries: secs.map((v) => v.name) };
        const pf = md === "performance" ? secs.filter((v) => isMap(v.replFilter)) : [];
        if (pf.length) { set.paths_filter = {}; for (const v of pf) set.paths_filter[v.name] = clone(v.replFilter); }
        repl[md] = set;
      }
      if (Object.keys(repl).length) doc.vault.replication = repl;
    }
    if (m.scenario && Array.isArray(m.scenario.steps) && m.scenario.steps.length) doc.scenario = JSON.parse(JSON.stringify(m.scenario));
    return doc;
  }

  // scopeBody is one dc's (or partition's) gateways, nodes and entries.
  function scopeBody(m, d, s, all) {
    const body = {};
    const inScope = (x) => (x.part || "") === s.id;
    const gws = d.gws.filter(inScope), nodes = d.nodes.filter(inScope);
    const entries = [];
    const httpSvcs = [];
    const routes = [];
    if (gws.length) {
      body.gateways = {};
      for (const g of gws) {
        const tnodes = targetsOf(m, d, g), targets = tnodes.map((t) => t.name);
        const gns = gwNs(g);
        // A service a gateway names, with its namespace when it isn't the
        // gateway's own.
        const ref = (n, extra) => Object.assign({ Name: n.name }, nsOf(n) !== gns ? { Namespace: nsOf(n) || "default" } : {}, extra || {});
        let v = {};
        if (g.name !== g.kind) v.kind = g.kind;
        if (gns) v.namespace = gns;
        if (g.kind === "ingress" && targets.length) {
          v.entry = { Listeners: tnodes.map((t, i) => ({ Port: 8080 + i, Protocol: "http", Services: [ref(t, { Hosts: ["*"] })] })) };
          httpSvcs.push(...tnodes);
        } else if (g.kind === "terminating" && targets.length) {
          v.entry = { Services: tnodes.map((t) => ref(t)) };
        } else if (g.kind === "api") {
          v.entry = { Listeners: [{ Name: "http", Port: 8081, Protocol: "http" }] };
          httpSvcs.push(...tnodes);
          tnodes.forEach((t) => {
            // One service takes every path; several split by a path prefix.
            const rule = tnodes.length > 1 ? { Matches: [{ Path: { Match: "prefix", Value: "/" + t.name } }], Services: [ref(t)] } : { Services: [ref(t)] };
            const r = { Kind: "http-route", Name: `${g.name}-to-${t.name}` };
            if (gns) r.Namespace = gns;
            Object.assign(r, { Parents: [{ Kind: "api-gateway", Name: g.name, SectionName: "http" }], Rules: [rule] });
            routes.push(r);
          });
        }
        body.gateways[g.name] = Object.keys(v).length ? v : true;
      }
    }
    if (nodes.length) {
      body.nodes = {};
      for (const n of nodes) {
        const v = {};
        if (n.count > 1) v.count = n.count;
        if (n.agent === "dataplane") v.agent = "dataplane";
        if (n.svc) {
          const sv = { port: n.svc.port };
          const want = connectOf(m, n);
          if (want !== inherited(m, n)) sv.connect = want;
          if (normNs(n.svc.ns || n.svc.inhNs) !== normNs(n.svc.inhNs)) sv.namespace = n.svc.ns;
          const ups = {};
          for (const c of all.filter((c) => c.src === n && !c.fo)) {
            let u;
            if (c.r.kind === "local") u = { port: c.port };
            else if (c.r.kind === "partition") u = { port: c.port, partition: c.r.partition };
            else if (c.r.kind === "peer") u = { port: c.port, peer: c.r.peer };
            else u = { port: c.port, dc: c.r.dc };
            // The destination namespace defaults to the caller's.
            if (nsOf(c.t) !== nsOf(n)) u.namespace = nsOf(c.t) || "default";
            ups[c.t.name] = Object.keys(u).length === 1 ? u.port : u;
          }
          if (Object.keys(ups).length) sv.upstreams = ups;
          if (isMap(n.svc.fake) && Object.keys(n.svc.fake).length) sv.fake = clone(n.svc.fake);
          v.service = sv;
        }
        body.nodes[n.name] = v;
      }
    }
    // Mesh gateways in local mode for calls that leave the dc or the
    // partition: on the caller's side, and on both sides between partitions.
    const here = (dd, part) => dd.id === d.id && (part || "") === s.id;
    const viaGW = all.some((c) => (c.r.kind !== "local" && here(c.sd, c.src.part)) || (c.r.kind === "partition" && here(c.td, c.t.part)));
    if (viaGW) entries.push({ Kind: "proxy-defaults", Name: "global", MeshGateway: { Mode: "local" } });
    const seenHttp = new Set();
    for (const t of httpSvcs) {
      const k = nsOf(t) + "/" + t.name;
      if (seenHttp.has(k)) continue;
      seenHttp.add(k);
      entries.push(Object.assign({ Kind: "service-defaults", Name: t.name }, nsOf(t) ? { Namespace: nsOf(t) } : {}, { Protocol: "http" }));
    }
    // Failovers from here: a service-resolver each.
    for (const fo of (m.fails || []).filter((x) => x.dc === d.id && (x.part || "") === s.id)) {
      const at = svcAt(m, fo.svc, fo.dc, fo.part);
      const targets = fo.to.map((t) => foTarget(m, fo, t)).filter(Boolean);
      if (!at || !targets.length) continue;
      const e = Object.assign({ Kind: "service-resolver", Name: at.t.name }, nsOf(at.t) ? { Namespace: nsOf(at.t) } : {});
      if (fo.timeout) e.ConnectTimeout = fo.timeout;
      e.Failover = { "*": { Targets: targets } };
      entries.push(e);
    }
    // Exports to other partitions.
    const exp = new Map();
    for (const c of all) if (c.r.kind === "partition" && here(c.td, c.t.part)) {
      const k = nsOf(c.t) + "/" + c.t.name;
      const e = exp.get(k) || { t: c.t, consumers: [] };
      const p = partName(c.sd, c.src.part);
      if (!e.consumers.includes(p)) e.consumers.push(p);
      exp.set(k, e);
    }
    if (exp.size) entries.push({ Kind: "exported-services", Name: s.name, Services: [...exp.values()].map((e) => Object.assign({ Name: e.t.name }, nsOf(e.t) ? { Namespace: nsOf(e.t) } : {}, { Consumers: e.consumers.map((p) => ({ Partition: p })) })) });
    entries.push(...routes);
    // With ACLs on (deny by default), allow each call.
    if (m.acl) {
      // By destination (namespace and name); a source's namespace defaults
      // to the destination's.
      const allow = new Map();
      const add = (t, src) => {
        const k = nsOf(t) + "/" + t.name;
        const e = allow.get(k) || { t, srcs: [] };
        if (!e.srcs.some((x) => JSON.stringify(x) === JSON.stringify(src))) e.srcs.push(src);
        allow.set(k, e);
      };
      const srcNs = (ns, t) => (ns !== nsOf(t) ? { Namespace: ns || "default" } : {});
      for (const c of all) if (here(c.td, c.t.part)) {
        const src = Object.assign({ Name: c.src.name }, srcNs(nsOf(c.src), c.t));
        if (c.r.kind === "peer") src.Peer = c.r.peer;
        if (c.r.kind === "partition") src.Partition = partName(c.sd, c.src.part);
        src.Action = "allow";
        add(c.t, src);
      }
      for (const g of gws) if (g.kind !== "mesh") for (const t of targetsOf(m, d, g)) {
        if (connectOf(m, t) !== "none") add(t, Object.assign({ Name: g.name }, srcNs(gwNs(g), t), { Action: "allow" }));
      }
      for (const e of allow.values()) entries.push(Object.assign({ Kind: "service-intentions", Name: e.t.name }, nsOf(e.t) ? { Namespace: nsOf(e.t) } : {}, { Sources: e.srcs }));
    }
    // The user's own entries, after the canvas's; theirs win.
    const own = ((d.ces || {})[s.id] || []);
    const all2 = [...entries.filter((e) => !own.some((o) => entryKey(o) === entryKey(e))), ...own];
    if (all2.length) body.config_entries = all2;
    return body;
  }

  // ---------- config entries ----------
  // entryKey names an entry: Kind/Namespace/Name, or file:path.
  const entryKey = (e) => (isMap(e) && typeof e.file === "string" ? `file:${e.file}` : isMap(e) ? `${e.Kind}/${normNs(e.Namespace)}/${e.Name}` : "");
  // entries lists side (d, part id)'s config entries: what the canvas
  // makes ({e, why, over: overridden by the user's}) and the user's own.
  function entries(m, d, pid) {
    const own = ((d.ces || {})[pid || ""] || []);
    const save = d.ces; d.ces = {};
    const gen = (scopeBody(m, d, { id: pid || "", name: partName(d, pid) }, calls(m, true)).config_entries || []);
    d.ces = save;
    const why = (e) => ({
      "proxy-defaults": "calls leave through mesh gateways (local mode)",
      "service-defaults": `a gateway routes HTTP to ${e.Name}`,
      "exported-services": "calls from other partitions",
      "http-route": `API gateway ${((e.Parents || [])[0] || {}).Name || ""}`,
      "service-intentions": `calls to ${e.Name}, with ACLs on`,
      "service-resolver": `${e.Name} fails over`,
    }[e.Kind] || "the canvas");
    return { gen: gen.map((e) => ({ e, why: why(e), over: own.some((o) => entryKey(o) === entryKey(e)) })), own };
  }
  // ENTRY_KEYS are the top-level fields Consul takes for each kind, to
  // catch a misspelling before Consul does.
  const BASE = ["Kind", "Name", "Namespace", "Partition", "Meta"];
  const ENTRY_KEYS = {
    "service-defaults": ["Protocol", "Mode", "TransparentProxy", "MutualTLSMode", "UpstreamConfig", "Destination", "MaxInboundConnections", "LocalConnectTimeoutMs", "LocalRequestTimeoutMs", "BalanceInboundConnections", "MeshGateway", "ExternalSNI", "Expose", "EnvoyExtensions", "RateLimits"],
    "proxy-defaults": ["Config", "Mode", "TransparentProxy", "MutualTLSMode", "MeshGateway", "Expose", "AccessLogs", "EnvoyExtensions", "FailoverPolicy", "PrioritizeByLocality"],
    "service-resolver": ["ConnectTimeout", "RequestTimeout", "DefaultSubset", "Subsets", "Redirect", "Failover", "LoadBalancer", "PrioritizeByLocality"],
    "service-router": ["Routes"],
    "service-splitter": ["Splits"],
    "service-intentions": ["Sources", "JWT"],
    "exported-services": ["Services"],
    "mesh": ["TransparentProxy", "AllowEnablingPermissiveMutualTLS", "ValidateClusters", "TLS", "HTTP", "Peering"],
    "ingress-gateway": ["TLS", "Defaults", "Listeners"],
    "terminating-gateway": ["Services"],
    "api-gateway": ["Listeners"],
    "http-route": ["Parents", "Rules", "Hostnames"],
    "tcp-route": ["Parents", "Services"],
    "inline-certificate": ["Certificate", "PrivateKey"],
    "file-system-certificate": ["Certificate", "PrivateKey"],
    "jwt-provider": ["JSONWebKeySet", "Issuer", "Audiences", "Locations", "Forwarding", "ClockSkewSeconds", "CacheConfig"],
    "sameness-group": ["DefaultForFailover", "IncludeLocal", "Members"],
    "control-plane-request-limit": ["Mode", "ReadRate", "WriteRate", "KV", "ACL", "Catalog", "ConfigEntry", "ConnectCA", "Coordinate", "DiscoveryChain", "Health", "Intention", "Internal", "PreparedQuery", "Session", "Txn"],
  };
  // entryNotes are problems with an entry the user typed: [message].
  function entryNotes(e) {
    if (!isMap(e)) return ["An entry is a map: Kind, Name and its fields, or file: PATH."];
    if ("file" in e) return Object.keys(e).length === 1 && typeof e.file === "string" ? [] : ["file: takes a path (.hcl or .json, relative to the lab file) and no other keys."];
    const out = [];
    if (typeof e.Kind !== "string") out.push("Kind: missing (service-defaults, service-resolver, ...). Fields use Consul's casing.");
    else if (!ENTRY_KEYS[e.Kind]) out.push(`Kind: ${e.Kind} isn't a config entry kind Parker knows; check the spelling.`);
    if (typeof e.Name !== "string" && e.Kind !== "mesh") out.push("Name: missing.");
    const known = e.Kind && ENTRY_KEYS[e.Kind];
    if (known) for (const k of Object.keys(e)) if (!BASE.includes(k) && !known.includes(k)) out.push(`${k}: a ${e.Kind} has no such field${/^[a-z]/.test(k) ? " (fields use Consul's casing, e.g. " + k[0].toUpperCase() + k.slice(1).replace(/_(.)/g, (x, c) => c.toUpperCase()) + ")" : ""}.`);
    return out;
  }
  // setEntry adds the user's entry e to side (d, pid), or replaces their
  // entry at index i. It returns what it did.
  function setEntry(d, pid, e, i) {
    d.ces = d.ces || {};
    const l = (d.ces[pid || ""] = d.ces[pid || ""] || []);
    if (l.some((o, j) => j !== i && entryKey(o) === entryKey(e))) throw new Error(`There's a ${entryKey(e)} here already; edit that one.`);
    if (i === undefined || i < 0) l.push(e); else l[i] = e;
    return `${i === undefined || i < 0 ? "Added" : "Saved"} ${e.file ? "file: " + e.file : `${e.Kind} ${e.Name || ""}`.trim()}.`;
  }
  function removeEntry(d, pid, i) {
    const l = (d.ces || {})[pid || ""] || [];
    l.splice(i, 1);
    if (!l.length && d.ces) delete d.ces[pid || ""];
  }

  // Keys whose value is always written as a block, like the examples.
  function blockAt(path) {
    const p = path.join(".");
    return path.length <= 1 && p !== "versions" ||
      /^consul(\.defaults|\.(datacenters|peering|external)(\.[^.]+)?)?$/.test(p) || p === "scenario" || p === "vault.clusters" || /^vault\.replication(\.(performance|dr)(\.paths_filter)?)?$/.test(p) ||
      /\.(nodes|partitions)(\.[^.]+)?$/.test(p) && !/\.nodes\.[^.]+\.service/.test(p);
  }

  function scalar(v) {
    if (v === true || v === false || typeof v === "number") return String(v);
    const s = String(v);
    // Plain when it reads back as the same string (spaces inside too).
    if (/^[A-Za-z0-9_./+]([A-Za-z0-9_./+ -]*[A-Za-z0-9_./+-])?$/.test(s) && !/^(true|false|null|yes|no|on|off|~)$/i.test(s) && !/^[-+]?(\d+\.?\d*|\.\d+)([eE][-+]?\d+)?$/.test(s)) return s;
    return JSON.stringify(s);
  }
  // A key: as scalar, but on: is a plain key (YAML 1.2, as Parker reads it).
  const skey = (k) => (k === "on" ? k : scalar(k));
  const isMap = (v) => v && typeof v === "object" && !Array.isArray(v);
  function mapDepth(v) {
    if (Array.isArray(v)) return Math.max(0, ...v.map(mapDepth));
    if (isMap(v)) return 1 + Math.max(0, ...Object.values(v).map(mapDepth));
    return 0;
  }
  function flow(v) {
    if (Array.isArray(v)) return `[${v.map(flow).join(", ")}]`;
    if (isMap(v)) { const e = Object.entries(v); return e.length ? `{ ${e.map(([k, x]) => `${skey(k)}: ${flow(x)}`).join(", ")} }` : "{}"; }
    return scalar(v);
  }

  // toYAML writes the lab. It returns the text, and where each path (as
  // validate names them: consul.datacenters.dc1.nodes.web) sits, by line.
  function toYAML(m) {
    const doc = toDoc(m);
    const lines = [];
    const spans = {}; // path -> [first line, last line], 1-based
    const key = (path) => path.reduce((a, k) => (typeof k === "number" ? `${a}[${k}]` : a ? `${a}.${k}` : k), "");
    const mark = (path, from) => { spans[key(path)] = [from, lines.length]; };
    function emit(v, path, ind, lead) {
      // lead is "key: " or "- key: ", written at ind.
      const pad = " ".repeat(ind);
      const start = lines.length + 1;
      if (!isMap(v) && !Array.isArray(v)) { lines.push(pad + lead + scalar(v)); return mark(path, start); }
      const f = flow(v);
      const empty = isMap(v) ? !Object.keys(v).length : !v.length;
      const block = isMap(v) ? blockAt(path) || mapDepth(v) > 2 : v.some(isMap) && (/config_entries$|^scenario\.steps$/.test(key(path)) || mapDepth(v) > 2);
      if (empty || (!block && pad.length + lead.length + f.length <= 80)) { lines.push(pad + lead + f); return markAll(v, path, start); }
      lines.push(pad + lead.slice(0, -1));
      const inner = ind + (lead.startsWith("- ") ? 4 : 2);
      if (isMap(v)) for (const [k, x] of Object.entries(v)) emit(x, [...path, k], inner, skey(k) + ": ");
      else v.forEach((x, i) => emitItem(x, [...path, i], inner));
      return mark(path, start);
    }
    // A list item: flow when it is short, else "- " and its keys under it.
    function emitItem(x, path, ind) {
      const pad = " ".repeat(ind);
      const start = lines.length + 1;
      const entry = /config_entries\[\d+\]$|^scenario\.steps\[\d+\]$/.test(key(path));
      if (!isMap(x) || !Object.keys(x).length || (!entry && mapDepth(x) <= 2 && pad.length + 2 + flow(x).length <= 80)) {
        lines.push(pad + "- " + flow(x));
        return markAll(x, path, start);
      }
      Object.entries(x).forEach(([k, v], i) => emit(v, [...path, k], i ? ind + 2 : ind, (i ? "" : "- ") + skey(k) + ": "));
      return mark(path, start);
    }
    function markAll(v, path, start) {
      mark(path, start);
      if (isMap(v)) for (const [k, x] of Object.entries(v)) markAll(x, [...path, k], start);
      else if (Array.isArray(v)) v.forEach((x, i) => markAll(x, [...path, i], start));
    }
    const header = ["# Built in the Parker lab builder.", `# Bring it up with: parker up ${m.name}.yaml`];
    lines.push(...header);
    for (const [k, v] of Object.entries(doc)) emit(v, [k], 0, scalar(k) + ": ");
    return { text: lines.join("\n") + "\n", lines, spans, doc };
  }

  // lineOf finds the line for a path an error names: the path itself, or
  // the nearest parent the YAML has.
  function lineOf(spans, path) {
    let p = path;
    while (p) {
      if (spans[p]) return spans[p][0];
      const i = Math.max(p.lastIndexOf("."), p.lastIndexOf("["));
      p = i > 0 ? p.slice(0, i) : "";
    }
    return 0;
  }

  // pathOf is the YAML path of a thing on the canvas.
  function pathOf(m, id) {
    const f = find(m, id);
    if (!f) return "";
    if (f.t === "peering") return `consul.peering.${peeringName(m, f.peering)}`;
    if (f.t === "ext") return `consul.external.${f.ext.name}`;
    if (f.t === "vault") return `vault.clusters.${f.vault.name}`;
    if (f.t === "fail") return "";
    const base = `consul.datacenters.${f.dc.name}`;
    if (f.t === "dc") return base;
    const scope = (pid) => (f.dc.parts.length ? `${base}.partitions.${partName(f.dc, pid)}` : base);
    if (f.t === "part") return scope(f.part.id);
    if (f.t === "node") return `${scope(f.node.part)}.nodes.${f.node.name}`;
    return `${scope(f.gw.part)}.gateways.${f.gw.name}`;
  }

  // idOf is the canvas thing a path names (the deepest one).
  function idOf(m, path) {
    let best = "", len = -1;
    const each = (id) => { const p = pathOf(m, id); if (p && (path === p || path.startsWith(p + ".") || path.startsWith(p + "[")) && p.length > len) { best = id; len = p.length; } };
    for (const d of m.dcs) { each(d.id); d.parts.forEach((p) => each(p.id)); d.nodes.forEach((n) => each(n.id)); d.gws.forEach((g) => each(g.id)); }
    m.peerings.forEach((p) => each(p.id));
    (m.exts || []).forEach((x) => each(x.id));
    (m.vaults || []).forEach((v) => each(v.id));
    return best;
  }

  // checks are problems the builder sees before the daemon does.
  function checks(m) {
    const out = [];
    if (!NAME_RE.test(m.name)) out.push({ message: "The lab name wants lowercase letters, digits and single hyphens.", id: "" });
    if (!m.dcs.length && !(m.vaults || []).length) out.push({ message: "Add a datacenter or a Vault cluster to start.", id: "" });
    for (const v of m.vaults || []) {
      const t = v.transit && vaultById(m, v.transit);
      if (t && t.dev) out.push({ message: `${v.name} auto-unseals with ${t.name}, which runs in dev mode and loses its keys; turn dev off on ${t.name}.`, id: v.id });
      if (t && t.transit) out.push({ message: `${v.name} auto-unseals with ${t.name}, which auto-unseals itself; ${t.name} must unseal with keys.`, id: v.id });
      const p = v.repl && vaultById(m, v.repl);
      if (p) {
        const R = REPL_NAME[v.replMode];
        if (v.dev || p.dev) out.push({ message: `${v.name} replicates ${p.name}, but ${(v.dev ? v : p).name} runs in dev mode; replication needs a cluster that keeps its data.`, id: v.id });
        if (p.repl) out.push({ message: `${v.name} replicates ${p.name}, a secondary itself; Parker doesn't chain replication.`, id: v.id });
        if (v.transit) out.push({ message: `${v.name} is a ${R} secondary that auto-unseals with transit; Parker unseals secondaries with the primary's keys.`, id: v.id });
        if (v.secrets) out.push({ message: `${v.name} is a ${R} secondary with the Consul secrets engine, which activating it would wipe; set it on ${p.name}.`, id: v.id });
        const ca = m.dcs.find((d) => d.ca === v.id);
        if (ca) out.push({ message: `${v.name} is a ${R} secondary and ${ca.name}'s Connect CA, whose keys activating it would wipe; use ${p.name}.`, id: v.id });
        const user = m.vaults.find((x) => x.transit === v.id);
        if (user) out.push({ message: `${user.name} auto-unseals with ${v.name}, a ${R} secondary whose keys activating it would wipe.`, id: v.id });
        const first = m.vaults.find((x) => x.repl && x.replMode === v.replMode);
        if (first !== v && first.repl !== v.repl) out.push({ message: `${R} replication has one primary: ${v.name} replicates ${p.name}, ${first.name} replicates ${vaultById(m, first.repl).name}.`, id: v.id });
      }
      if (v.dev && (v.servers > 1 || v.transit || v.reg || v.secrets || v.storage !== "raft")) out.push({ message: `${v.name} runs in dev mode: one server, in memory, with no auto-unseal, registration or secrets engines.`, id: v.id });
    }
    for (const c of calls(m)) if (c.r.kind === "none") {
      const message = c.r.peerParts
        ? `${sideName(m, c.sd.id, c.src.part)}/${c.src.name} calls ${c.t.name} in ${sideName(m, c.td.id, c.t.part)}, but those partitions aren't peered. Link ${sideName(m, c.sd.id, c.src.part)} to ${sideName(m, c.td.id, c.t.part)}.`
        : `${c.sd.name}/${c.src.name} calls ${c.t.name} in ${c.td.name}, but the datacenters are neither federated nor peered. Link ${c.sd.name} to ${c.td.name}.`;
      out.push({ message, id: c.src.id });
    }
    for (const d of m.dcs) {
      for (const n of d.nodes) if (n.svc && n.svc.ns && !NAME_RE.test(n.svc.ns)) out.push({ message: `${n.name}'s namespace wants lowercase letters, digits and single hyphens.`, id: n.id });
      for (const g of d.gws) if (g.ns && !NAME_RE.test(g.ns)) out.push({ message: `${g.name}'s namespace wants lowercase letters, digits and single hyphens.`, id: g.id });
    }
    for (const x of m.exts || []) {
      if (!NAME_RE.test(x.name)) out.push({ message: `${x.name}: external service names want lowercase letters, digits and single hyphens.`, id: x.id });
      if (!(x.port > 0)) out.push({ message: `${x.name} wants a port.`, id: x.id });
      if (x.ns && !NAME_RE.test(x.ns)) out.push({ message: `${x.name}'s namespace wants lowercase letters, digits and single hyphens.`, id: x.id });
      if (!x.in.length) out.push({ message: `${x.name} isn't registered anywhere. Link it to a datacenter or partition, or link a terminating gateway to it.`, id: x.id });
    }
    for (const fo of m.fails || []) {
      const at = svcAt(m, fo.svc, fo.dc, fo.part);
      if (!at) continue;
      for (const t of fo.to) if (!foTarget(m, fo, t)) out.push({ message: `${at.t.name} in ${sideName(m, fo.dc, fo.part)} fails over to ${sideName(m, t.dc, t.part)}, but nothing joins them. ${joinHint(m, dcById(m, fo.dc), fo.part, dcById(m, t.dc), t.part)}`, id: fo.svc });
    }
    const unfronted = new Set();
    for (const c of calls(m)) if (c.t.ext && c.r.kind !== "none") {
      const k = c.t.ext.id + "@" + c.td.id + "/" + c.t.part;
      if (unfronted.has(k) || c.td.gws.some((g) => g.kind === "terminating" && (g.part || "") === c.t.part && g.to.includes(c.t.ext.id))) continue;
      unfronted.add(k);
      out.push({ message: `${c.src.name} calls ${c.t.name}, but no terminating gateway in ${sideName(m, c.td.id, c.t.part)} fronts it. Link one to ${c.t.name}.`, id: c.t.ext.id });
    }
    for (const p of m.peerings) {
      const a = dcById(m, p.acceptor), d = dcById(m, p.dialer);
      if (a && d && a.id === d.id) out.push({ message: `Consul can't peer two partitions of one datacenter (${peeringName(m, p)}). Link their services instead: the builder exports them to the other partition.`, id: p.id });
    }
    return out;
  }

  // ---------- files ----------
  // A file someone opened is the source of truth: the canvas shows what it
  // merges to (imports and templates applied), and a canvas edit becomes
  // ops (see labfile.Patch) that splice the change into the file's text.

  const dot = (path) => path.reduce((a, k) => (typeof k === "number" ? `${a}[${k}]` : a ? `${a}.${k}` : k), "");
  const at = (v, path) => { for (const k of path) { if (v === null || typeof v !== "object") return undefined; v = v[k]; } return v; };
  const has = (v, path) => { for (const k of path) { if (v === null || typeof v !== "object" || !(k in v)) return false; v = v[k]; } return true; };
  // canon is v as JSON with sorted keys: equal values, equal text.
  const canon = (v) => JSON.stringify(v, (k, x) => (isMap(x) ? Object.fromEntries(Object.keys(x).sort().map((y) => [y, x[y]])) : x));
  const same = (a, b) => canon(a) === canon(b);

  // segs is a thing's path as keys, the way ops want it.
  function segs(m, id) {
    const f = find(m, id);
    if (!f) return null;
    if (f.t === "peering") return ["consul", "peering", peeringName(m, f.peering)];
    if (f.t === "ext") return ["consul", "external", f.ext.name];
    if (f.t === "vault") return ["vault", "clusters", f.vault.name];
    if (f.t === "fail") return null;
    const base = ["consul", "datacenters", f.dc.name];
    if (f.t === "dc") return base;
    const scope = (pid) => (f.dc.parts.length ? [...base, "partitions", partName(f.dc, pid)] : base);
    if (f.t === "part") return scope(f.part.id);
    if (f.t === "node") return [...scope(f.node.part), "nodes", f.node.name];
    return [...scope(f.gw.part), "gateways", f.gw.name];
  }

  // fromTree builds the canvas model from a merged file (the tree check
  // returns). prev is the model it replaces: things at the same path keep
  // their ids, so a selection survives an edit. A file with no consul:
  // (the low-level form) gives a model with no datacenters and raw set.
  function fromTree(t, prev) {
    t = isMap(t) ? t : {};
    const ids = new Map();
    if (prev) {
      for (const d of prev.dcs) { ids.set(dot(segs(prev, d.id)), d.id); for (const x of [...d.parts, ...d.nodes, ...d.gws]) ids.set(dot(segs(prev, x.id)), x.id); }
      for (const p of prev.peerings) ids.set(dot(segs(prev, p.id)), p.id);
      for (const x of prev.exts || []) ids.set(dot(segs(prev, x.id)), x.id);
      for (const x of prev.vaults || []) ids.set(dot(segs(prev, x.id)), x.id);
    }
    const keep = (x, path) => { const id = ids.get(dot(path)); if (id) x.id = id; return x; };
    const m = lab(typeof t.name === "string" ? t.name : "lab");
    const c = isMap(t.consul) ? t.consul : null;
    const v = isMap(t.versions) ? t.versions : {};
    if (v.consul) m.consul = String(v.consul);
    if (v.envoy) m.envoy = String(v.envoy);
    if (v["consul-dataplane"]) m.dataplane = String(v["consul-dataplane"]);
    if (v.vault) m.vault = String(v.vault);
    // Vault clusters, and the dcs whose Connect CA they are, once the
    // dcs exist.
    const cas = [];
    const readVaults = () => {
      const vt = isMap(t.vault) ? t.vault : {};
      const defs = isMap(vt.defaults) ? vt.defaults : {};
      const dcNamed = (n) => (m.dcs.find((d) => d.name === n) || {}).id || "";
      const transits = [];
      for (const [vn, cvRaw] of Object.entries(isMap(vt.clusters) ? vt.clusters : {})) {
        const cv = Object.assign({}, defs, isMap(cvRaw) ? cvRaw : {});
        const x = keep(vault(vn, Number(cv.servers) || 1), ["vault", "clusters", vn]);
        x.dev = !!cv.dev; x.tls = !!cv.tls;
        if (cv.storage === "file") x.storage = "file";
        else if (isMap(cv.storage) && typeof cv.storage.consul === "string") { x.storage = "consul"; x.store = dcNamed(cv.storage.consul); }
        if (typeof cv.register === "string") x.reg = dcNamed(cv.register);
        if (isMap(cv.secrets) && typeof cv.secrets.consul === "string") x.secrets = dcNamed(cv.secrets.consul);
        if (isMap(cv.seal) && typeof cv.seal.transit === "string") transits.push([x, cv.seal.transit]);
        m.vaults.push(x);
      }
      for (const [x, tn] of transits) x.transit = (m.vaults.find((y) => y.name === tn) || {}).id || "";
      const vNamed = (n) => m.vaults.find((y) => y.name === n);
      const rp = isMap(vt.replication) ? vt.replication : {};
      for (const md of ["performance", "dr"]) {
        const set = rp[md];
        if (!isMap(set) || !vNamed(set.primary)) continue;
        const pf = isMap(set.paths_filter) ? set.paths_filter : {};
        for (const sn of Array.isArray(set.secondaries) ? set.secondaries : []) {
          const x = vNamed(sn);
          if (!x || x.repl) continue;
          x.repl = vNamed(set.primary).id; x.replMode = md;
          if (md === "performance" && isMap(pf[sn])) x.replFilter = clone(pf[sn]);
        }
      }
      for (const [d, vn] of cas) d.ca = (m.vaults.find((y) => y.name === vn) || {}).id || "";
    };
    if (isMap(t.scenario)) m.scenario = JSON.parse(JSON.stringify(t.scenario));
    if (isMap(t.scenarios) && Object.keys(t.scenarios).length) m.named = true;
    if (!c) {
      m.mesh = false;
      if (isMap(t.vault)) { readVaults(); return m; }
      m.raw = !!(t.nodes || t.networks);
      return m;
    }
    const defs = isMap(c.defaults) ? c.defaults : {};
    const sdOf = (x) => (x && isMap(x.service_template) ? x.service_template : {});
    m.tls = !!defs.tls; m.acl = !!defs.acl;
    m.mesh = !!sdOf(defs).connect && sdOf(defs).connect !== "none";
    if (typeof sdOf(defs).namespace === "string" && normNs(sdOf(defs).namespace)) m.defNs = sdOf(defs).namespace;
    if (isMap(sdOf(defs).fake) && Object.keys(sdOf(defs).fake).length) m.fakeDef = clone(sdOf(defs).fake);
    if (isMap(c.federation) && c.federation.mode) m.fed = String(c.federation.mode);
    const pending = []; // upstreams and routes, resolved once every node exists
    const resolvers = []; // failover candidates: {d, pid, ce}
    const fileEntries = []; // each side's entries: {d, pid, list}
    for (const [dn, dvRaw] of Object.entries(isMap(c.datacenters) ? c.datacenters : {})) {
      const dv = isMap(dvRaw) ? dvRaw : {};
      const dpath = ["consul", "datacenters", dn];
      const d = keep(dc(dn, Number(dv.servers) || 1), dpath);
      d.ui = !!dv.ui;
      if (isMap(dv.connect) && isMap(dv.connect.ca) && typeof dv.connect.ca.vault === "string") cas.push([d, dv.connect.ca.vault]);
      m.dcs.push(d);
      const scope = (pid, sv, spath, sds) => {
        const sd = Object.assign({}, ...sds.map(sdOf), sdOf(sv));
        if (typeof sd.namespace === "string" && normNs(sd.namespace)) (d.inh = d.inh || {})[pid] = sd.namespace;
        for (const [gn, gvRaw] of Object.entries(isMap(sv.gateways) ? sv.gateways : {})) {
          const gv = isMap(gvRaw) ? gvRaw : {};
          if (gvRaw === false) continue;
          const kind = KINDS.includes(gv.kind) ? gv.kind : KINDS.includes(gn) ? gn : "mesh";
          const g = keep(gw(kind, gn, pid), [...spath, "gateways", gn]);
          if (kind !== "mesh" && typeof gv.namespace === "string" && normNs(gv.namespace)) g.ns = gv.namespace;
          d.gws.push(g);
          const e = isMap(gv.entry) ? gv.entry : {};
          const names = kind === "ingress" ? (e.Listeners || []).flatMap((l) => (l && l.Services) || []).map((x) => x && x.Name)
            : kind === "terminating" ? (e.Services || []).map((x) => x && x.Name) : [];
          if (kind === "api") for (const ce of Array.isArray(sv.config_entries) ? sv.config_entries : []) {
            if (ce && ce.Kind === "http-route" && (ce.Parents || []).some((p) => p && p.Name === gn)) for (const r of ce.Rules || []) for (const x of (r && r.Services) || []) names.push(x && x.Name);
          }
          pending.push(() => {
            for (const nm of names) {
              const n = d.nodes.find((x) => x.name === nm && (x.part || "") === pid);
              const x = !n && kind === "terminating" && m.exts.find((y) => y.name === nm && regAt(y, d.id, pid));
              const id = (n || x || {}).id;
              if (id && !g.to.includes(id)) g.to.push(id);
            }
          });
        }
        if (Array.isArray(sv.config_entries)) fileEntries.push({ d, pid, list: JSON.parse(JSON.stringify(sv.config_entries)) });
        // Service-resolvers the builder would write: failovers.
        for (const ce of Array.isArray(sv.config_entries) ? sv.config_entries : []) {
          const fo = isMap(ce) && ce.Kind === "service-resolver" && isMap(ce.Failover) ? ce.Failover["*"] : null;
          if (!isMap(fo) || Object.keys(ce).some((k) => !["Kind", "Name", "Namespace", "ConnectTimeout", "Failover"].includes(k)) || Object.keys(ce.Failover).length !== 1 || Object.keys(fo).join() !== "Targets" || !Array.isArray(fo.Targets) || !fo.Targets.length) continue;
          resolvers.push({ d, pid, ce });
        }
        for (const [nn, nvRaw] of Object.entries(isMap(sv.nodes) ? sv.nodes : {})) {
          const nv = isMap(nvRaw) ? nvRaw : {};
          const n = keep(node(nn, 0, { part: pid, agent: nv.agent === "dataplane" ? "dataplane" : "client", count: Number(nv.count) || 1 }), [...spath, "nodes", nn]);
          if (isMap(nv.service)) {
            const s = nv.service;
            n.svc = { port: Number(s.port) || 0, connect: typeof s.connect === "string" ? s.connect : "", ups: [] };
            if (typeof s.namespace === "string" && s.namespace) n.svc.ns = s.namespace;
            if (isMap(s.fake) && Object.keys(s.fake).length) n.svc.fake = clone(s.fake);

            const inh = typeof sd.connect === "string" ? sd.connect : "";
            if (inh && inh !== (m.mesh ? sdOf(defs).connect : "none")) n.svc.inh = inh;
            for (const [un, uv] of Object.entries(isMap(s.upstreams) ? s.upstreams : {})) {
              const u = isMap(uv) ? uv : { port: uv };
              pending.push(() => {
                let td = d, tp = pid;
                if (u.dc) td = m.dcs.find((x) => x.name === u.dc);
                else if (u.peer) {
                  const p = m.peerings.find((x) => x.name === u.peer);
                  const mine = p && p.dialer === d.id && (p.dialPart || "") === pid;
                  td = p && dcById(m, mine ? p.acceptor : p.dialer);
                  tp = p ? (mine ? p.accPart : p.dialPart) || "" : "";
                }
                if (!td) return;
                if (u.partition) tp = ((td.parts.find((x) => x.name === u.partition) || { id: u.partition === "default" ? "" : null }).id);
                else if (td !== d && !u.peer) tp = "";
                // By name; when several namespaces have it, the upstream's
                // namespace (else the caller's) picks one.
                const cands = td.nodes.filter((x) => x.name === un && (x.part || "") === tp && x.svc);
                const want = normNs(typeof u.namespace === "string" ? u.namespace : nsOf(n));
                const t = cands.find((x) => nsOf(x) === want) || cands[0];
                // Else an external service of that name.
                const x = !t && m.exts.find((y) => y.name === un);
                if (t || x) n.svc.ups.push({ to: (t || x).id, port: Number(u.port) || 0 });
                // An upstream to something the canvas doesn't show (an
                // external service) stays in the file; keep its port taken.
                else (n.svc.held = n.svc.held || []).push(Number(u.port) || 0);
              });
            }
          }
          d.nodes.push(n);
        }
      };
      if (isMap(dv.partitions)) {
        for (const [pn, pvRaw] of Object.entries(dv.partitions)) {
          const ppath = [...dpath, "partitions", pn];
          let pid = "";
          if (pn !== "default") { const p = keep({ id: uid("p"), name: pn }, ppath); d.parts.push(p); pid = p.id; }
          scope(pid, isMap(pvRaw) ? pvRaw : {}, ppath, [defs, dv]);
        }
        if (!("default" in dv.partitions)) scope("", dv, dpath, [defs]);
      } else scope("", dv, dpath, [defs]);
    }
    for (const [pn, pvRaw] of Object.entries(isMap(c.peering) ? c.peering : {})) {
      const pv = isMap(pvRaw) ? pvRaw : {};
      // A side is dc or dc/partition.
      const side = (v) => {
        const [dn, part] = String(v || "").split("/");
        const x = m.dcs.find((y) => y.name === dn);
        if (!x) return null;
        if (!part || part === "default") return [x, ""];
        const pp = x.parts.find((y) => y.name === part);
        return pp ? [x, pp.id] : null;
      };
      const a = side(pv.acceptor), d = side(pv.dialer);
      if (a && d) m.peerings.push(keep({ id: uid("p"), name: pn, acceptor: a[0].id, dialer: d[0].id, accPart: a[1], dialPart: d[1], mesh: !!pv.via_mesh_gateways }, ["consul", "peering", pn]));
    }
    for (const [xn, xvRaw] of Object.entries(isMap(c.external) ? c.external : {})) {
      const xv = isMap(xvRaw) ? xvRaw : {};
      const sv = isMap(xv.service) ? xv.service : {};
      const x = keep(ext(xn, Number(sv.port) || 0), ["consul", "external", xn]);
      if (typeof sv.namespace === "string" && sv.namespace) x.ns = sv.namespace;
      for (const v of Array.isArray(xv.in) ? xv.in : []) {
        const [dn, part] = String(v || "").split("/");
        const d = m.dcs.find((y) => y.name === dn);
        const pid = !d || !part || part === "default" ? "" : (d.parts.find((y) => y.name === part) || {}).id;
        if (d && pid !== undefined) x.in.push({ dc: d.id, part: pid });
      }
      m.exts.push(x);
    }
    pending.forEach((f) => f());
    // A resolver becomes a failover when the builder would write it back
    // the same; else it stays the file's own entry.
    for (const { d, pid, ce } of resolvers) {
      const ns = normNs(typeof ce.Namespace === "string" ? ce.Namespace : "");
      const n = d.nodes.find((x) => x.svc && x.name === ce.Name && (x.part || "") === pid && nsOf(x) === ns);
      const x = !n && m.exts.find((y) => y.name === ce.Name && regAt(y, d.id, pid) && extNs(m, y) === ns);
      const svc = (n || x || {}).id;
      if (!svc) continue;
      const to = [];
      for (const t of ce.Failover["*"].Targets) {
        if (!isMap(t)) { to.length = 0; break; }
        if (typeof t.Peer === "string") {
          const p = m.peerings.find((y) => y.name === t.Peer || peeringName(m, y) === t.Peer);
          const mine = p && p.dialer === d.id && (p.dialPart || "") === pid;
          if (p) to.push(mine ? { dc: p.acceptor, part: p.accPart || "" } : { dc: p.dialer, part: p.dialPart || "" });
        } else if (typeof t.Partition === "string") {
          const pp = t.Partition === "default" ? { id: "" } : d.parts.find((y) => y.name === t.Partition);
          if (pp) to.push({ dc: d.id, part: pp.id });
        } else if (typeof t.Datacenter === "string") {
          const td = m.dcs.find((y) => y.name === t.Datacenter);
          if (td) to.push({ dc: td.id, part: "" });
        }
      }
      if (to.length !== ce.Failover["*"].Targets.length) continue;
      const fo = { id: uid("f"), svc, dc: d.id, part: pid, to, timeout: typeof ce.ConnectTimeout === "string" ? ce.ConnectTimeout : "" };
      if (same(to.map((t) => foTarget(m, fo, t)), ce.Failover["*"].Targets)) m.fails.push(fo);
    }
    // The file's entries that aren't the canvas's own are the user's.
    for (const { d, pid, list } of fileEntries) {
      const gen = entries(m, d, pid).gen.map((x) => x.e);
      const own = list.filter((e) => !gen.some((g) => same(g, e)));
      if (own.length) (d.ces = d.ces || {})[pid] = own;
    }
    readVaults();
    return m;
  }

  // origin says where a thing on the canvas comes from: "" for the file
  // itself, "import" when only the merged file has it (a module or an
  // overlay), "template NAME" for a node made from a template.
  function origin(m, id, src) {
    const p = segs(m, id);
    if (!p) return "";
    if (!has(src, p)) return "import";
    const v = at(src, p);
    return isMap(v) && typeof v.from === "string" ? `template ${v.from}` : "";
  }

  // diffOps turns a canvas edit (model a before, b after) into ops on the
  // file whose own tree is src. It throws when the edit can't go in the
  // file: a thing only an import has, or a value set by a ${var}.
  function diffOps(a, b, src) {
    src = JSON.parse(JSON.stringify(src === undefined ? {} : src));
    const ops = [];
    // How a message names a path: from the datacenter down.
    const label = (p) => dot(p).replace(/^consul\.datacenters\./, "");
    const inSrc = (p) => has(src, p);
    const varAt = (p) => { let v = src; for (const k of p) { if (typeof v === "string") break; if (v === null || typeof v !== "object" || !(k in v)) return ""; v = v[k]; } return typeof v === "string" && v.includes("${") ? v : ""; };
    const da = toDoc(a), db = toDoc(b);
    // Renames first, parents before children, so the paths after them line up.
    const order = (m) => [...m.dcs.map((d) => d.id), ...(m.vaults || []).map((v) => v.id), ...m.dcs.flatMap((d) => d.parts.map((p) => p.id)), ...m.peerings.map((p) => p.id), ...(m.exts || []).map((x) => x.id), ...m.dcs.flatMap((d) => [...d.nodes, ...d.gws].map((x) => x.id))];
    for (const id of order(b)) {
      const pa = segs(a, id), pb = segs(b, id);
      if (!pa || !pb) continue;
      const from = pa[pa.length - 1], to = pb[pb.length - 1];
      if (from === to) continue;
      const path = [...pb.slice(0, -1), from];
      if (!inSrc(path)) throw new Error(`${from} comes from an import, so rename it there.`);
      ops.push({ op: "rename", path, key: to });
      // The same rename in a's doc, keeping its place.
      const parent = at(da, path.slice(0, -1));
      if (isMap(parent) && from in parent) {
        const e = Object.entries(parent).map(([k, v]) => [k === from ? to : k, v]);
        Object.keys(parent).forEach((k) => delete parent[k]);
        e.forEach(([k, v]) => { parent[k] = v; });
      }
      if (isMap(src)) { const sp = at(src, path.slice(0, -1)); if (isMap(sp) && from in sp) { const e = Object.entries(sp).map(([k, v]) => [k === from ? to : k, v]); Object.keys(sp).forEach((k) => delete sp[k]); e.forEach(([k, v]) => { sp[k] = v; }); } }
    }
    const set = (p, v) => {
      const vr = varAt(p);
      if (vr) throw new Error(`${label(p)} is set by ${vr} in the file; change the var instead.`);
      ops.push({ op: "set", path: p, value: v });
    };
    const walk = (x, y, p) => {
      if (same(x, y)) return;
      if (isMap(x) && isMap(y)) {
        for (const k of Object.keys(x)) if (!(k in y) && k !== "config_entries") {
          if (!inSrc([...p, k])) throw new Error(`${k} comes from an import, so remove it there (${label([...p, k])}).`);
          ops.push({ op: "delete", path: [...p, k] });
        }
        for (const [k, v] of Object.entries(y)) if (k !== "config_entries") (k in x ? walk(x[k], v, [...p, k]) : add(v, [...p, k]));
        return;
      }
      set(p, noEntries(y));
    };
    // A new map where the file (or an import) has one already adds to it.
    const add = (v, p) => (isMap(v) && isMap(at(src, p)) ? walk({}, v, p) : set(p, noEntries(v)));
    const noEntries = (v) => (isMap(v) ? Object.fromEntries(Object.entries(v).filter(([k]) => k !== "config_entries").map(([k, x]) => [k, noEntries(x)])) : v);
    walk(da, db, []);
    // Config entries the builder writes (exports, gateway routes, intentions,
    // proxy-defaults): added and removed by kind and name, and only where
    // the file's own entry is the one the builder would write.
    const scopes = (doc) => {
      const out = [];
      for (const [dn, dv] of Object.entries(((doc.consul || {}).datacenters) || {})) {
        if (isMap(dv.partitions)) for (const [pn, pv] of Object.entries(dv.partitions)) out.push([["consul", "datacenters", dn, "partitions", pn], pv]);
        else out.push([["consul", "datacenters", dn], dv]);
      }
      return out;
    };
    const old = new Map(scopes(da).map(([p, v]) => [dot(p), v.config_entries || []]));
    const ek = entryKey;
    for (const [p, v] of scopes(db)) {
      const was = old.get(dot(p)) || [], now = v.config_entries || [];
      const lp = [...p, "config_entries"];
      const file = Array.isArray(at(src, lp)) ? at(src, lp) : [];
      const removes = [], sets = [], appends = [];
      for (const e of was) {
        const n = now.find((x) => ek(x) === ek(e));
        const i = file.findIndex((x) => same(x, e));
        if (!n && i >= 0) removes.push(i);
        else if (n && !same(n, e) && i >= 0) sets.push([i, n]);
      }
      for (const e of now) if (!was.some((x) => ek(x) === ek(e)) && !file.some((x) => isMap(x) && ek(x) === ek(e))) appends.push(e);
      for (const [i, n] of sets) ops.push({ op: "set", path: [...lp, i], value: n });
      for (const i of removes.sort((x, y) => y - x)) ops.push({ op: "remove", path: [...lp, i] });
      for (const e of appends) ops.push({ op: "append", path: lp, value: e });
    }
    return ops;
  }

  // ---------- diff ----------
  // lineDiff compares two texts line by line (LCS): [{t: " "|"-"|"+", s}].
  function lineDiff(a, b) {
    const x = a.split("\n"), y = b.split("\n");
    let i0 = 0;
    while (i0 < x.length && i0 < y.length && x[i0] === y[i0]) i0++;
    let e = 0;
    while (e < x.length - i0 && e < y.length - i0 && x[x.length - 1 - e] === y[y.length - 1 - e]) e++;
    const X = x.slice(i0, x.length - e), Y = y.slice(i0, y.length - e);
    const out = x.slice(0, i0).map((s) => ({ t: " ", s }));
    if (X.length * Y.length > 4e6) {
      X.forEach((s) => out.push({ t: "-", s })); Y.forEach((s) => out.push({ t: "+", s }));
    } else {
      const L = Array.from({ length: X.length + 1 }, () => new Uint32Array(Y.length + 1));
      for (let i = X.length - 1; i >= 0; i--) for (let j = Y.length - 1; j >= 0; j--) L[i][j] = X[i] === Y[j] ? L[i + 1][j + 1] + 1 : Math.max(L[i + 1][j], L[i][j + 1]);
      let i = 0, j = 0;
      while (i < X.length || j < Y.length) {
        if (i < X.length && j < Y.length && X[i] === Y[j]) { out.push({ t: " ", s: X[i] }); i++; j++; }
        else if (i < X.length && (j >= Y.length || L[i + 1][j] >= L[i][j + 1])) out.push({ t: "-", s: X[i++] });
        else out.push({ t: "+", s: Y[j++] });
      }
    }
    x.slice(x.length - e).forEach((s) => out.push({ t: " ", s }));
    return out;
  }

  // ---------- starters ----------
  function starter(name) {
    const m = lab(name);
    const d1 = dc("dc1", 1);
    m.dcs.push(d1);
    switch (name) {
      case "blank": m.name = "lab"; break;
      case "minimal": d1.servers = 3; break;
      case "clients": {
        d1.servers = 3; m.mesh = false;
        d1.nodes.push(node("web", 8080, { count: 2 }), node("client", 0));
        break;
      }
      case "mesh": {
        d1.servers = 1;
        const web = node("web", 8080), api = node("api", 9090);
        d1.nodes.push(web, api);
        web.svc.ups.push({ to: api.id, port: 9091 });
        break;
      }
      case "dataplane": {
        d1.servers = 3;
        const web = node("web", 8080), api = node("api", 9090, { agent: "dataplane" });
        d1.nodes.push(web, api);
        web.svc.ups.push({ to: api.id, port: 9091 });
        break;
      }
      case "meshfed":
      case "peering": {
        const d2 = dc("dc2", 1);
        m.dcs.push(d2);
        d1.gws.push(gw("mesh")); d2.gws.push(gw("mesh"));
        const web = node("web", 8080), api = node("api", 9090);
        d1.nodes.push(web); d2.nodes.push(api);
        web.svc.ups.push({ to: api.id, port: 9091 });
        if (name === "meshfed") { m.fed = "mesh-gateways"; m.tls = true; }
        else m.peerings.push({ id: uid("p"), name: "", acceptor: d2.id, dialer: d1.id, mesh: true });
        break;
      }
      case "vault": {
        m.dcs = [];
        m.vaults.push(vault("east", 3));
        break;
      }
      case "vault-ca": {
        d1.servers = 1;
        const web = node("web", 8080), api = node("api", 9090);
        d1.nodes.push(web, api);
        web.svc.ups.push({ to: api.id, port: 9091 });
        const v = vault("east", 3);
        m.vaults.push(v);
        d1.ca = v.id;
        break;
      }
      default: throw new Error("unknown starter " + name);
    }
    return m;
  }
  const STARTERS = [
    ["blank", "Blank", "One datacenter, one server"],
    ["minimal", "minimal", "Three servers (parker init)"],
    ["clients", "clients", "Servers, services, a client (parker init)"],
    ["mesh", "Service mesh", "web calls api through sidecars"],
    ["dataplane", "Dataplane", "An agentless node in the mesh"],
    ["meshfed", "Mesh federation", "Two dcs federated through mesh gateways"],
    ["peering", "Cluster peering", "dc2 exports api to dc1"],
    ["vault", "Vault", "Three Vault servers on raft"],
    ["vault-ca", "Vault as Connect CA", "dc1's mesh certificates from Vault"],
  ];

  const api = { FAKE_PROFILES, fakeGet, fakeSet, fakeValue, profileOf, entryKey, entries, entryNotes, setEntry, removeEntry, ENTRY_KEYS, pruneFails, svcAt, foSides, foTarget, failover, nameStep, sel, FAULTS, stepFor, addStep, stepText, retarget, ext, extNs, extFor, targetsOf, register, VERSIONS, minorOf, envoyNote, setConsul, normNs, nsOf, gwNs, usesNs, KINDS, GW_NAME, NAME_RE, lab, dc, node, gw, vault, vaultById, joinVault, joinVaults, replRole, find, dcById, partName, sideName, peeringName, peeringOf, peerOf, peeringGateways, route, calls, connectOf, uniqueName, nextDCName, nextPort, nextUpPort, link, joinDCs, remove, rename, toDoc, toYAML, lineOf, pathOf, idOf, checks, starter, STARTERS, ent, segs, fromTree, origin, diffOps, lineDiff, same };
  if (typeof module === "object" && module.exports) module.exports = api;
  else root.Build = api;
})(typeof window === "object" ? window : globalThis);
