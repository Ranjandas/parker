// Unit checks of buildmodel.js helpers, run by builder_test.go
// (TestBuildModelUnits): node units.js. Exits non-zero on the first
// failure.
"use strict";
const assert = require("assert");
const B = require("../../static/buildmodel.js");

const vt = {
  releases: [{ consul: "2.1.0", envoy: "1.39.1", dataplane: "2.1.0" }, { consul: "1.22.7", envoy: "1.35.12", dataplane: "1.9.12" }],
  envoy: { "2.1": ["1.39", "1.38", "1.37", "1.36"], "1.22": ["1.35", "1.34", "1.33", "1.32"] },
};

// Versions: new labs start on the newest pins; changing Consul brings an
// Envoy that fits.
{
  const m = B.lab("v");
  assert.strictEqual(m.consul, B.VERSIONS.consul);
  assert.strictEqual(B.envoyNote(m, vt), "");
  B.setConsul(m, "1.22.7", vt);
  assert.deepStrictEqual([m.consul, m.envoy, m.dataplane], ["1.22.7", "1.35.12", "1.9.12"]);
  m.envoy = "1.39.1";
  assert.match(B.envoyNote(m, vt), /Consul 1.22 supports Envoy 1.35, 1.34, 1.33, 1.32, not 1.39/);
  B.setConsul(m, "1.22.6+ent", vt);
  assert.strictEqual(m.envoy, "1.35.12", "a patch of a known minor takes its pair");
  m.envoy = "1.34.1";
  B.setConsul(m, "1.22.7", vt);
  assert.strictEqual(m.envoy, "1.34.1", "an Envoy that still fits is kept");
  B.setConsul(m, "path:/my/consul", vt);
  assert.strictEqual(B.envoyNote(m, vt), "", "a path build isn't judged");
  assert.strictEqual(B.minorOf("v2.1.0+ent"), "2.1");
}
console.log("ok");

