// Lab builder (#/build): drag components onto a canvas, link them, and
// read the lab's YAML as it changes. The model and the YAML writer are in
// buildmodel.js; this file draws them and handles the edits. The YAML is
// checked by the daemon's Validate (what `parker validate` does) as you go.
"use strict";

const BI = {
  dc: '<svg width="15" height="15" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6"><rect x="1.8" y="1.8" width="12.4" height="12.4" rx="3" stroke-dasharray="3 2"/></svg>',
  part: '<svg width="15" height="15" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6"><rect x="1.8" y="1.8" width="12.4" height="12.4" rx="3"/><path d="M8 2v12"/></svg>',
  server: '<svg width="15" height="15" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6"><rect x="2" y="2.5" width="12" height="4.5" rx="1.2"/><rect x="2" y="9" width="12" height="4.5" rx="1.2"/><path d="M4.5 4.75h.01M4.5 11.25h.01" stroke-width="2.2" stroke-linecap="round"/></svg>',
  client: '<svg width="15" height="15" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6"><rect x="2.5" y="2.5" width="11" height="11" rx="2"/><path d="M5.5 8h5"/></svg>',
  dp: '<svg width="15" height="15" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6"><rect x="2.5" y="2.5" width="11" height="11" rx="2" stroke-dasharray="2.5 2"/><circle cx="8" cy="8" r="2"/></svg>',
  svc: '<svg width="15" height="15" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6"><circle cx="8" cy="8" r="5.5"/><circle cx="8" cy="8" r="2"/></svg>',
  mesh: '<svg width="15" height="15" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6"><path d="M8 1.8l5.4 3.1v6.2L8 14.2l-5.4-3.1V4.9z"/><path d="M5.5 8h5M8.5 6l2 2-2 2"/></svg>',
  ingress: '<svg width="15" height="15" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6"><path d="M1.5 8h8M7 5l3 3-3 3M12.5 2.5v11"/></svg>',
  terminating: '<svg width="15" height="15" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6"><path d="M3.5 2.5v11M6 8h8.5M11.5 5l3 3-3 3"/></svg>',
  api: '<svg width="15" height="15" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6"><path d="M5 3L2 8l3 5M11 3l3 5-3 5M9.5 3l-3 10"/></svg>',
  up: '<svg width="15" height="15" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6"><path d="M2.5 11.5C5 11.5 5 4.5 8 4.5h5.5M11 2l2.5 2.5L11 7"/></svg>',
  peer: '<svg width="15" height="15" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6"><circle cx="4.5" cy="8" r="2.5"/><circle cx="11.5" cy="8" r="2.5"/><path d="M7 8h2"/></svg>',
  fed: '<svg width="15" height="15" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6"><circle cx="8" cy="8" r="6"/><path d="M2 8h12M8 2c2 2 2 10 0 12M8 2c-2 2-2 10 0 12"/></svg>',
};

const PALETTE = [
  ["Structure", [["dc", "Datacenter", "A Consul datacenter", "dc"], ["part", "Admin partition", "Groups nodes in a dc", "part", true]]],
  ["Consul", [["servers", "Server group", "servers: 1, 3 or 5", "server"], ["client", "Client node", "A client agent, no service", "client"], ["dp", "Dataplane node", "Agentless, consul-dataplane", "dp"]]],
  ["Services and mesh", [["svc", "Service", "fake-service with a sidecar", "svc"], ["mesh", "Mesh gateway", "Cross-dc, peering, partitions", "mesh"], ["ingress", "Ingress gateway", "North-south entry point", "ingress"], ["terminating", "Terminating gateway", "Reach services outside the mesh", "terminating"], ["api", "API gateway", "HTTP routes into the mesh", "api"]]],
  ["Connections", [["up", "Upstream", "A service calls a service", "up"], ["peer", "Cluster peering", "Peer two datacenters", "peer"], ["fed", "WAN federation", "Federate the datacenters", "fed"]]],
];

const SVC_NAMES = ["web", "api", "billing", "orders", "cart", "search", "auth", "ledger", "inventory", "payments"];
const BK = "parker-builder";

const BL = {
  m: null, sel: "", mode: "select", from: "", pick: null,
  undo: [], redo: [], schema: null, fresh: "",
  y: null, prev: [], val: { seq: 0, busy: false, res: null, err: "" }, vt: null,
};

// ---------- state ----------
function bInit() {
  if (BL.m) return;
  try { const s = JSON.parse(localStorage.getItem(BK) || "null"); if (s && s.dcs) BL.m = s; } catch (e) { /* none saved */ }
  if (!BL.m) BL.m = Build.starter("mesh");
  fetch("/ui/schema.json", { credentials: "same-origin" }).then((r) => r.json()).then((s) => { BL.schema = s; if (S.route.view === "build") bInspector(); }).catch(() => {});
}
function bSave() { try { localStorage.setItem(BK, JSON.stringify(BL.m)); } catch (e) { /* not saved */ } }

// bEdit applies an edit, with undo. fn may return a message to toast.
function bEdit(fn) {
  const before = JSON.stringify(BL.m);
  let msg;
  try { msg = fn(BL.m); } catch (e) { BL.m = JSON.parse(before); toast(e.message); return false; }
  if (JSON.stringify(BL.m) !== before) { BL.undo.push(before); if (BL.undo.length > 100) BL.undo.shift(); BL.redo = []; bSave(); }
  if (msg && msg !== "pick") toast(msg);
  bRender();
  return msg;
}
function bHistory(back) {
  const from = back ? BL.undo : BL.redo, to = back ? BL.redo : BL.undo;
  if (!from.length) { toast(back ? "Nothing to undo" : "Nothing to redo"); return; }
  to.push(JSON.stringify(BL.m));
  BL.m = JSON.parse(from.pop());
  if (!Build.find(BL.m, BL.sel)) BL.sel = "";
  bSave(); bRender();
}

// ---------- schema docs ----------
function sdef(def, prop) {
  const d = BL.schema && BL.schema.$defs && BL.schema.$defs[def];
  return (d && d.properties && d.properties[prop]) || {};
}
// The first sentence of a field's doc, for the hint under it.
function sdoc(def, prop) {
  const t = (sdef(def, prop).description || "").replace(/\s+/g, " ").trim();
  const m = t.match(/^.*?[.!?](\s|$)/);
  return m ? m[0].trim() : t;
}
const senum = (def, prop, fallback) => sdef(def, prop).enum || fallback;

