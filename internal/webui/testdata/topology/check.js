// check.js <graph.json>: lays out a lab's graph with topolayout.js and
// prints, as JSON, where each card went and every drawn hop that would run
// under a card it doesn't connect (before route() bends it round), and
// the hops that run round a datacenter they don't go through, and where
// the internet's globes went. Run by
// TestTopologyLayout (webui/topology_test.go).
"use strict";
const fs = require("fs");
const path = require("path");
const T = require(path.join(__dirname, "..", "..", "static", "topolayout.js"));

const g = JSON.parse(fs.readFileSync(process.argv[2], "utf8"));
const L = T.layout(g);
const crossings = [], around = [];
for (const e of g.edges || []) {
  const hops = [e.from, ...(e.via || []), e.to];
  for (let i = 0; i + 1 < hops.length; i++) {
    const [a, b] = [hops[i], hops[i + 1]];
    if (!L.pos[a] || !L.pos[b]) continue;
    const A = T.anchors(L.pos[a], L.pos[b], 0);
    const under = Object.entries(L.pos).filter(([, c]) => T.blockers(L.pos, a, b, A).includes(c)).map(([n]) => n);
    if (under.length) crossings.push(`${e.kind} ${a} > ${b} under ${under.join(", ")}`);
    // A hop that would cross another datacenter runs below it instead,
    // and its legs down and up mustn't run under a card either.
    const R = T.route(L.pos, a, b, A, 0, [], L.regions);
    if (!R.around) continue;
    around.push(`${e.kind} ${a} > ${b} around ${R.around.map((r) => r.label).join(", ")}`);
    for (let i = 0; i + 1 < R.pts.length; i++) {
      const [[x, y0], [x1, y1]] = [R.pts[i], R.pts[i + 1]];
      if (x !== x1) continue;
      const hit = Object.entries(L.pos).filter(([n, c]) => n !== a && n !== b && x > c.x - 6 && x < c.x + T.TW + 6 && c.y + T.TH > Math.min(y0, y1) && c.y < Math.max(y0, y1)).map(([n]) => n);
      if (hit.length) crossings.push(`${e.kind} ${a} > ${b} detour under ${hit.join(", ")}`);
    }
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
process.stdout.write(JSON.stringify({ pos: L.pos, crossings, overlaps, around, entries: L.entries }));
