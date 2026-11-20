// Lab builder model: what the canvas edits, and the YAML it writes.
// Plain JS with no DOM, so node can run it too (the Go tests generate the
// builder's fixtures with it; see webui/builder_test.go).
//
// The YAML is idiomatic, like the examples: settings every service shares
// go in consul.defaults.service_defaults, an upstream says dc: or peer: or
// partition: from where its two ends sit, and the config entries that
// make those calls work (proxy-defaults with local mesh gateways, exports,
// intentions under ACLs, gateway routes) are written for you.
"use strict";

(function (root) {
  // The newest release pins Parker knows (compat.Releases[0]); the daemon's
  // /ui/versions lists the rest.
  const VERSIONS = { consul: "2.1.0", envoy: "1.39.1", dataplane: "2.1.0" };
  const KINDS = ["mesh", "ingress", "terminating", "api"];
  // A gateway's default node name per kind (api gateways are often "edge").
  const GW_NAME = { mesh: "mesh", ingress: "ingress", terminating: "terminating", api: "edge" };
  const NAME_RE = /^[a-z0-9]+(-[a-z0-9]+)*$/;

  let seq = 0;
  const uid = (p) => p + Date.now().toString(36).slice(-4) + (++seq).toString(36);

  // ---------- the model ----------
  // lab: {name, consul, envoy, dataplane, mesh, tls, acl, fed, dcs, peerings}
  //   mesh: services get sidecars (service_defaults connect: sidecar)
  //   fed: "" | "wan" | "mesh-gateways"
  // dc: {id, name, servers, ui, parts: [{id, name}], nodes, gws}
  // node: {id, name, part, agent, count, svc}
  //   part: a part id, "" for the default partition
  //   agent: "client" | "dataplane"
  //   svc: null (a bare client) or {port, connect, ups: [{to, port}]},
  //        held: ports of upstreams the canvas doesn't show
  //     connect: "" (from service_defaults) | "none" | "sidecar" | "tproxy"
  //     to: the called node's id
  // gw: {id, name, kind, part, to: [node ids]}
  // peering: {id, name, acceptor, dialer, accPart, dialPart, mesh}
  //   acceptor, dialer: dc ids; accPart, dialPart: part ids ("" = default)
  //   name "" = dialer-acceptor
  function lab(name) {
    return { name: name || "lab", consul: VERSIONS.consul, envoy: VERSIONS.envoy, dataplane: VERSIONS.dataplane, mesh: true, tls: false, acl: false, fed: "", dcs: [], peerings: [] };
  }
  function dc(name, servers) { return { id: uid("d"), name, servers: servers || 1, ui: false, parts: [], nodes: [], gws: [] }; }
  function node(name, port, opts) {
    const o = opts || {};
    return { id: uid("n"), name, part: o.part || "", agent: o.agent || "client", count: o.count || 1, svc: port ? { port, connect: o.connect || "", ups: [] } : null };
  }
  function gw(kind, name, part) { return { id: uid("g"), name: name || GW_NAME[kind], kind, part: part || "", to: [] }; }

  // ---------- lookups ----------
  function find(m, id) {
    for (const d of m.dcs) {
      if (d.id === id) return { t: "dc", dc: d };
      for (const p of d.parts) if (p.id === id) return { t: "part", dc: d, part: p };
      for (const n of d.nodes) if (n.id === id) return { t: "node", dc: d, node: n };
      for (const g of d.gws) if (g.id === id) return { t: "gw", dc: d, gw: g };
    }
    for (const p of m.peerings) if (p.id === id) return { t: "peering", peering: p };
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
  const ent = (m) => m.dcs.some((d) => d.parts.length > 0);
  // Every name in a dc: nodes and gateways share one namespace.
  const namesIn = (d) => new Set([...d.nodes.map((n) => n.name), ...d.gws.map((g) => g.name)]);

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

  // Every call in the lab, resolved: [{sd, src, td, t, port, r}].
  function calls(m) {
    const out = [];
    for (const sd of m.dcs) for (const src of sd.nodes) for (const u of (src.svc && src.svc.ups) || []) {
      const f = find(m, u.to);
      if (f && f.t === "node" && f.node.svc) out.push({ sd, src, td: f.dc, t: f.node, port: u.port, r: route(m, sd, src, f.dc, f.node) });
    }
    return out;
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
  function uniqueName(d, base) {
    const used = namesIn(d);
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
    throw new Error("Link a service to a service it calls, a gateway to a service, or two datacenters (or partitions in two datacenters).");
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
    }
    for (const d of m.dcs) {
      for (const n of d.nodes) if (n.svc) n.svc.ups = n.svc.ups.filter((u) => !drop.has(u.to));
      for (const g of d.gws) g.to = g.to.filter((t) => !drop.has(t));
    }
  }

  // rename checks a new name and applies it. Upstreams follow by id.
  function rename(m, id, name) {
    const f = find(m, id);
    name = String(name || "").trim();
    if (!NAME_RE.test(name)) throw new Error("Use lowercase letters, digits and single hyphens.");
    if (f.t === "dc") {
      if (m.dcs.some((d) => d !== f.dc && d.name === name)) throw new Error(`There's a ${name} already.`);
      f.dc.name = name;
    } else if (f.t === "part") {
      if (name === "default" || f.dc.parts.some((p) => p !== f.part && p.name === name)) throw new Error(`There's a partition ${name} already.`);
      f.part.name = name;
    } else if (f.t === "node" || f.t === "gw") {
      const self = f.node || f.gw;
      if (self.name !== name && namesIn(f.dc).has(name)) throw new Error(`${f.dc.name} has a ${name} already.`);
      self.name = name;
    } else if (f.t === "peering") {
      f.peering.name = name;
    }
  }

  // ---------- YAML ----------
  // toDoc builds the topology as plain objects, keys in the examples' order.
  function toDoc(m) {
    const doc = { parker: "v1alpha1", name: m.name };
    const nodes = m.dcs.flatMap((d) => d.nodes);
    const anyEnvoy = nodes.some((n) => connectOf(m, n) !== "none") || m.dcs.some((d) => d.gws.length);
    const versions = { consul: m.consul + (ent(m) && !/\+ent$/.test(m.consul) ? "+ent" : "") };
    if (anyEnvoy) versions.envoy = m.envoy;
    if (nodes.some((n) => n.agent === "dataplane")) versions["consul-dataplane"] = m.dataplane;
    doc.versions = versions;

    const consul = {};
    const defs = {};
    if (m.tls) defs.tls = true;
    if (m.acl) defs.acl = true;
    if (nodes.some((n) => n.svc)) defs.service_defaults = m.mesh ? { run: "fake-service", connect: "sidecar" } : { run: "fake-service" };
    if (Object.keys(defs).length) consul.defaults = defs;
    if (m.fed && m.dcs.length > 1) consul.federation = { mode: m.fed };

    const all = calls(m);
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
          (exp[k] = exp[k] || []).includes(c.t.name) || exp[k].push(c.t.name);
        }
        if (Object.keys(exp).length) pe.exports = exp;
        consul.peering[peeringName(m, p)] = pe;
      }
    }

    consul.datacenters = {};
    for (const d of m.dcs) {
      const out = { servers: d.servers };
      if (d.ui) out.ui = true;
      const scopes = [{ id: "", name: "default" }, ...d.parts];
      const bodies = scopes.map((s) => scopeBody(m, d, s, all));
      if (d.parts.length) {
        out.partitions = {};
        scopes.forEach((s, i) => { out.partitions[s.name] = bodies[i]; });
      } else Object.assign(out, bodies[0]);
      consul.datacenters[d.name] = out;
    }
    doc.consul = consul;
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
        const targets = g.to.map((id) => find(m, id)).filter((f) => f && f.t === "node").map((f) => f.node.name);
        let v = {};
        if (g.name !== g.kind) v.kind = g.kind;
        if (g.kind === "ingress" && targets.length) {
          v.entry = { Listeners: targets.map((t, i) => ({ Port: 8080 + i, Protocol: "http", Services: [{ Name: t, Hosts: ["*"] }] })) };
          httpSvcs.push(...targets);
        } else if (g.kind === "terminating" && targets.length) {
          v.entry = { Services: targets.map((t) => ({ Name: t })) };
        } else if (g.kind === "api") {
          v.entry = { Listeners: [{ Name: "http", Port: 8081, Protocol: "http" }] };
          httpSvcs.push(...targets);
          targets.forEach((t) => {
            // One service takes every path; several split by a path prefix.
            const rule = targets.length > 1 ? { Matches: [{ Path: { Match: "prefix", Value: "/" + t } }], Services: [{ Name: t }] } : { Services: [{ Name: t }] };
            const r = { Kind: "http-route", Name: `${g.name}-to-${t}`, Parents: [{ Kind: "api-gateway", Name: g.name, SectionName: "http" }], Rules: [rule] };
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
          const ups = {};
          for (const c of all.filter((c) => c.src === n)) {
            if (c.r.kind === "local") ups[c.t.name] = c.port;
            else if (c.r.kind === "partition") ups[c.t.name] = { port: c.port, partition: c.r.partition };
            else if (c.r.kind === "peer") ups[c.t.name] = { port: c.port, peer: c.r.peer };
            else ups[c.t.name] = { port: c.port, dc: c.r.dc };
          }
          if (Object.keys(ups).length) sv.upstreams = ups;
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
    for (const t of [...new Set(httpSvcs)]) entries.push({ Kind: "service-defaults", Name: t, Protocol: "http" });
    // Exports to other partitions.
    const exp = new Map();
    for (const c of all) if (c.r.kind === "partition" && here(c.td, c.t.part)) {
      const consumers = exp.get(c.t.name) || [];
      const p = partName(c.sd, c.src.part);
      if (!consumers.includes(p)) consumers.push(p);
      exp.set(c.t.name, consumers);
    }
    if (exp.size) entries.push({ Kind: "exported-services", Name: s.name, Services: [...exp].map(([n, cs]) => ({ Name: n, Consumers: cs.map((p) => ({ Partition: p })) })) });
    entries.push(...routes);
    // With ACLs on (deny by default), allow each call.
    if (m.acl) {
      const allow = new Map();
      const add = (name, src) => { const l = allow.get(name) || []; if (!l.some((x) => JSON.stringify(x) === JSON.stringify(src))) l.push(src); allow.set(name, l); };
      for (const c of all) if (here(c.td, c.t.part)) {
        const src = { Name: c.src.name };
        if (c.r.kind === "peer") src.Peer = c.r.peer;
        if (c.r.kind === "partition") src.Partition = partName(c.sd, c.src.part);
        src.Action = "allow";
        add(c.t.name, src);
      }
      for (const g of gws) if (g.kind !== "mesh") for (const id of g.to) {
        const f = find(m, id);
        if (f && f.node && connectOf(m, f.node) !== "none") add(f.node.name, { Name: g.name, Action: "allow" });
      }
      for (const [n, srcs] of allow) entries.push({ Kind: "service-intentions", Name: n, Sources: srcs });
    }
    if (entries.length) body.config_entries = entries;
    return body;
  }

  // Keys whose value is always written as a block, like the examples.
  function blockAt(path) {
    const p = path.join(".");
    return path.length <= 1 && p !== "versions" ||
      /^consul(\.defaults|\.(datacenters|peering)(\.[^.]+)?)?$/.test(p) ||
      /\.(nodes|partitions)(\.[^.]+)?$/.test(p) && !/\.nodes\.[^.]+\.service/.test(p);
  }

  function scalar(v) {
    if (v === true || v === false || typeof v === "number") return String(v);
    const s = String(v);
    if (/^[A-Za-z0-9_./+][A-Za-z0-9_./+-]*$/.test(s) && !/^(true|false|null|yes|no|on|off|~)$/i.test(s) && !/^[-+]?(\d+\.?\d*|\.\d+)([eE][-+]?\d+)?$/.test(s)) return s;
    return JSON.stringify(s);
  }
  const isMap = (v) => v && typeof v === "object" && !Array.isArray(v);
  function mapDepth(v) {
    if (Array.isArray(v)) return Math.max(0, ...v.map(mapDepth));
    if (isMap(v)) return 1 + Math.max(0, ...Object.values(v).map(mapDepth));
    return 0;
  }
  function flow(v) {
    if (Array.isArray(v)) return `[${v.map(flow).join(", ")}]`;
    if (isMap(v)) { const e = Object.entries(v); return e.length ? `{ ${e.map(([k, x]) => `${scalar(k)}: ${flow(x)}`).join(", ")} }` : "{}"; }
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
      const block = isMap(v) ? blockAt(path) || mapDepth(v) > 2 : v.some(isMap) && (/config_entries$/.test(key(path)) || mapDepth(v) > 2);
      if (empty || (!block && pad.length + lead.length + f.length <= 80)) { lines.push(pad + lead + f); return markAll(v, path, start); }
      lines.push(pad + lead.slice(0, -1));
      const inner = ind + (lead.startsWith("- ") ? 4 : 2);
      if (isMap(v)) for (const [k, x] of Object.entries(v)) emit(x, [...path, k], inner, scalar(k) + ": ");
      else v.forEach((x, i) => emitItem(x, [...path, i], inner));
      return mark(path, start);
    }
    // A list item: flow when it is short, else "- " and its keys under it.
    function emitItem(x, path, ind) {
      const pad = " ".repeat(ind);
      const start = lines.length + 1;
      const entry = /config_entries\[\d+\]$/.test(key(path));
      if (!isMap(x) || !Object.keys(x).length || (!entry && mapDepth(x) <= 2 && pad.length + 2 + flow(x).length <= 80)) {
        lines.push(pad + "- " + flow(x));
        return markAll(x, path, start);
      }
      Object.entries(x).forEach(([k, v], i) => emit(v, [...path, k], i ? ind + 2 : ind, (i ? "" : "- ") + scalar(k) + ": "));
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
    return best;
  }

  // checks are problems the builder sees before the daemon does.
  function checks(m) {
    const out = [];
    if (!NAME_RE.test(m.name)) out.push({ message: "The lab name wants lowercase letters, digits and single hyphens.", id: "" });
    if (!m.dcs.length) out.push({ message: "Add a datacenter to start.", id: "" });
    for (const c of calls(m)) if (c.r.kind === "none") {
      const message = c.r.peerParts
        ? `${sideName(m, c.sd.id, c.src.part)}/${c.src.name} calls ${c.t.name} in ${sideName(m, c.td.id, c.t.part)}, but those partitions aren't peered. Link ${sideName(m, c.sd.id, c.src.part)} to ${sideName(m, c.td.id, c.t.part)}.`
        : `${c.sd.name}/${c.src.name} calls ${c.t.name} in ${c.td.name}, but the datacenters are neither federated nor peered. Link ${c.sd.name} to ${c.td.name}.`;
      out.push({ message, id: c.src.id });
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
    }
    const keep = (x, path) => { const id = ids.get(dot(path)); if (id) x.id = id; return x; };
    const m = lab(typeof t.name === "string" ? t.name : "lab");
    const c = isMap(t.consul) ? t.consul : null;
    const v = isMap(t.versions) ? t.versions : {};
    if (v.consul) m.consul = String(v.consul);
    if (v.envoy) m.envoy = String(v.envoy);
    if (v["consul-dataplane"]) m.dataplane = String(v["consul-dataplane"]);
    if (!c) { m.raw = !!(t.nodes || t.networks); m.mesh = false; return m; }
    const defs = isMap(c.defaults) ? c.defaults : {};
    const sdOf = (x) => (x && isMap(x.service_defaults) ? x.service_defaults : {});
    m.tls = !!defs.tls; m.acl = !!defs.acl;
    m.mesh = !!sdOf(defs).connect && sdOf(defs).connect !== "none";
    if (isMap(c.federation) && c.federation.mode) m.fed = String(c.federation.mode);
    const pending = []; // upstreams and routes, resolved once every node exists
    for (const [dn, dvRaw] of Object.entries(isMap(c.datacenters) ? c.datacenters : {})) {
      const dv = isMap(dvRaw) ? dvRaw : {};
      const dpath = ["consul", "datacenters", dn];
      const d = keep(dc(dn, Number(dv.servers) || 1), dpath);
      d.ui = !!dv.ui;
      m.dcs.push(d);
      const scope = (pid, sv, spath, sds) => {
        const sd = Object.assign({}, ...sds.map(sdOf), sdOf(sv));
        for (const [gn, gvRaw] of Object.entries(isMap(sv.gateways) ? sv.gateways : {})) {
          const gv = isMap(gvRaw) ? gvRaw : {};
          if (gvRaw === false) continue;
          const kind = KINDS.includes(gv.kind) ? gv.kind : KINDS.includes(gn) ? gn : "mesh";
          const g = keep(gw(kind, gn, pid), [...spath, "gateways", gn]);
          d.gws.push(g);
          const e = isMap(gv.entry) ? gv.entry : {};
          const names = kind === "ingress" ? (e.Listeners || []).flatMap((l) => (l && l.Services) || []).map((x) => x && x.Name)
            : kind === "terminating" ? (e.Services || []).map((x) => x && x.Name) : [];
          if (kind === "api") for (const ce of Array.isArray(sv.config_entries) ? sv.config_entries : []) {
            if (ce && ce.Kind === "http-route" && (ce.Parents || []).some((p) => p && p.Name === gn)) for (const r of ce.Rules || []) for (const x of (r && r.Services) || []) names.push(x && x.Name);
          }
          pending.push(() => { for (const nm of names) { const n = d.nodes.find((x) => x.name === nm && (x.part || "") === pid); if (n && !g.to.includes(n.id)) g.to.push(n.id); } });
        }
        for (const [nn, nvRaw] of Object.entries(isMap(sv.nodes) ? sv.nodes : {})) {
          const nv = isMap(nvRaw) ? nvRaw : {};
          const n = keep(node(nn, 0, { part: pid, agent: nv.agent === "dataplane" ? "dataplane" : "client", count: Number(nv.count) || 1 }), [...spath, "nodes", nn]);
          if (isMap(nv.service)) {
            const s = nv.service;
            n.svc = { port: Number(s.port) || 0, connect: typeof s.connect === "string" ? s.connect : "", ups: [] };
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
                const t = td.nodes.find((x) => x.name === un && (x.part || "") === tp && x.svc);
                if (t) n.svc.ups.push({ to: t.id, port: Number(u.port) || 0 });
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
    pending.forEach((f) => f());
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
    const order = (m) => [...m.dcs.map((d) => d.id), ...m.dcs.flatMap((d) => d.parts.map((p) => p.id)), ...m.peerings.map((p) => p.id), ...m.dcs.flatMap((d) => [...d.nodes, ...d.gws].map((x) => x.id))];
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
    const ek = (e) => `${e.Kind}/${e.Name}`;
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
  ];

  const api = { VERSIONS, minorOf, envoyNote, setConsul, KINDS, GW_NAME, NAME_RE, lab, dc, node, gw, find, dcById, partName, sideName, peeringName, peeringOf, peerOf, peeringGateways, route, calls, connectOf, uniqueName, nextDCName, nextPort, nextUpPort, link, joinDCs, remove, rename, toDoc, toYAML, lineOf, pathOf, idOf, checks, starter, STARTERS, ent, segs, fromTree, origin, diffOps, lineDiff, same };
  if (typeof module === "object" && module.exports) module.exports = api;
  else root.Build = api;
})(typeof window === "object" ? window : globalThis);