// ---------- page ----------
function renderBuild() {
  bInit();
  const v = $("#view");
  if (!$("#builder", v)) {
    v.innerHTML = `<div class="builder" id="builder">
      <div class="palette" id="bpal" aria-label="Components">
        ${PALETTE.map(([g, items]) => `<h6>${g}</h6>` + items.map(([k, n, d, ic, ent]) => `<div class="pi" draggable="true" data-pk="${k}" tabindex="0" role="button" title="Drag onto the canvas, or press Enter to add it to the selected datacenter"><div class="ico">${BI[ic]}</div><div><b>${n}${ent ? ' <span class="ent">ENT</span>' : ""}</b><small>${d}</small></div></div>`).join("")).join("")}
        <h6>Start from</h6>
        <div class="starters">${Build.STARTERS.map(([k, n, d]) => `<button class="btn sm" data-starter="${k}" title="${esc(d)}">${esc(n)}</button>`).join("")}</div>
      </div>
      <div class="bwrap">
        <div class="bstage" id="bstage">
          <div class="btools">
            <div class="seg" id="bmodes"><button data-bmode="select" title="Select and drag (V)">Select <span class="faint mono">V</span></button><button data-bmode="link" title="Click two things to link them (L)">Link <span class="faint mono">L</span></button></div>
            <div class="seg"><button data-bact="undo" title="Undo (Ctrl+Z)">↶</button><button data-bact="redo" title="Redo (Ctrl+Shift+Z)">↷</button></div>
            <div class="seg"><button data-bact="lab" title="Lab settings">Lab settings</button></div>
            <span class="bhint" id="bhint"></span>
          </div>
          <div class="bworld" id="bworld"></div>
        </div>
        <div class="card insp" id="binsp"></div>
      </div>
      <div class="yaml">
        <div class="hd"><b id="byname"></b><span id="bvchip"></span><div style="flex:1"></div><span class="faint" style="font-size:11.5px">read-only</span></div>
        <div class="code" id="bcode" tabindex="0" aria-label="Generated YAML"></div>
        <div class="val" id="bval"></div>
        <div class="yfoot">
          <button class="btn sm" data-bact="copy">Copy</button><button class="btn sm" data-bact="download">Download</button>
          <div class="grow"></div>
          <button class="btn primary sm" data-bact="up" id="bup" title="Copy the command that brings the lab up">${ICON.term}<span id="bupcmd"></span></button>
        </div>
      </div>
    </div>`;
    bBind();
  }
  bRender();
}

function bRender() {
  if (!$("#builder")) return;
  $$("#bmodes button").forEach((b) => b.classList.toggle("on", b.dataset.bmode === BL.mode));
  $("#bhint").textContent = BL.mode === "link"
    ? (BL.from ? `Linking from ${bLabel(BL.from)}: click what it should reach. Esc cancels.` : "Click a service, gateway or datacenter, then what it links to.")
    : "Drag components in. Drag from a dot to link.";
  bCanvas();
  bInspector();
  bYAML();
}

const bLabel = (id) => { const f = Build.find(BL.m, id); return f ? (f.node || f.gw || f.part || f.dc || {}).name || "it" : "it"; };

// ---------- canvas ----------
const DCW = 288, GAP = 76, X0 = 28, Y0 = 112, CW = 236, CH = 48, ROW = 60;

function bLayout() {
  const P = { dc: {}, it: {}, part: {} };
  BL.m.dcs.forEach((d, i) => {
    const x = X0 + i * (DCW + GAP);
    let y = Y0 + 30;
    const cx = x + (DCW - CW) / 2;
    P.it["srv:" + d.id] = { x: cx, y, w: CW }; y += ROW;
    const scope = (pid, ix, w) => {
      d.gws.filter((g) => (g.part || "") === pid).forEach((g) => { P.it[g.id] = { x: ix, y, w }; y += ROW; });
      d.nodes.filter((n) => (n.part || "") === pid).forEach((n) => { P.it[n.id] = { x: ix, y, w }; y += ROW; });
    };
    if (d.parts.length) {
      const sub = (pid, name) => {
        const top = y; y += 30;
        scope(pid, x + 26, DCW - 52);
        y = Math.max(y, top + 84) + 8;
        P.part[d.id + ":" + pid] = { x: x + 12, y: top, w: DCW - 24, h: y - top - 8, name, pid };
        y += 10;
      };
      sub("", "default");
      d.parts.forEach((p) => sub(p.id, p.name));
    } else scope("", cx, CW);
    P.dc[d.id] = { x, y: Y0, w: DCW, h: Math.max(330, y - Y0 + 70) };
  });
  return P;
}

function bKind(m, n) {
  if (n.agent === "dataplane") return "dp";
  return n.svc ? "svc" : "client";
}

function bCard(id, p, ico, cls, name, sub, opts) {
  const o = opts || {};
  const on = BL.sel === id, src = BL.from === id;
  return `<div class="bn ${on ? "sel" : ""} ${src ? "linksrc" : ""} ${BL.fresh === id ? "new" : ""} ${o.bad ? "bad" : ""}" data-bid="${esc(id)}" style="left:${p.x}px;top:${p.y}px;width:${p.w}px" tabindex="0" role="button" aria-label="${esc(name)}">
    <div class="ico ${cls}">${ico}</div><div style="min-width:0"><b>${esc(name)}</b><small>${esc(sub)}</small></div>${o.port ? `<span class="port" data-port="${esc(id)}" title="Drag to link"></span>` : ""}</div>`;
}

