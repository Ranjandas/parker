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
