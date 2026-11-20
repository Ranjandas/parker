// Writes the lab builder's fixtures: every starter, plus labs built with
// the builder's own edits (links, gateways, partitions, ACLs). Run by
// builder_test.go: node gen.js OUTDIR.
"use strict";
const fs = require("fs");
const path = require("path");
const B = require("../../static/buildmodel.js");

const out = process.argv[2];
const write = (name, m) => fs.writeFileSync(path.join(out, name + ".yaml"), B.toYAML(m).text);

for (const [name] of B.STARTERS) write(name, B.starter(name));

// meshfed and peering again, built step by step as on the canvas.
for (const how of ["mesh-gateways", "peering"]) {
  const m = B.lab(how === "peering" ? "peering" : "meshfed");
  const d1 = B.dc("dc1", 1), d2 = B.dc("dc2", 1);
  m.dcs.push(d1, d2);
  const web = B.node("web", 8080), api = B.node("api", 9090);
  d1.nodes.push(web); d2.nodes.push(api);
  B.joinDCs(m, d1.id, d2.id, how);
  B.link(m, web.id, api.id);
  write("steps-" + m.name, m);
}

// Gateways of every kind, with ACLs.
{
  const m = B.starter("mesh"); m.name = "gateways"; m.acl = true;
  const d = m.dcs[0], [web, api] = d.nodes;
  const legacy = B.node("legacy", 7070); d.nodes.push(legacy);
  const ing = B.gw("ingress"), term = B.gw("terminating"), edge = B.gw("api");
  d.gws.push(ing, term, edge);
  B.link(m, ing.id, api.id); B.link(m, term.id, legacy.id);
  B.link(m, edge.id, api.id); B.link(m, edge.id, web.id);
  B.link(m, web.id, legacy.id);
  write("gateways", m);
}

// Admin partitions, with ACLs.
{
  const m = B.lab("partitions"); m.acl = true;
  const d = B.dc("dc1", 3); m.dcs.push(d);
  const pa = { id: "pa", name: "team-a" }; d.parts.push(pa);
  d.gws.push(B.gw("mesh"), B.gw("mesh", "mesh", pa.id));
  const fe = B.node("frontend", 8080), bi = B.node("billing", 9000, { part: pa.id, count: 2 });
  d.nodes.push(fe, bi);
  B.link(m, fe.id, bi.id);
  write("partitions", m);
}

// WAN federation and peering with ACLs; a dataplane both ways.
{
  const m = B.starter("peering"); m.name = "peering-acl"; m.acl = true; write("peering-acl", m);
  const f = B.lab("wan"); f.acl = true;
  const d1 = B.dc("dc1", 3), d2 = B.dc("dc2", 1); f.dcs.push(d1, d2);
  const web = B.node("web", 8080), api = B.node("api", 9090, { agent: "dataplane" });
  d1.nodes.push(web, B.node("client", 0)); d2.nodes.push(api);
  B.joinDCs(f, d1.id, d2.id, "wan");
  B.link(f, web.id, api.id);
  write("wan-acl", f);
}

// Peering between partitions of two datacenters, linked partition to
// partition on the canvas, with ACLs.
{
  const m = B.lab("peering-partitions"); m.acl = true;
  const d1 = B.dc("dc1", 1), d2 = B.dc("dc2", 1); m.dcs.push(d1, d2);
  const pa = { id: "pa", name: "team-a" }, pb = { id: "pb", name: "team-b" };
  d1.parts.push(pa); d2.parts.push(pb);
  const fe = B.node("frontend", 8080, { part: pa.id }), bi = B.node("billing", 9000, { part: pb.id });
  d1.nodes.push(fe); d2.nodes.push(bi);
  B.link(m, pa.id, pb.id);
  B.link(m, fe.id, bi.id);
  write("peering-partitions", m);
}

// Namespaces: across a partition and over a peering, with a terminating
// gateway in a namespace, and ACLs.
{
  const m = B.lab("namespaces"); m.acl = true;
  const d1 = B.dc("dc1", 1), d2 = B.dc("dc2", 1); m.dcs.push(d1, d2);
  const pa = { id: "pa", name: "part-a" }, pb = { id: "pb", name: "part-b" };
  d1.parts.push(pa); d2.parts.push(pb);
  const web = B.node("web", 8080, { part: pa.id }), api = B.node("api", 9090, { part: pa.id });
  const legacy = B.node("legacy", 7070, { part: pa.id }), billing = B.node("billing", 9000, { part: pb.id });
  web.svc.ns = "app"; api.svc.ns = "backend"; legacy.svc.ns = "app"; billing.svc.ns = "payments";
  d1.nodes.push(web, api, legacy); d2.nodes.push(billing);
  const tgw = B.gw("terminating", "terminating", pa.id); tgw.ns = "app";
  d1.gws.push(tgw);
  B.link(m, tgw.id, legacy.id);
  B.link(m, pa.id, pb.id);
  B.link(m, web.id, api.id); B.link(m, web.id, legacy.id); B.link(m, web.id, billing.id);
  write("namespaces", m);
}