function bCanvas() {
  const m = BL.m, P = bLayout();
  let h = "";
  const problems = new Set(((BL.val.res && BL.val.res.problems) || []).map((p) => p.id).filter(Boolean));
  for (const d of m.dcs) {
    const r = P.dc[d.id];
    const svcs = d.nodes.filter((n) => n.svc).length;
    h += `<div class="breg ${BL.sel === d.id ? "sel" : ""} ${BL.from === d.id ? "linksrc" : ""} ${problems.has(d.id) ? "bad" : ""}" data-drop="${d.id}:" style="left:${r.x}px;top:${r.y}px;width:${r.w}px;height:${r.h}px">
      <div class="lbl" data-bid="${d.id}" tabindex="0" role="button">${esc(d.name)}<span>${d.servers} server${d.servers > 1 ? "s" : ""} · ${svcs} service${svcs !== 1 ? "s" : ""}</span><span class="port dcport" data-port="${d.id}" title="Drag to another datacenter to federate or peer"></span></div>
      <div class="drop">Drop to add to ${esc(d.name)}</div></div>`;
    for (const k of Object.keys(P.part)) if (k.startsWith(d.id + ":")) {
      const pr = P.part[k], pid = pr.pid;
      h += `<div class="bpart ${pid && BL.sel === pid ? "sel" : ""}" data-drop="${d.id}:${pid}" style="left:${pr.x}px;top:${pr.y}px;width:${pr.w}px;height:${pr.h}px"><div class="plbl" ${pid ? `data-bid="${pid}" role="button" tabindex="0"` : ""}>${BI.part}${esc(pr.name)}</div></div>`;
    }
    h += bCard("srv:" + d.id, P.it["srv:" + d.id], BI.server, "srv", d.servers > 1 ? `server-1 … server-${d.servers}` : "server-1", `servers: ${d.servers}${d.ui ? " · UI" : ""}`);
    for (const g of d.gws) {
      const n = g.to.length;
      h += bCard(g.id, P.it[g.id], BI[g.kind], "gw", g.name, `${g.kind} gateway${n ? ` · ${n} route${n > 1 ? "s" : ""}` : ""}`, { port: g.kind !== "mesh", bad: problems.has(g.id) });
    }
    for (const n of d.nodes) {
      const k = bKind(m, n);
      const sub = n.svc ? `:${n.svc.port} · ${Build.connectOf(m, n) === "none" ? "no mesh" : Build.connectOf(m, n)}${n.agent === "dataplane" ? " · dataplane" : ""}` : "client agent";
      h += bCard(n.id, P.it[n.id], BI[k], k === "svc" || k === "dp" ? "svc" : "cl", n.name + (n.count > 1 ? ` ×${n.count}` : ""), sub, { port: !!n.svc, bad: problems.has(n.id) });
    }
  }
  const ax = X0 + m.dcs.length * (DCW + GAP);
  const maxH = Math.max(330, ...m.dcs.map((d) => P.dc[d.id].h));
  h += `<div class="adddc" data-drop="new" style="left:${ax}px;top:${Y0}px;width:190px;height:${maxH}px">${BI.dc}<span>Drop a Datacenter</span></div>`;
  const W = ax + 230, H = Y0 + maxH + 60;

  // Edges: federation and peering over the regions; calls and routes between cards.
  let s = `<svg class="edges" width="${W}" height="${H}"><defs>${["accent", "fed", "peer", "bad", "ink-2"].map((c) => `<marker id="bah-${c}" viewBox="0 0 10 10" refX="8" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse"><path d="M0,0 L10,5 L0,10 z" fill="var(--${c})"/></marker>`).join("")}</defs>`;
  const pill = (x, y, text, color, id) => `<g class="bpill" transform="translate(${x},${y})"${id ? ` data-bid="${id}"` : ""}><rect x="${-(text.length * 3.3 + 10)}" y="-10" width="${text.length * 6.6 + 20}" height="20" rx="10" fill="var(--panel)" stroke="var(--${color})" stroke-opacity=".6"/><text x="0" y="4" text-anchor="middle" font-family="var(--f-mono)" font-size="10.5" fill="var(--${color})">${esc(text)}</text></g>`;
  const labels = [];
  const top = (d) => { const r = P.dc[d.id]; return [r.x + r.w / 2, r.y]; };
  if (m.fed && m.dcs.length > 1) {
    for (let i = 0; i + 1 < m.dcs.length; i++) {
      const [ax1, ay] = top(m.dcs[i]), [bx] = top(m.dcs[i + 1]);
      s += `<path d="M${ax1},${ay} C${ax1},${ay - 44} ${bx},${ay - 44} ${bx},${ay}" fill="none" stroke="var(--fed)" stroke-width="1.8" stroke-dasharray="5 5"/>`;
      labels.push(pill((ax1 + bx) / 2, ay - 33, m.fed === "wan" ? "wan federation" : "federated via mesh gateways", "fed", "lab"));
    }
  }
  m.peerings.forEach((p, i) => {
    const a = Build.dcById(m, p.dialer), b = Build.dcById(m, p.acceptor);
    if (!a || !b) return;
    const [x1, y1] = top(a), [x2] = top(b);
    const lift = 78 + i * 14;
    s += `<path d="M${x1 + 30},${y1} C${x1 + 30},${y1 - lift} ${x2 - 30},${y1 - lift} ${x2 - 30},${y1}" fill="none" stroke="var(--peer)" stroke-width="2" ${BL.sel === p.id ? 'stroke-width="3"' : ""}/>`;
    labels.push(pill((x1 + x2) / 2, y1 - lift * 0.75, `peering ${Build.peeringName(m, p)}`, "peer", p.id));
  });
  const mid = (r) => [r.x + r.w / 2, r.y + CH / 2];
  const curve = (A, B, side) => {
    if (side) { const o = Math.max(40, Math.abs(B[1] - A[1]) * 0.35) * side; return `M${A[0]},${A[1]} C${A[0] + o},${A[1]} ${B[0] + o},${B[1]} ${B[0]},${B[1]}`; }
    const dx = Math.max(40, Math.abs(B[0] - A[0]) * 0.45) * (B[0] > A[0] ? 1 : -1);
    return `M${A[0]},${A[1]} C${A[0] + dx},${A[1]} ${B[0] - dx},${B[1]} ${B[0]},${B[1]}`;
  };
  for (const c of Build.calls(m)) {
    const a = P.it[c.src.id], b = P.it[c.t.id];
    if (!a || !b) continue;
    const color = { local: "accent", partition: "accent", dc: "fed", peer: "peer", none: "bad" }[c.r.kind];
    let A, B, d;
    if (Math.abs(a.x - b.x) < 5) { A = [a.x + a.w, a.y + CH / 2]; B = [b.x + b.w, b.y + CH / 2]; d = curve(A, B, 1); }
    else if (b.x > a.x) { A = [a.x + a.w, a.y + CH / 2]; B = [b.x, b.y + CH / 2]; d = curve(A, B); }
    else { A = [a.x, a.y + CH / 2]; B = [b.x + b.w, b.y + CH / 2]; d = curve(A, B); }
    s += `<path d="${d}" fill="none" stroke="var(--${color})" stroke-opacity=".14" stroke-width="7"/><path d="${d}" fill="none" stroke="var(--${color})" stroke-width="2" class="flow" marker-end="url(#bah-${color})"/>`;
    const lx = Math.abs(a.x - b.x) < 5 ? A[0] + Math.max(40, Math.abs(B[1] - A[1]) * 0.35) * 0.75 : (A[0] + B[0]) / 2;
    labels.push(pill(lx, (A[1] + B[1]) / 2, `${c.t.name} :${c.port}`, color, c.src.id));
  }
  for (const d of m.dcs) for (const g of d.gws) for (const id of g.to) {
    const a = P.it[g.id], b = P.it[id];
    if (!a || !b) continue;
    const A = [a.x, a.y + CH / 2], B = [b.x, b.y + CH / 2];
    s += `<path d="${curve(A, B, -1)}" fill="none" stroke="var(--ink-2)" stroke-width="1.6" stroke-dasharray="4 4" marker-end="url(#bah-ink-2)"/>`;
  }
  s += `<path id="blinkline" d="" fill="none" stroke="var(--accent)" stroke-width="2" stroke-dasharray="5 4"/></svg>`;
  let pick = "";
  if (BL.pick) {
    const a = Build.dcById(m, BL.pick.a), b = Build.dcById(m, BL.pick.b);
    pick = `<div class="card bpick" style="left:${BL.pick.x}px;top:${BL.pick.y}px"><div class="hd"><h3>Join ${esc(a.name)} and ${esc(b.name)}</h3></div>
      <button data-join="peering">${BI.peer}<span><b>Cluster peering</b><small>${esc(a.name)} dials, ${esc(b.name)} accepts and exports</small></span></button>
      <button data-join="wan">${BI.fed}<span><b>WAN federation</b><small>Servers gossip over the wan network</small></span></button>
      <button data-join="mesh-gateways">${BI.mesh}<span><b>Federation via mesh gateways</b><small>Turns on TLS; a mesh gateway per dc</small></span></button>
      <button data-join="" class="cancel"><span>Cancel</span></button></div>`;
  }
  const world = $("#bworld");
  world.innerHTML = s + h + `<svg class="edges labels" width="${W}" height="${H}">${labels.join("")}</svg>` + pick;
  world.style.width = W + "px"; world.style.height = H + "px";
  BL.fresh = "";
}

