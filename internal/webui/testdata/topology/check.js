// check.js <graph.json>: lays out a lab's graph with topolayout.js and
// prints, as JSON, where each card went and every drawn hop that would run
// under a card it doesn't connect (before route() bends it round). Run by
// TestTopologyLayout (webui/topology_test.go).
"use strict";
const fs = require("fs");
const path = require("path");
const T = require(path.join(__dirname, "..", "..", "static", "topolayout.js"));

const g = JSON.parse(fs.readFileSync(process.argv[2], "utf8"));
const L = T.layout(g);
const crossings = [];
for (const e of g.edges || []) {
  const hops = [e.from, ...(e.via || []), e.to];
  for (let i = 0; i + 1 < hops.length; i++) {
    const [a, b] = [hops[i], hops[i + 1]];
    if (!L.pos[a] || !L.pos[b]) continue;
    const A = T.anchors(L.pos[a], L.pos[b], 0);
    const under = Object.entries(L.pos).filter(([, c]) => T.blockers(L.pos, a, b, A).includes(c)).map(([n]) => n);
    if (under.length) crossings.push(`${e.kind} ${a} > ${b} under ${under.join(", ")}`);
  }
}
// Cards that overlap each other.
const overlaps = [];
const names = Object.keys(L.pos);
for (let i = 0; i < names.length; i++) {
  for (let j = i + 1; j < names.length; j++) {
    const p = L.pos[names[i]], q = L.pos[names[j]];
    if (Math.abs(p.x - q.x) < T.TW && Math.abs(p.y - q.y) < T.TH) overlaps.push(`${names[i]} ${names[j]}`);
  }
}
process.stdout.write(JSON.stringify({ pos: L.pos, crossings, overlaps }));
