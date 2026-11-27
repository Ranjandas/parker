// The builder's round trip over files: node roundtrip.js IN OUT. IN is
// [{name, tree, source}] (each example checked, its merged and own
// trees); OUT gets, for each, the ops an untouched canvas makes (none,
// or the test fails), and the ops and resulting doc for a few edits made
// as on the canvas (servers, a new service, a call to it, a rename; a Vault
// cluster's servers and name). With a third argument (verify), IN is
// [{name, tree, want}] and OUT says whether the patched file reads back
// as the edited canvas.
"use strict";
const fs = require("fs");
const B = require("../../static/buildmodel.js");

const [inp, out, mode] = process.argv.slice(2);
const items = JSON.parse(fs.readFileSync(inp, "utf8"));
const clone = (m) => JSON.parse(JSON.stringify(m));
const res = [];
for (const it of items) {
  const r = { name: it.name };
  try {
    const m = B.fromTree(it.tree);
    if (mode === "verify") {
      r.same = B.same(B.toDoc(m), it.want);
      if (!r.same) r.got = B.toDoc(m);
    } else {
      r.noop = B.diffOps(m, clone(m), it.source);
      r.raw = !!m.raw;
      const e = clone(m);
      const d = e.dcs.find((x) => B.origin(e, x.id, it.source) === "");
      if (d) {
        d.servers = d.servers === 3 ? 5 : 3;
        const n = B.node(B.uniqueName(d, "added"), B.nextPort(e, 7000), { part: d.parts.length ? d.parts[0].id : "" });
        d.nodes.push(n);
        const caller = d.nodes.find((x) => x.svc && x !== n && (x.part || "") === (n.part || "") && B.connectOf(e, x) !== "none" && B.origin(e, x.id, it.source) === "");
        if (caller) {
          B.link(e, caller.id, n.id);
          // A rename too, where no scenario names the node.
          if (!it.tree.scenario) B.rename(e, caller.id, B.uniqueName(d, caller.name + "-renamed"));
        }
        r.ops = B.diffOps(m, e, it.source);
        r.want = B.toDoc(e);
      }
      // A Vault cluster: more servers (none for a dev one), and a rename
      // where no scenario names it.
      const v = (e.vaults || []).find((x) => B.origin(e, x.id, it.source) === "");
      if (v) {
        if (!v.dev && v.storage !== "file") v.servers = v.servers === 3 ? 5 : 3;
        if (!it.tree.scenario) B.rename(e, v.id, v.name + "-renamed");
        r.ops = B.diffOps(m, e, it.source);
        r.want = B.toDoc(e);
      }
    }
  } catch (err) { r.error = String(err.stack || err); }
  res.push(r);
}
fs.writeFileSync(out, JSON.stringify(res));