// ---------- inspector ----------
// A field. doc (from the schema) shows while the field is hovered or
// focused; note always shows.
const fld = (label, input, doc, forId, note) => `<div class="fld"><label${forId ? ` for="${forId}"` : ""}${doc ? ` title="${esc(doc)}"` : ""}>${label}</label><div>${input}${note ? `<div class="hint">${esc(note)}</div>` : ""}${doc ? `<div class="hint doc">${esc(doc)}</div>` : ""}</div></div>`;
const seg = (f, opts, cur) => `<div class="seg">${opts.map(([v, l]) => `<button type="button" data-f="${f}" data-v="${esc(v)}" class="${String(cur) === String(v) ? "on" : ""}">${esc(l)}</button>`).join("")}</div>`;
const stepper = (f, n) => `<span class="stepper"><button type="button" data-f="${f}" data-v="-1" aria-label="Fewer">−</button><span>${n}</span><button type="button" data-f="${f}" data-v="1" aria-label="More">+</button></span>`;
const txt = (f, v, ph) => `<input id="bf-${f}" data-f="${f}" value="${esc(v)}" ${ph ? `placeholder="${esc(ph)}"` : ""} spellcheck="false" autocomplete="off">`;

function bInspector() {
  const el = $("#binsp");
  if (!el) return;
  const m = BL.m;
  el.hidden = !BL.sel;
  if (!BL.sel) return;
  const f = BL.sel === "lab" ? null : BL.sel.startsWith("srv:") ? { t: "dc", dc: Build.dcById(m, BL.sel.slice(4)) } : Build.find(m, BL.sel);
  let title, where = "", body;
  const yes = [["true", "on"], ["false", "off"]];
  if (!f || !f.t || (f.t === "dc" && !f.dc)) {
    title = "Lab";
    const fedOpts = [["", "none"], ...senum("Federation", "mode", ["wan", "mesh-gateways"]).map((x) => [x, x])];
    body = fld("Name", txt("name", m.name), "", "bf-name") +
      fld("Consul", txt("consul", m.consul), "", "bf-consul", Build.ent(m) ? "Partitions need Consul Enterprise; the YAML pins the +ent build." : "") +
      fld("Envoy", txt("envoy", m.envoy), "", "bf-envoy") +
      fld("Mesh", seg("mesh", yes, m.mesh), "Services get sidecars by default (service_defaults connect: sidecar).") +
      fld("TLS", seg("tls", yes, m.tls), sdoc("Inheritable", "tls")) +
      fld("ACLs", seg("acl", yes, m.acl), sdoc("Inheritable", "acl"), "", m.acl ? "The builder writes the intentions each call needs." : "") +
      (m.dcs.length > 1 ? fld("Federation", `<select data-f="fed" id="bf-fed">${fedOpts.map(([v, l]) => `<option value="${v}" ${m.fed === v ? "selected" : ""}>${l}</option>`).join("")}</select>`, sdoc("Federation", "mode"), "bf-fed") : "");
  } else if (f.t === "dc") {
    const d = f.dc;
    title = `Datacenter · ${esc(d.name)}`;
    body = fld("Name", txt("name", d.name), "", "bf-name") +
      fld("Servers", stepper("servers", d.servers), sdoc("Datacenter", "servers"), "", d.servers % 2 ? "" : "An even number of servers can't break a tie; use 1, 3 or 5.") +
      fld("Web UI", seg("ui", yes, d.ui), sdoc("Datacenter", "ui")) +
      `<div class="row-btns"><button class="btn sm" data-bact="linkfrom">${BI.peer}Federate or peer…</button><button class="btn sm danger" data-bact="remove">Remove ${esc(d.name)}</button></div>`;
  } else if (f.t === "part") {
    title = `Partition · ${esc(f.part.name)}`; where = f.dc.name;
    body = fld("Name", txt("name", f.part.name), "", "bf-name") + `<div class="hint">${esc(sdoc("Datacenter", "partitions"))} Calls between partitions get exports and local mesh gateways.</div>` +
      `<div class="row-btns"><button class="btn sm danger" data-bact="remove">Remove partition</button></div>`;
  } else if (f.t === "node") {
    const n = f.node;
    title = `${n.svc ? "Service" : "Node"} · ${esc(n.name)}`; where = f.dc.name + (n.part ? "/" + Build.partName(f.dc, n.part) : "");
    const agents = senum("Node", "agent", ["client", "dataplane"]).filter((a) => a !== "none");
    body = fld("Name", txt("name", n.name), "", "bf-name") +
      fld("Agent", seg("agent", agents.map((a) => [a, a]), n.agent), sdoc("Node", "agent")) +
      fld("Count", stepper("count", n.count), sdoc("Node", "count")) +
      fld("Service", seg("svc", yes, !!n.svc), "", "", n.svc ? "" : "A bare client agent: it joins the cluster and runs no service.");
    if (n.svc) {
      const conn = [["", `default (${m.mesh ? "sidecar" : "none"})`], ...senum("Service", "connect", ["none", "sidecar", "tproxy"]).map((x) => [x, x])];
      const ups = n.svc.ups.map((u, i) => {
        const t = Build.find(m, u.to);
        if (!t || !t.node) return "";
        const r = Build.route(m, f.dc, n, t.dc, t.node);
        const how = { local: "", partition: `partition ${r.partition}`, dc: `dc ${r.dc}`, peer: `peer ${r.peer}`, none: `${t.dc.name}, not joined` }[r.kind];
        return `<div class="up ${r.kind === "none" ? "bad" : ""}">${BI.up}<span>${esc(t.node.name)}${how ? ` <i>${esc(how)}</i>` : ""}</span><input data-f="upport" data-i="${i}" value="${u.port}" aria-label="Local port for ${esc(t.node.name)}" inputmode="numeric"><button class="x" data-f="rmup" data-i="${i}" title="Remove">✕</button></div>`;
      }).join("");
      body += fld("Port", `<input id="bf-port" data-f="port" value="${n.svc.port}" inputmode="numeric">`, sdoc("Service", "port"), "bf-port") +
        fld("Connect", `<select data-f="connect" id="bf-connect">${conn.map(([v, l]) => `<option value="${v}" ${n.svc.connect === v ? "selected" : ""}>${l}</option>`).join("")}</select>`, sdoc("Service", "connect"), "bf-connect") +
        `<div class="fld" style="align-items:start"><label title="${esc(sdoc("Service", "upstreams"))}">Upstreams</label><div class="ups">${ups || '<span class="hint">None yet.</span>'}<button class="btn sm" data-bact="linkfrom">+ Call a service</button><div class="hint doc">dc:, peer: or partition: follow from where the two services sit.</div></div></div>`;
    }
    body += `<div class="row-btns"><button class="btn sm danger" data-bact="remove">Remove ${esc(n.name)}</button></div>`;
  } else if (f.t === "gw") {
    const g = f.gw;
    title = `${g.kind[0].toUpperCase() + g.kind.slice(1)} gateway · ${esc(g.name)}`; where = f.dc.name + (g.part ? "/" + Build.partName(f.dc, g.part) : "");
    const what = { mesh: "Carries calls between datacenters, peers and partitions; the builder sets proxy-defaults MeshGateway mode local where calls need it.", ingress: "Each route is an HTTP listener (8080, 8081, ...) for one service.", terminating: "Fronts services outside the mesh; the services it fronts run without a sidecar.", api: "Listens on 8081; each route is an http-route (by path prefix when there are several)." }[g.kind];
    const routes = g.to.map((id, i) => { const t = Build.find(m, id); return t && t.node ? `<div class="up">${BI.up}<span>${esc(t.node.name)}</span><button class="x" data-f="rmroute" data-i="${i}" title="Remove">✕</button></div>` : ""; }).join("");
    body = fld("Name", txt("name", g.name), "", "bf-name") + `<div class="hint">${esc(what)}</div>` +
      (g.kind !== "mesh" ? `<div class="fld" style="align-items:start"><label>${g.kind === "terminating" ? "Fronts" : "Routes to"}</label><div class="ups">${routes || '<span class="hint">None yet.</span>'}<button class="btn sm" data-bact="linkfrom">+ Route to a service</button></div></div>` : "") +
      `<div class="row-btns"><button class="btn sm danger" data-bact="remove">Remove gateway</button></div>`;
  } else if (f.t === "peering") {
    const p = f.peering, a = Build.dcById(m, p.acceptor), d = Build.dcById(m, p.dialer);
    title = `Peering · ${esc(Build.peeringName(m, p))}`;
    body = fld("Name", txt("name", p.name, `${d.name}-${a.name}`), sdoc("Consul", "peering").replace(/,? keyed by.*$/, "."), "bf-name") +
      fld("Dialer", `<b class="mono">${esc(d.name)}</b>`, sdoc("Peering", "dialer")) +
      fld("Acceptor", `<b class="mono">${esc(a.name)}</b> <button class="btn sm" data-f="swap" style="margin-left:6px">Swap</button>`, sdoc("Peering", "acceptor")) +
      fld("Via mesh gw", seg("pmesh", yes, p.mesh), sdoc("Peering", "via_mesh_gateways")) +
      `<div class="hint">Exports follow from the calls: a service called over this peering is exported by its datacenter.</div>` +
      `<div class="row-btns"><button class="btn sm danger" data-bact="remove">Remove peering</button></div>`;
  }
  el.innerHTML = `<div class="hd"><h3>${title}</h3><div class="grow"></div><span class="faint mono" style="font-size:11px">${esc(where)}</span><button class="btn icon ghost sm" data-bact="deselect" title="Close (Esc)" aria-label="Close">✕</button></div><div class="bd">${body}</div>`;
}

