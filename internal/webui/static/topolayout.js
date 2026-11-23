// Topology view layout: where each card goes and how the lines between
// cards run. Plain JS with no DOM, so node can run it too (the Go tests
// check the examples' layouts with it; see webui/topology_test.go).
"use strict";

(function (root) {
const TW = 210, TH = 88, GX = 40, GY = 60, PAD = 28, DCGAP = 120;

// layout places datacenters side by side. Inside one, servers come first;
// with partitions, the default partition (the control plane: its gateway
// carries peering and federation) sits beside them, and the other
// partitions get a row of boxes below, so control-plane and service
// traffic run in rows of their own. Each row runs from the side facing the
// next datacenter (the first one's right, the others' left), so servers
// and gateways face the datacenter they talk to and lines between them
// don't cross other cards.
function layout(g) {
  const nodes = g.nodes || [];
  const dcs = [...new Set(nodes.map((n) => n.dc || "lab"))].sort();
  const pos = {}, regions = [];
  let x = 0, maxH = 0;
  const part = (n) => (n.partition && n.partition !== "default" ? n.partition : "default");
  const plural = (k, s) => `${k} ${s}${k === 1 ? "" : "s"}`;
  const inset = 16;
  const span = (cols) => cols * TW + (cols - 1) * GX;
  dcs.forEach((dc, di) => {
    const right = di === 0 && dcs.length > 1; // lean right, towards the next dc
    const firstRegion = regions.length;
    const dn = nodes.filter((n) => (n.dc || "lab") === dc);
    const servers = dn.filter((n) => n.role === "server");
    const rest = dn.filter((n) => n.role !== "server");
    const parts = [...new Set(rest.map(part))].sort((a, b) => (a === "default" ? -1 : b === "default" ? 1 : a.localeCompare(b)));
    const boxed = parts.length > 1 || (parts.length === 1 && parts[0] !== "default");
    const groups = parts.map((p) => {
      const pn = rest.filter((n) => part(n) === p);
      // Mesh gateways, which talk to other dcs, go first: on the facing side.
      const mesh = (n) => ((n.gateways || []).includes("mesh") ? 1 : 0);
      const gws = pn.filter((n) => n.role === "gateway").sort((a, b) => mesh(b) - mesh(a));
      return { p, gws, others: pn.filter((n) => n.role !== "gateway") };
    });
    const widest = Math.max(servers.length, ...groups.map((gr) => Math.max(gr.gws.length, gr.others.length)));
    const scols = Math.max(1, Math.min(3, boxed ? servers.length : widest));
    // place lays list out in rows of cols cards, each row starting on the
    // side the dc leans to: the first card (server-1, which carries WAN
    // federation and peering, or a mesh gateway) faces the next dc.
    const place = (list, x0, cols, y0) => {
      list.forEach((n, i) => {
        const row = Math.floor(i / cols);
        const col = right ? cols - 1 - (i % cols) : i % cols;
        pos[n.name] = { x: x0 + col * (TW + GX), y: y0 + row * (TH + GY) };
      });
      return list.length ? Math.ceil(list.length / cols) * (TH + GY) : 0;
    };
    let y = PAD + 8;
    let w = span(scols);
    if (!boxed) {
      y += place(servers, x + PAD, scols, y);
      for (const gr of groups) {
        y += place(gr.gws, x + PAD, scols, y);
        y += place(gr.others, x + PAD, scols, y);
      }
    } else {
      const cols = (gr) => Math.max(1, Math.min(2, Math.max(gr.gws.length, gr.others.length)));
      const boxW = (gr) => span(cols(gr)) + 2 * inset;
      // box draws a partition's box at (bx, by) and returns its height.
      const box = (gr, bx, by) => {
        let iy = by + 30;
        iy += place(gr.gws, bx + inset, cols(gr), iy);
        iy += place(gr.others, bx + inset, cols(gr), iy);
        const h = iy - GY + 16 - by;
        regions.push({ part: true, x: bx, y: by, w: boxW(gr), h, label: gr.p, sub: plural(gr.gws.length + gr.others.length, "node") });
        return h;
      };
      const def = groups.find((gr) => gr.p === "default");
      const below = groups.filter((gr) => gr !== def);
      const sw = servers.length ? span(scols) : 0;
      const topW = sw + (def ? (sw ? GX : 0) + boxW(def) : 0);
      const botW = below.reduce((t, gr) => t + boxW(gr), 0) + Math.max(0, below.length - 1) * GX;
      w = Math.max(topW, botW);
      if (def) y = PAD + 30; // room for its box's label
      // Top row: servers and the default partition, its gateway outermost.
      let tx = x + PAD + (right ? w - topW : 0), top = y + (servers.length ? Math.ceil(servers.length / scols) * (TH + GY) : 0);
      const srv = () => { place(servers, tx, scols, y); tx += sw + GX; };
      const dbox = () => { top = Math.max(top, y - 30 + box(def, tx, y - 30) + GY); tx += boxW(def) + GX; };
      if (!def) srv();
      else if (right) { srv(); dbox(); }
      else { dbox(); srv(); }
      // Below: the other partitions.
      let bx = x + PAD + (right ? w - botW : 0), bottom = top;
      for (const gr of below) {
        bottom = Math.max(bottom, top + box(gr, bx, top) + GY);
        bx += boxW(gr) + GX;
      }
      y = bottom;
    }
    const h = y - GY + PAD;
    const gws = dn.filter((n) => n.role === "gateway").length;
    const others = dn.length - servers.length - gws;
    const sub = [servers.length && plural(servers.length, "server"), gws && plural(gws, "gateway"), others && plural(others, "other node")].filter(Boolean).join(" · ");
    // External services and the outside world sit beside the datacenters.
    const beyond = dn.length > 0 && dn.every((n) => n.role === "outside" || n.role === "external");
    regions.splice(firstRegion, 0, { x, y: 0, w: w + 2 * PAD, h, label: beyond ? "outside Consul" : dc, sub: beyond ? plural(dn.length, "node") : sub });
    maxH = Math.max(maxH, h);
    x += w + 2 * PAD + DCGAP;
  });
  // Same height for every datacenter, so they read as peers.
  for (const r of regions) if (!r.part) r.h = maxH;
  return { pos, regions, w: Math.max(0, x - DCGAP), h: maxH };
}

// Anchor points between two cards: sides when they sit apart horizontally,
// top and bottom when stacked.
function anchors(a, b, off) {
  const ac = { x: a.x + TW / 2, y: a.y + TH / 2 }, bc = { x: b.x + TW / 2, y: b.y + TH / 2 };
  if (Math.abs(bc.x - ac.x) > TW * 0.75) {
    const r = bc.x > ac.x ? 1 : -1;
    return { p: [ac.x + (r * TW) / 2, ac.y + off], q: [bc.x - (r * TW) / 2, bc.y + off], h: true };
  }
  const d = bc.y > ac.y ? 1 : -1;
  return { p: [ac.x + off, ac.y + (d * TH) / 2], q: [bc.x + off, bc.y - (d * TH) / 2], h: false };
}
function curve(a, b, horiz) {
  if (horiz) {
    const dx = Math.max(30, Math.abs(b[0] - a[0]) / 2) * Math.sign(b[0] - a[0] || 1);
    return `M${a[0]},${a[1]} C${a[0] + dx},${a[1]} ${b[0] - dx},${b[1]} ${b[0]},${b[1]}`;
  }
  const dy = Math.max(24, Math.abs(b[1] - a[1]) / 2) * Math.sign(b[1] - a[1] || 1);
  return `M${a[0]},${a[1]} C${a[0]},${a[1] + dy} ${b[0]},${b[1] - dy} ${b[0]},${b[1]}`;
}

// cards a straight segment from a to b would run under, other than its ends.
function blockers(pos, a, b, A) {
  const lo = (i) => Math.min(A.p[i], A.q[i]), hi = (i) => Math.max(A.p[i], A.q[i]);
  const [x, y] = A.h ? [0, 1] : [1, 0]; // along, across
  const size = A.h ? [TW, TH] : [TH, TW];
  return Object.entries(pos).filter(([n, c]) => {
    const at = A.h ? [c.x, c.y] : [c.y, c.x];
    return n !== a && n !== b && at[0] + size[0] > lo(x) && at[0] < hi(x) && at[1] + size[1] > lo(y) - 6 && at[1] < hi(y) + 6;
  }).map(([, c]) => c);
}
// between lists the datacenter boxes a hop from a to b would cross that
// hold neither end: the hop doesn't go through those datacenters.
function between(pos, regions, a, b, A) {
  if (!A.h || !regions) return [];
  const inside = (r, c) => c.x >= r.x && c.x + TW <= r.x + r.w && c.y >= r.y && c.y + TH <= r.y + r.h;
  const lo = Math.min(A.p[0], A.q[0]), hi = Math.max(A.p[0], A.q[0]);
  return regions.filter((r) => !r.part && r.x < hi && r.x + r.w > lo && !inside(r, pos[a]) && !inside(r, pos[b]));
}
// route draws a segment, going round the cards in its way: a hop between
// two gateways must not look like it passes through a third one, and a hop
// between two datacenters must not look like it goes through one between
// them, so it runs in a corridor below the datacenters instead.
// lanes holds the detours drawn so far, so two detours don't share a lane.
// regions (from layout) is optional; without it only cards are avoided.
function route(pos, a, b, A, off, lanes, regions) {
  const dcs = between(pos, regions, a, b, A);
  if (dcs.length) {
    // Down the gap beside each card, along the corridor, and up the gap
    // beside the other. Lines that share a pair of cards nest: the upper
    // one turns outside the lower one at both ends.
    const r = Math.sign(A.q[0] - A.p[0]) || 1, k = 12;
    const [px, py] = A.p, [qx, qy] = A.q;
    const x1 = px + r * (20 - off), x2 = qx - r * (20 - off);
    let y = Math.max(...regions.filter((g) => !g.part).map((g) => g.y + g.h)) + 24 + off;
    while (lanes.some((l) => l.h && Math.abs(l.at - y) < 10 && l.lo < Math.max(x1, x2) && Math.min(x1, x2) < l.hi)) y += 14;
    lanes.push({ h: true, at: y, lo: Math.min(x1, x2), hi: Math.max(x1, x2) });
    const d = `M${px},${py} L${x1 - r * k},${py} Q${x1},${py} ${x1},${py + k} L${x1},${y - k} Q${x1},${y} ${x1 + r * k},${y}` +
      ` L${x2 - r * k},${y} Q${x2},${y} ${x2},${y - k} L${x2},${qy + k} Q${x2},${qy} ${x2 + r * k},${qy} L${qx},${qy}`;
    return { d, mid: [(x1 + x2) / 2, y], lane: true, around: dcs, bottom: y };
  }
  const bl = blockers(pos, a, b, A);
  if (!bl.length) return { d: curve(A.p, A.q, A.h), mid: [(A.p[0] + A.q[0]) / 2, (A.p[1] + A.q[1]) / 2], lane: false };
  const free = (at, i) => {
    const lo = Math.min(A.p[i], A.q[i]), hi = Math.max(A.p[i], A.q[i]);
    while (lanes.some((l) => l.h === A.h && Math.abs(l.at - at) < 10 && l.lo < hi && lo < l.hi)) at += 14;
    lanes.push({ h: A.h, at, lo, hi });
    return at;
  };
  if (A.h) {
    const y = free(Math.max(...bl.map((c) => c.y + TH)) + 22 + off, 0), r = Math.sign(A.q[0] - A.p[0]) || 1;
    const [px, py] = A.p, [qx, qy] = A.q;
    return { d: `M${px},${py} C${px + r * 34},${py} ${px + r * 34},${y} ${px + r * 68},${y} L${qx - r * 68},${y} C${qx - r * 34},${y} ${qx - r * 34},${qy} ${qx},${qy}`, mid: [(px + qx) / 2, y], lane: true };
  }
  const x = free(Math.max(...bl.map((c) => c.x + TW)) + 22 + off, 1), r = Math.sign(A.q[1] - A.p[1]) || 1;
  const [px, py] = A.p, [qx, qy] = A.q;
  return { d: `M${px},${py} C${px},${py + r * 30} ${x},${py + r * 10} ${x},${py + r * 40} L${x},${qy - r * 40} C${x},${qy - r * 10} ${qx},${qy - r * 30} ${qx},${qy}`, mid: [x, (py + qy) / 2], lane: true };
}

  const api = { TW, TH, GX, GY, PAD, DCGAP, layout, anchors, curve, blockers, between, route };
  if (typeof module === "object" && module.exports) module.exports = api;
  else root.Topo = api;
})(typeof window === "object" ? window : globalThis);
