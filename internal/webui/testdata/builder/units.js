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