// bField applies one inspector field.
function bField(el) {
  const f = el.dataset.f, v = el.dataset.v !== undefined ? el.dataset.v : el.value, i = +el.dataset.i;
  const sel = BL.sel;
  const num = (x, lo, hi) => { const n = parseInt(x, 10); if (!(n >= lo && n <= hi)) throw new Error(`Want a number from ${lo} to ${hi}.`); return n; };
  bEdit((m) => {
    const t = sel === "lab" ? null : sel.startsWith("srv:") ? { t: "dc", dc: Build.dcById(m, sel.slice(4)) } : Build.find(m, sel);
    if (!t) {
      if (f === "name") { if (!Build.NAME_RE.test(v)) throw new Error("Use lowercase letters, digits and single hyphens."); m.name = v; }
      else if (f === "consul" || f === "envoy") m[f] = v.trim() || m[f];
      else if (f === "mesh" || f === "tls" || f === "acl") m[f] = v === "true";
      else if (f === "fed") { if (v) return Build.joinDCs(m, m.dcs[0].id, m.dcs[1].id, v); m.fed = ""; }
      return;
    }
    const id = t.t === "dc" ? t.dc.id : sel;
    if (f === "name") return Build.rename(m, id, v);
    if (t.t === "dc") {
      if (f === "servers") t.dc.servers = Math.max(1, Math.min(7, t.dc.servers + +v));
      if (f === "ui") t.dc.ui = v === "true";
    } else if (t.t === "node") {
      const n = t.node;
      if (f === "agent") n.agent = v;
      if (f === "count") n.count = Math.max(1, Math.min(9, n.count + +v));
      if (f === "svc") n.svc = v === "true" ? n.svc || { port: Build.nextPort(m, 8080), connect: "", ups: [] } : null;
      if (f === "port") n.svc.port = num(v, 1, 65535);
      if (f === "connect") n.svc.connect = v;
      if (f === "upport") n.svc.ups[i].port = num(v, 1, 65535);
      if (f === "rmup") n.svc.ups.splice(i, 1);
      // A node with no service is called by nothing.
      if (f === "svc" && v === "false") for (const d of m.dcs) { d.nodes.forEach((x) => { if (x.svc) x.svc.ups = x.svc.ups.filter((u) => u.to !== n.id); }); d.gws.forEach((g) => { g.to = g.to.filter((x) => x !== n.id); }); }
    } else if (t.t === "gw") {
      if (f === "rmroute") t.gw.to.splice(i, 1);
    } else if (t.t === "peering") {
      const p = t.peering;
      if (f === "pmesh") p.mesh = v === "true";
      if (f === "swap") [p.acceptor, p.dialer] = [p.dialer, p.acceptor];
    }
  });
}

