// Runs static/fakepanel.js over a lab's graph and fakes (both JSON files)
// and prints, per fake, what the panel would show (see fakepanel_test.go).
"use strict";
const fs = require("fs");
const path = require("path");
const FP = require(path.join(__dirname, "..", "..", "static", "fakepanel.js"));
const g = JSON.parse(fs.readFileSync(process.argv[2], "utf8"));
const fakes = JSON.parse(fs.readFileSync(process.argv[3], "utf8")) || []; // null: no fakes
const out = {};
for (const f of fakes) {
  out[f.node + "/" + f.service] = {
    upstreams: FP.upstreamsOf(g, f.node),
    callers: FP.callersOf(g, fakes, f.node, f.service),
    chain: FP.fakeChain(g, fakes, f.node, f.service),
  };
}
// The header and summary for a fixed set of rows.
const rows = [{ svc: "api", fail: 100, delay: 0 }, { svc: "db", fail: 0, delay: 1000 }, { svc: "web", fail: 0, delay: 0 }, { svc: "", fail: 50, delay: 0 }];
process.stdout.write(JSON.stringify({ fakes: out, header: FP.rqHeader(rows), summary: FP.rqSummary(rows), empty: FP.rqHeader([]) }));