// External services: registered where they're linked, fronted by
// terminating gateways only, and read back from a file.
{
  const m = B.lab("x");
  const d = B.dc("dc1", 1); m.dcs.push(d);
  const pa = { id: "pa", name: "part-a" }; d.parts.push(pa);
  const web = B.node("web", 8080, { part: pa.id }); d.nodes.push(web);
  const api = B.ext("api", 9000); m.exts.push(api);
  assert.match(B.link(m, web.id, api.id), /registered in dc1\/part-a.*Link a terminating gateway in dc1\/part-a/);
  assert.deepStrictEqual(api.in, [{ dc: d.id, part: pa.id }]);
  assert.ok(B.checks(m).some((c) => /no terminating gateway in dc1\/part-a fronts it/.test(c.message)));
  const ing = B.gw("ingress", "", pa.id), tgw = B.gw("terminating", "", pa.id);
  d.gws.push(ing, tgw);
  assert.throws(() => B.link(m, ing.id, api.id), /Only a terminating gateway/);
  assert.match(B.link(m, tgw.id, api.id), /fronts api/);
  assert.ok(!B.checks(m).some((c) => /fronts it/.test(c.message)));
  assert.throws(() => B.link(m, api.id, pa.id), /registered in dc1\/part-a already/);
  assert.match(B.link(m, d.id, api.id), /registered in dc1's catalog/);
  api.ns = "app";
  const doc = B.toDoc(m);
  assert.deepStrictEqual(doc.consul.external, { api: { service: { port: 9000, namespace: "app" }, in: ["dc1/part-a", "dc1"] } });
  assert.deepStrictEqual(doc.consul.datacenters.dc1.partitions["part-a"].gateways.terminating.entry, { Services: [{ Name: "api", Namespace: "app" }] });
  const back = B.fromTree(JSON.parse(JSON.stringify(doc)), m);
  assert.ok(B.same(B.toDoc(back), doc), "reads back");
  assert.strictEqual(back.exts[0].id, api.id, "keeps its id");
  assert.throws(() => B.rename(m, api.id, "web"), /dc1\/part-a has a web already/);
  B.remove(m, pa.id);
  assert.deepStrictEqual(api.in, [{ dc: d.id, part: "" }]);
  B.remove(m, api.id);
  assert.strictEqual(m.exts.length, 0);
}
console.log("ok externals");

// Scenarios: steps made from the canvas, selectors that follow renames,
// and the file's own steps kept as they are.
{
  const m = B.lab("s");
  const d = B.dc("dc1", 1); m.dcs.push(d);
  const pa = { id: "pa", name: "team-a" }; d.parts.push(pa);
  const web = B.node("web", 8080, { part: pa.id }), api = B.node("api", 9090, { part: pa.id });
  d.nodes.push(web, api);
  B.link(m, web.id, api.id);
  assert.strictEqual(B.sel(m, web.id), "dc1/team-a/web");
  assert.strictEqual(B.sel(m, "srv:" + d.id), "dc1/servers");
  B.addStep(m, B.stepFor(m, d.id, "wait"));
  B.addStep(m, B.stepFor(m, api.id, "stop"));
  B.addStep(m, B.stepFor(m, web.id, "call"));
  B.addStep(m, B.stepFor(m, api.id, "healthy"));
  assert.deepStrictEqual(m.scenario.steps, [
    { wait: "dc1 ready" },
    { stop: "dc1/team-a/api" },
    { assert: { call: { on: "dc1/team-a/web", reaches: "api" }, within: "60s" } },
    { assert: { consul: { service_healthy: "api" }, within: "60s" } },
  ]);
  assert.strictEqual(B.stepFor(m, "srv:" + d.id, "call"), null, "servers answer no calls");
  B.rename(m, api.id, "backend");
  B.rename(m, pa.id, "team-b");
  B.rename(m, d.id, "east");
  assert.deepStrictEqual(m.scenario.steps.map((s) => s.stop || s.wait || (s.assert.call || s.assert.consul)), [
    "east ready", "east/team-b/backend", { on: "east/team-b/web", reaches: "backend" }, { service_healthy: "backend" }]);
  const y = B.toYAML(m).text;
  assert.match(y, /\nscenario:\n  steps:\n    - wait: east ready\n    - stop: east\/team-b\/backend\n    - assert: { call: { on: east\/team-b\/web, reaches: backend }, within: 60s }\n/);
  const back = B.fromTree(JSON.parse(JSON.stringify(B.toDoc(m))), m);
  assert.ok(B.same(B.toDoc(back), B.toDoc(m)), "reads back");
  assert.strictEqual(B.stepText({ assert: { exec: { on: "dc1/web", run: "x", output: "ok" }, within: "9s" } }), "exec on dc1/web shows ok within 9s");
}
console.log("ok scenario");

// Failover: a link between two copies of a service, and an external
// service failing over between its registrations.
{
  const m = B.lab("f"); m.acl = true;
  const d1 = B.dc("dc1", 1), d2 = B.dc("dc2", 1); m.dcs.push(d1, d2);
  const web = B.node("web", 8080), a1 = B.node("api", 9090), a2 = B.node("api", 9090);
  d1.nodes.push(web, a1); d2.nodes.push(a2);
  B.link(m, web.id, a1.id);
  assert.match(B.link(m, a1.id, a2.id), /api in dc1 fails over to dc2.*Federate or peer dc1 and dc2/);
  assert.ok(B.checks(m).some((c) => /fails over to dc2, but nothing joins them/.test(c.message)));
  assert.ok(!B.toDoc(m).consul.datacenters.dc1.config_entries.some((e) => e.Kind === "service-resolver"), "no resolver without a way there");
  B.joinDCs(m, d1.id, d2.id, "peering");
  const doc = B.toDoc(m);
  const e1 = doc.consul.datacenters.dc1.config_entries, e2 = doc.consul.datacenters.dc2.config_entries;
  assert.deepStrictEqual(e1.find((e) => e.Kind === "service-resolver"), { Kind: "service-resolver", Name: "api", Failover: { "*": { Targets: [{ Peer: "dc1-dc2" }] } } });
  assert.ok(e1.some((e) => e.Kind === "proxy-defaults"), "mesh gateways in local mode");
  assert.deepStrictEqual(e2.find((e) => e.Kind === "service-intentions"), { Kind: "service-intentions", Name: "api", Sources: [{ Name: "web", Peer: "dc1-dc2", Action: "allow" }] });
  assert.deepStrictEqual(Object.values(doc.consul.peering)[0].exports, { dc2: ["api"] });
  assert.deepStrictEqual(doc.consul.datacenters.dc1.nodes.web.service.upstreams, { api: 9091 }, "the call itself stays local");
  assert.throws(() => B.link(m, a1.id, a2.id), /fails over to dc2 already/);
  const back = B.fromTree(JSON.parse(JSON.stringify(doc)));
  assert.strictEqual(back.fails.length, 1, "the resolver reads back as a failover");
  assert.ok(B.same(B.toDoc(back), doc));
  B.remove(m, a2.id);
  assert.strictEqual(m.fails.length, 0, "no target, no failover");
}
console.log("ok failover");

// Config entries: the canvas's, the user's own (which win), and notes on
// what the user typed.
{
  const m = B.lab("c"); m.acl = true;
  const d = B.dc("dc1", 1); m.dcs.push(d);
  const web = B.node("web", 8080), api = B.node("api", 9090); d.nodes.push(web, api);
  B.link(m, web.id, api.id);
  let es = B.entries(m, d, "");
  assert.deepStrictEqual(es.gen.map((x) => [x.e.Kind, x.why, x.over]), [["service-intentions", "calls to api, with ACLs on", false]]);
  B.setEntry(d, "", { Kind: "service-defaults", Name: "api", Protocol: "http" });
  B.setEntry(d, "", { Kind: "service-intentions", Name: "api", Sources: [{ Name: "web", Action: "deny" }] });
  assert.throws(() => B.setEntry(d, "", { Kind: "service-defaults", Name: "api" }), /service-defaults\/\/api here already/);
  es = B.entries(m, d, "");
  assert.ok(es.gen[0].over, "the user's intention overrides the canvas's");
  assert.deepStrictEqual(B.toDoc(m).consul.datacenters.dc1.config_entries.map((e) => e.Kind + (e.Sources ? ":" + e.Sources[0].Action : "")), ["service-defaults", "service-intentions:deny"]);
  // Read back: the user's entries stay theirs, the canvas's are the canvas's.
  const back = B.fromTree(JSON.parse(JSON.stringify(B.toDoc(m))));
  assert.deepStrictEqual(back.dcs[0].ces[""].map((e) => e.Kind), ["service-defaults", "service-intentions"]);
  const plain = B.lab("p"); plain.acl = true; const pd = B.dc("dc1", 1); plain.dcs.push(pd);
  const w2 = B.node("web", 8080), a2 = B.node("api", 9090); pd.nodes.push(w2, a2); B.link(plain, w2.id, a2.id);
  assert.strictEqual(B.fromTree(JSON.parse(JSON.stringify(B.toDoc(plain)))).dcs[0].ces, undefined, "generated entries aren't the user's");
  B.removeEntry(d, "", 1);
  assert.ok(!B.entries(m, d, "").gen[0].over);
  assert.deepStrictEqual(B.entryNotes({ Kind: "service-defaults", Name: "a", protocol: "http" }), ["protocol: a service-defaults has no such field (fields use Consul's casing, e.g. Protocol)."]);
  assert.deepStrictEqual(B.entryNotes({ Kind: "service-defualts", Name: "a" }), ["Kind: service-defualts isn't a config entry kind Parker knows; check the spelling."]);
  assert.deepStrictEqual(B.entryNotes({ file: "x.hcl" }), []);
  assert.deepStrictEqual(B.entryNotes({ file: "x.hcl", Name: "y" }), ["file: takes a path (.hcl or .json, relative to the lab file) and no other keys."]);
  assert.strictEqual(B.entryKey({ file: "x.hcl" }), "file:x.hcl");
}
console.log("ok entries");