// ---------- adding ----------
function bAdd(kind, dcId, pid) {
  BL.pick = null;
  if (kind === "up" || kind === "peer" || kind === "fed") {
    bMode("link");
    toast(kind === "up" ? "Click the calling service, then the one it calls. Or drag from a service's dot." : "Click one datacenter, then the other. Or drag from a datacenter's dot.");
    return;
  }
  bEdit((m) => {
    if (kind === "dc") {
      const d = Build.dc(Build.nextDCName(m), 1);
      if (m.fed === "mesh-gateways" || m.peerings.length) d.gws.push(Build.gw("mesh"));
      m.dcs.push(d);
      BL.sel = d.id; BL.fresh = "srv:" + d.id;
      return `Added ${d.name}.` + (m.dcs.length > 1 && !m.fed ? " Link it to another datacenter to federate or peer them." : "");
    }
    const d = Build.dcById(m, dcId) || m.dcs[0];
    if (!d) throw new Error("Add a datacenter first.");
    pid = pid || "";
    if (kind === "part") {
      const used = new Set(d.parts.map((p) => p.name));
      const name = ["team-a", "team-b", "team-c", "team-d"].find((x) => !used.has(x)) || "team-" + (used.size + 1);
      const p = { id: "p" + Math.random().toString(36).slice(2, 8), name };
      d.parts.push(p);
      BL.sel = p.id;
      return `Added partition ${name} to ${d.name}. Partitions need Consul Enterprise.`;
    }
    if (kind === "servers") {
      d.servers = d.servers < 3 ? 3 : d.servers < 5 ? 5 : 1;
      BL.sel = "srv:" + d.id; BL.fresh = BL.sel;
      return `${d.name} now has ${d.servers} server${d.servers > 1 ? "s" : ""}.`;
    }
    if (Build.KINDS.includes(kind)) {
      if (kind === "mesh" && d.gws.some((g) => g.kind === "mesh" && (g.part || "") === pid)) throw new Error(`${d.name} has a mesh gateway there already.`);
      const g = Build.gw(kind, Build.uniqueName(d, Build.GW_NAME[kind]), pid);
      d.gws.push(g);
      BL.sel = g.id; BL.fresh = g.id;
      return kind === "mesh" ? `Added a mesh gateway to ${d.name}.` : `Added the ${kind} gateway ${g.name}. Link it to the services it should reach.`;
    }
    const all = new Set(m.dcs.flatMap((x) => [...x.nodes.map((n) => n.name), ...x.gws.map((g) => g.name)]));
    let name = kind === "client" ? Build.uniqueName(d, "client") : SVC_NAMES.find((x) => !all.has(x)) || Build.uniqueName(d, "svc");
    name = Build.uniqueName(d, name);
    const n = Build.node(name, kind === "client" ? 0 : Build.nextPort(m, 8080 + 10 * m.dcs.reduce((a, x) => a + x.nodes.length, 0) % 1000), { part: pid, agent: kind === "dp" ? "dataplane" : "client" });
    d.nodes.push(n);
    BL.sel = n.id; BL.fresh = n.id;
    return `Added ${name} to ${d.name}${pid ? "/" + Build.partName(d, pid) : ""}.`;
  });
}

function bLink(from, to, at) {
  const r = bEdit((m) => {
    const a = from.startsWith("srv:") ? from.slice(4) : from, b = to.startsWith("srv:") ? to.slice(4) : to;
    return Build.link(m, a, b);
  });
  BL.from = "";
  if (r === "pick") {
    const a = from.replace(/^srv:/, ""), b = to.replace(/^srv:/, "");
    BL.pick = { a, b, x: at[0], y: at[1] };
    bCanvas();
  } else bRender();
}

function bMode(mode) { BL.mode = mode; BL.from = ""; BL.pick = null; bRender(); }

// ---------- YAML pane ----------
function bHL(line) {
  if (/^\s*#/.test(line)) return `<span class="c">${esc(line)}</span>`;
  // key: value, with flow maps and lists; strings in quotes stay whole.
  return line.replace(/("(?:[^"\\]|\\.)*")|([A-Za-z_][\w./+-]*)(:)(?=\s|$)|(\b\d[\d.]*\b|true|false)|([^\s{}[\],:"]+)|([{}[\],:-])/g, (all, q, k, colon, n, w, p) => {
    if (q) return `<span class="s">${esc(q)}</span>`;
    if (k) return `<span class="k">${esc(k)}</span><span class="p">:</span>`;
    if (n) return /^\d[\d.]*$/.test(n) && (n.match(/\./g) || []).length > 1 ? `<span class="s">${n}</span>` : `<span class="n">${n}</span>`;
    if (w) return `<span class="s">${esc(w)}</span>`;
    return `<span class="p">${esc(p)}</span>`;
  });
}

function bYAML() {
  const m = BL.m;
  const y = Build.toYAML(m);
  const changed = !BL.y || BL.y.text !== y.text;
  BL.y = y;
  $("#byname").textContent = `${m.name}.yaml`;
  $("#bupcmd").textContent = `parker up ${m.name}.yaml`;
  if (changed) bValidate();
  bPaintYAML();
}

function bPaintYAML() {
  const y = BL.y;
  const res = BL.val.res;
  const errLines = new Set(((res && res.problems) || []).map((p) => p.line).filter(Boolean));
  const old = new Map();
  BL.prev.forEach((l) => old.set(l, (old.get(l) || 0) + 1));
  const added = y.lines.map((l) => { const c = old.get(l) || 0; if (c) { old.set(l, c - 1); return false; } return BL.prev.length > 0; });
  let span = null;
  if (BL.sel && BL.sel !== "lab") {
    const p = Build.pathOf(BL.m, BL.sel.replace(/^srv:/, "")) || "";
    span = y.spans[BL.sel.startsWith("srv:") ? p + ".servers" : p];
  }
  const code = $("#bcode");
  if (BL.prev.join("\n") !== y.lines.join("\n") || code.dataset.sel !== BL.sel || code.dataset.err !== [...errLines].join()) {
    code.innerHTML = y.lines.map((l, i) => {
      const n = i + 1;
      const on = span && n >= span[0] && n <= span[1];
      return `<div class="cl ${added[i] ? "add" : ""} ${errLines.has(n) ? "err" : ""} ${on ? "on" : ""}" data-line="${n}"><span>${bHL(l) || " "}</span></div>`;
    }).join("");
    code.dataset.sel = BL.sel; code.dataset.err = [...errLines].join();
    const first = $(".cl.on", code);
    if (first && span) { const r = first.getBoundingClientRect(), c = code.getBoundingClientRect(); if (r.top < c.top || r.bottom > c.bottom) first.scrollIntoView({ block: "nearest" }); }
    clearTimeout(BL.addT);
    BL.addT = setTimeout(() => $$("#bcode .cl.add").forEach((x) => x.classList.remove("add")), 2400);
  }
  BL.prev = y.lines;
  bPaintVal();
}

function bValidate() {
  clearTimeout(BL.vt);
  const seq = ++BL.val.seq;
  BL.val.busy = true;
  BL.vt = setTimeout(async () => {
    const m = BL.m, y = BL.y;
    const local = Build.checks(m).map((c) => ({ message: c.message, id: c.id, line: c.id ? Build.lineOf(y.spans, Build.pathOf(m, c.id)) : 0 }));
    let r = null, err = "";
    try { r = await call("Validate", { yaml: y.text }); } catch (e) { err = e.message; }
    if (seq !== BL.val.seq) return;
    const server = ((r && r.errors) || []).map((e) => ({ message: e.message, line: e.line || Build.lineOf(y.spans, e.path || ""), id: e.path ? Build.idOf(m, e.path) : "" }));
    BL.val = { seq, busy: false, err, res: r ? { ok: r.ok && !local.length, problems: [...local, ...server], warnings: r.warnings || [], nodes: r.nodes || 0, networks: r.networks || 0 } : null };
    bPaintYAML();
    bCanvas();
  }, 280);
  bPaintVal();
}

function bPaintVal() {
  const v = BL.val, res = v.res;
  let chip, body;
  if (v.err) {
    chip = '<span class="chip warn"><span class="d"></span>Not checked</span>';
    body = `<div class="row"><span class="st starting"><i></i></span><div>Can't reach the daemon to validate: ${esc(v.err)}</div></div>`;
  } else if (!res) {
    chip = '<span class="chip"><span class="d"></span>Checking…</span>';
    body = '<div class="row"><span class="st done"><i></i></span><div>Validating…</div></div>';
  } else if (!res.ok) {
    chip = `<span class="chip bad"><span class="d"></span>${res.problems.length} problem${res.problems.length !== 1 ? "s" : ""}</span>`;
    body = res.problems.map((p) => `<div class="row prob" data-pline="${p.line || 0}" data-pid="${esc(p.id || "")}" tabindex="0" role="button"><span class="st failing"><i></i></span><div><b style="font-weight:600;white-space:pre-wrap">${esc(p.message)}</b>${p.line ? `<br><code>line ${p.line}</code>` : ""}</div></div>`).join("");
  } else {
    chip = `<span class="chip ok"><span class="d"></span>Valid</span>`;
    body = `<div class="row"><span class="st ready"><i></i></span><div><b style="font-weight:600">parker validate passed</b><br><code>${res.nodes} node${res.nodes !== 1 ? "s" : ""} on ${res.networks} network${res.networks !== 1 ? "s" : ""} when expanded · ${BL.y.lines.length} lines</code></div></div>`;
  }
  if (res && res.warnings.length) body += res.warnings.map((w) => `<div class="row"><span class="st starting"><i></i></span><div style="white-space:pre-wrap">${esc(w)}</div></div>`).join("");
  if (v.busy && res) chip += ' <span class="faint" style="font-size:11.5px">checking…</span>';
  $("#bvchip").innerHTML = chip;
  $("#bval").innerHTML = body;
  const up = $("#bup");
  if (up) up.disabled = !res || !res.ok;
}

// ---------- events ----------
function bBind() {
  const root = $("#builder");
  root.addEventListener("click", (e) => {
    const t = e.target;
    const pk = t.closest("[data-pk]");
    if (pk) { const s = bSelDC(); bAdd(pk.dataset.pk, s.dc, s.pid); return; }
    const st = t.closest("[data-starter]");
    if (st) {
      BL.sel = ""; BL.prev = []; BL.mode = "select"; BL.from = "";
      bEdit((m) => { const n = Build.starter(st.dataset.starter); Object.keys(m).forEach((k) => delete m[k]); Object.assign(m, n); return `Started from ${st.textContent}. Undo with Ctrl+Z.`; });
      return;
    }
    const md = t.closest("[data-bmode]");
    if (md) { bMode(md.dataset.bmode); return; }
    const ba = t.closest("[data-bact]");
    if (ba) { bAct(ba.dataset.bact); return; }
    const jn = t.closest("[data-join]");
    if (jn) { const p = BL.pick; BL.pick = null; if (jn.dataset.join) bEdit((m) => Build.joinDCs(m, p.a, p.b, jn.dataset.join)); else bCanvas(); return; }
    const fb = t.closest("button[data-f]");
    if (fb) { bField(fb); return; }
    const pr = t.closest("[data-pline]");
    if (pr) { bGoto(+pr.dataset.pline, pr.dataset.pid); return; }
    const cl = t.closest("#bcode .cl");
    if (cl) { const id = Build.idOf(BL.m, bPathAt(+cl.dataset.line)); if (id) { BL.sel = id; bRender(); } return; }
    const it = t.closest("#bworld [data-bid]");
    if (it) { bClickItem(it.dataset.bid, e); return; }
    const rg = t.closest("#bworld [data-drop]");
    if (rg && rg.dataset.drop !== "new") { const [d, pid] = rg.dataset.drop.split(":"); bClickItem(pid || d, e); return; }
    if (t.closest("#bworld") && !t.closest(".bpick")) { BL.pick = null; if (BL.mode === "link" && BL.from) { BL.from = ""; } BL.sel = ""; bRender(); }
  });
  root.addEventListener("change", (e) => { if (e.target.matches("input[data-f],select[data-f]")) bField(e.target); });
  root.addEventListener("keydown", (e) => {
    if (e.key === "Enter" && e.target.matches("input[data-f]")) { e.target.blur(); return; }
    if ((e.key === "Enter" || e.key === " ") && e.target.matches("[data-pk],[data-bid],[data-pline]")) { e.preventDefault(); e.target.click(); }
  });
  // Palette drag and drop.
  root.addEventListener("dragstart", (e) => {
    const p = e.target.closest && e.target.closest("[data-pk]");
    if (!p) return;
    e.dataTransfer.setData("text/x-parker", p.dataset.pk);
    e.dataTransfer.setData("text/plain", p.dataset.pk);
    e.dataTransfer.effectAllowed = "copy";
    root.classList.add("dragging");
  });
  root.addEventListener("dragend", () => { root.classList.remove("dragging"); $$("#bworld .hot").forEach((x) => x.classList.remove("hot")); });
  const zone = (e) => { const z = e.target.closest && e.target.closest("[data-drop]"); return z && z.closest("#bworld") ? z : null; };
  root.addEventListener("dragover", (e) => {
    const z = zone(e);
    $$("#bworld .hot").forEach((x) => { if (x !== z) x.classList.remove("hot"); });
    if (z) { e.preventDefault(); e.dataTransfer.dropEffect = "copy"; z.classList.add("hot"); }
  });
  root.addEventListener("drop", (e) => {
    const z = zone(e);
    root.classList.remove("dragging");
    if (!z) return;
    e.preventDefault();
    z.classList.remove("hot");
    const k = e.dataTransfer.getData("text/x-parker") || e.dataTransfer.getData("text/plain");
    if (z.dataset.drop === "new") { if (k === "dc") bAdd("dc"); else toast("Drop that into a datacenter."); return; }
    const [dcId, pid] = z.dataset.drop.split(":");
    bAdd(k, dcId, pid);
  });
  // Drag from a dot to link.
  let drag = null;
  root.addEventListener("pointerdown", (e) => {
    const p = e.target.closest("[data-port]");
    if (!p) return;
    e.preventDefault(); e.stopPropagation();
    const w = $("#bworld").getBoundingClientRect(), r = p.getBoundingClientRect();
    drag = { from: p.dataset.port, x: r.left + r.width / 2 - w.left, y: r.top + r.height / 2 - w.top };
    BL.from = drag.from;
    p.setPointerCapture(e.pointerId);
  });
  root.addEventListener("pointermove", (e) => {
    if (!drag) return;
    const w = $("#bworld").getBoundingClientRect();
    const x = e.clientX - w.left, y = e.clientY - w.top;
    $("#blinkline").setAttribute("d", `M${drag.x},${drag.y} C${drag.x + 60},${drag.y} ${x - 60},${y} ${x},${y}`);
  });
  root.addEventListener("pointerup", (e) => {
    if (!drag) return;
    const d = drag; drag = null;
    const line = $("#blinkline"); if (line) line.setAttribute("d", "");
    const under = document.elementFromPoint(e.clientX, e.clientY);
    const tgt = under && (under.closest("#bworld [data-bid]") || under.closest("#bworld [data-drop]"));
    const id = tgt ? tgt.dataset.bid || (tgt.dataset.drop || "").split(":")[0] : "";
    BL.from = "";
    if (!id || id === "new" || id === d.from || id === "lab") { bCanvas(); return; }
    const w = $("#bworld").getBoundingClientRect();
    bLink(d.from, id, [e.clientX - w.left, e.clientY - w.top]);
  });
}

function bSelDC() {
  const m = BL.m;
  const f = BL.sel.startsWith("srv:") ? { dc: Build.dcById(m, BL.sel.slice(4)) } : Build.find(m, BL.sel);
  if (!f || !f.dc) return { dc: m.dcs.length ? m.dcs[m.dcs.length - 1].id : "", pid: "" };
  const pid = f.part ? f.part.id : f.node ? f.node.part : f.gw ? f.gw.part : "";
  return { dc: f.dc.id, pid: pid || "" };
}

function bClickItem(id, e) {
  BL.pick = null;
  if (id === "lab") { BL.sel = "lab"; bRender(); return; }
  if (BL.mode === "link") {
    if (!BL.from) { BL.from = id; BL.sel = id; bRender(); return; }
    if (BL.from !== id) {
      const w = $("#bworld").getBoundingClientRect();
      const from = BL.from;
      BL.sel = from.replace(/^srv:/, "");
      bLink(from, id, [e.clientX - w.left, e.clientY - w.top]);
      return;
    }
  }
  BL.sel = id;
  bRender();
}

function bAct(a) {
  const y = BL.y;
  switch (a) {
    case "undo": bHistory(true); return;
    case "redo": bHistory(false); return;
    case "lab": BL.sel = BL.sel === "lab" ? "" : "lab"; bRender(); return;
    case "deselect": BL.sel = ""; bRender(); return;
    case "linkfrom": BL.mode = "link"; BL.from = BL.sel; bRender(); return;
    case "remove": {
      const id = BL.sel.replace(/^srv:/, "");
      const name = bLabel(id);
      BL.sel = "";
      bEdit((m) => { Build.remove(m, id); return `Removed ${name}. Undo with Ctrl+Z.`; });
      return;
    }
    case "copy": bCopy(y.text, `Copied ${BL.m.name}.yaml`); return;
    case "download": {
      const u = URL.createObjectURL(new Blob([y.text], { type: "application/yaml" }));
      const l = document.createElement("a");
      l.href = u; l.download = `${BL.m.name}.yaml`;
      document.body.appendChild(l); l.click(); l.remove();
      setTimeout(() => URL.revokeObjectURL(u), 1000);
      return;
    }
    case "up": bCopy(`parker up ${BL.m.name}.yaml`, `Copied. Save the YAML as ${BL.m.name}.yaml, then run it where the daemon runs.`); return;
  }
}

async function bCopy(text, msg) {
  try { await navigator.clipboard.writeText(text); toast(msg); } catch (e) { toast("Can't copy here; select the text instead."); }
}

// bPathAt is the deepest path whose span holds a YAML line.
function bPathAt(line) {
  let best = "", size = Infinity;
  for (const [p, [a, b]] of Object.entries(BL.y.spans)) if (line >= a && line <= b && b - a < size) { best = p; size = b - a; }
  return best;
}

function bGoto(line, id) {
  if (id) BL.sel = id;
  bRender();
  if (line) { const el = $(`#bcode .cl[data-line="${line}"]`); if (el) el.scrollIntoView({ block: "center" }); }
}

document.addEventListener("keydown", (e) => {
  if (S.route.view !== "build" || !$("#builder")) return;
  if (e.target.matches("input,select,textarea")) return;
  const k = e.key.toLowerCase();
  if ((e.ctrlKey || e.metaKey) && k === "z") { e.preventDefault(); bHistory(!e.shiftKey); return; }
  if ((e.ctrlKey || e.metaKey) && k === "y") { e.preventDefault(); bHistory(false); return; }
  if (e.ctrlKey || e.metaKey || e.altKey) return;
  if (k === "v") bMode("select");
  else if (k === "l") bMode("link");
  else if (k === "escape") { BL.pick = null; BL.from = ""; if (BL.mode === "link") bMode("select"); else { BL.sel = ""; bRender(); } }
  else if ((k === "delete" || k === "backspace") && BL.sel && !BL.sel.startsWith("srv:")) { e.preventDefault(); bAct("remove"); }
});
