// Lab builder (#/build): drag components onto a canvas, link them, and
// read the lab's YAML as it changes. The model and the YAML writer are in
// buildmodel.js; this file draws them and handles the edits. The YAML is
// checked as `parker validate` checks it as you go (POST /ui/build/check).
//
// Two modes. A lab started here is the model, and the YAML is written from
// it. A file (opened, uploaded, or typed into the YAML pane) is the source
// of truth: the canvas shows what it merges to, and a canvas edit becomes
// ops that /ui/build/patch splices into the text, so comments, anchors and
// key order stay where they are.
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
  ["Connections", [["up", "Upstream", "A service calls a service", "up"], ["peer", "Cluster peering", "Peer two datacenters or partitions", "peer"], ["fed", "WAN federation", "Federate the datacenters", "fed"]]],
];

const SVC_NAMES = ["web", "api", "billing", "orders", "cart", "search", "auth", "ledger", "inventory", "payments"];
const BK = "parker-builder", BKF = "parker-builder-file";

const BL = {
  m: null, sel: "", mode: "select", from: "", pick: null,
  undo: [], redo: [], schema: null, fresh: "",
  y: null, prev: [], val: { seq: 0, busy: false, res: null, err: "" }, vt: null,
  // file: {text, path, saved, name} when a file is the source; saved is its
  // text as last opened or saved (null: never saved). chk: the last check.
  file: null, chk: null, tab: "source", typedAt: 0,
};

// bpost calls a builder file endpoint (see webui/build.go).
async function bpost(what, body) {
  const r = await fetch("/ui/build/" + what, { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
  if (r.status === 401) signedOut();
  if (!r.ok) throw new Error((await r.text()).trim() || r.statusText);
  return r.json();
}

// ---------- state ----------
function bInit() {
  if (BL.m) return;
  try { const s = JSON.parse(localStorage.getItem(BK) || "null"); if (s && s.dcs) BL.m = s; } catch (e) { /* none saved */ }
  try { const f = JSON.parse(localStorage.getItem(BKF) || "null"); if (f && typeof f.text === "string") BL.file = f; } catch (e) { /* none saved */ }
  if (!BL.m) BL.m = Build.starter("mesh");
  fetch("/ui/schema.json", { credentials: "same-origin" }).then((r) => r.json()).then((s) => { BL.schema = s; if (S.route.view === "build") bInspector(); }).catch(() => {});
}
function bSave() {
  try {
    localStorage.setItem(BK, JSON.stringify(BL.m));
    if (BL.file) localStorage.setItem(BKF, JSON.stringify(BL.file)); else localStorage.removeItem(BKF);
  } catch (e) { /* not saved */ }
}
const bText = () => (BL.file ? BL.file.text : BL.y ? BL.y.text : "");
const bName = () => (BL.chk && BL.chk.name) || BL.m.name;
const bDirty = () => !!BL.file && BL.file.saved !== BL.file.text;

// bUseFile makes text the source of truth (opened from path, or uploaded
// with only a name).
function bUseFile(text, path, name) {
  BL.file = { text, path: path || name || "", saved: text, name: name || (path || "").split("/").pop() || "lab.yaml" };
  BL.undo = []; BL.redo = []; BL.sel = ""; BL.prev = []; BL.chk = null; BL.tab = "source";
  BL.val = { seq: BL.val.seq, busy: false, res: null, err: "" };
  BL.m = Build.lab(BL.file.name.replace(/\.ya?ml$/, ""));
  bSave(); bRender();
}

// bSetText changes the file's text and checks it again. typing: from the
// YAML pane itself (leave the textarea alone).
function bSetText(text, o) {
  o = o || {};
  BL.file.text = text;
  bSave();
  bPaintYAML(!o.typing);
  bValidate(o.typing ? 400 : 0);
}

// bEdit applies an edit, with undo. fn may return a message to toast.
// With a file, the edit goes into the file's text as ops.
function bEdit(fn) {
  if (BL.file) return bEditFile(fn);
  const before = JSON.stringify(BL.m);
  let msg;
  try { msg = fn(BL.m); } catch (e) { BL.m = JSON.parse(before); toast(e.message); return false; }
  if (JSON.stringify(BL.m) !== before) { BL.undo.push(before); if (BL.undo.length > 100) BL.undo.shift(); BL.redo = []; bSave(); }
  if (msg && msg !== "pick") toast(msg);
  bRender();
  return msg;
}
function bEditFile(fn) {
  const src = BL.chk && BL.chk.source;
  if (!src || BL.val.busy) { toast(src ? "Checking the file; try again in a moment." : "The file doesn't parse; fix it in the YAML pane first."); return false; }
  if (BL.m.raw) { toast("This is a low-level file (nodes and networks); edit it in the YAML pane."); return false; }
  const before = JSON.stringify(BL.m);
  let msg, ops;
  try { msg = fn(BL.m); ops = Build.diffOps(JSON.parse(before), BL.m, src); } catch (e) { BL.m = JSON.parse(before); toast(e.message); bRender(); return false; }
  if (msg && msg !== "pick") toast(msg);
  bRender();
  if (!ops.length) return msg;
  const text = BL.file.text;
  BL.val.busy = true;
  bpost("patch", { yaml: text, ops }).then((r) => {
    BL.val.busy = false;
    if (!BL.file || BL.file.text !== text) return; // typed over meanwhile
    BL.undo.push({ text }); if (BL.undo.length > 100) BL.undo.shift(); BL.redo = [];
    bSetText(r.yaml);
  }).catch((e) => {
    BL.val.busy = false;
    BL.m = JSON.parse(before);
    toast("That edit can't go in the file: " + e.message);
    bRender();
  });
  return msg;
}

function bHistory(back) {
  const from = back ? BL.undo : BL.redo, to = back ? BL.redo : BL.undo;
  if (!from.length) { toast(back ? "Nothing to undo" : "Nothing to redo"); return; }
  if (BL.file) {
    const e = from.pop();
    if (typeof e === "object" && e) { to.push({ text: BL.file.text }); bSetText(e.text); }
    return;
  }
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
        <div class="hd"><b id="byname"></b><span id="bvchip"></span><div style="flex:1"></div>
          <div class="seg" id="btabs"><button data-btab="source" title="The file: edit it here or on the canvas">Source</button><button data-btab="merged" title="Imports and templates applied, vars substituted">Merged</button><button data-btab="expanded" title="The low-level form parker up runs">Expanded</button></div></div>
        <div class="bfile" id="bfile"></div>
        <div class="code" id="bcode"><div class="cwrap"><div id="blines"></div><textarea id="bta" spellcheck="false" autocomplete="off" autocapitalize="off" wrap="off" aria-label="The lab's YAML"></textarea></div></div>
        <div class="val" id="bval"></div>
        <div class="yfoot">
          <button class="btn sm" data-bact="open" title="Open a file by upload or by path">Open…</button><button class="btn sm" data-bact="save" title="See what changed, then save">Save…</button><button class="btn sm" data-bact="download">Download</button><button class="btn sm" data-bact="copy">Copy</button>
          <div class="yrun"><button class="btn sm" data-bact="down" id="bdown" title="Take the lab down">Down</button>
          <button class="btn primary sm" data-bact="up" id="bup" title="Bring the lab up and open its dashboard">${ICON.term}<span id="bupcmd"></span></button></div>
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
  const og = bOrigin(id);
  return `<div class="bn ${on ? "sel" : ""} ${src ? "linksrc" : ""} ${BL.fresh === id ? "new" : ""} ${o.bad ? "bad" : ""} ${og === "import" ? "lock" : ""}" data-bid="${esc(id)}" style="left:${p.x}px;top:${p.y}px;width:${p.w}px" tabindex="0" role="button" aria-label="${esc(name)}">
    <div class="ico ${cls}">${ico}</div><div style="min-width:0"${og ? ` title="${og === "import" ? "From an import: rename or remove it in the module" : "Made from a template: what you set here overrides it"}"` : ""}><b>${og === "import" ? "🔒 " : ""}${esc(name)}</b><small>${esc(sub)}${og ? ` · from ${esc(og === "import" ? "an import" : og.replace(/^template /, ""))}` : ""}</small></div>${o.port ? `<span class="port" data-port="${esc(id)}" title="Drag to link"></span>` : ""}</div>`;
}

// bOrigin is where a thing comes from when a file is the source: "" (the
// file), "import", or "template NAME".
function bOrigin(id) {
  if (!BL.file || !BL.chk || !BL.chk.source || !id || id === "lab") return "";
  return Build.origin(BL.m, id.replace(/^srv:/, ""), BL.chk.source);
}

function bCanvas() {
  const m = BL.m, P = bLayout();
  if (m.raw) {
    const world = $("#bworld");
    world.innerHTML = `<div class="card braw"><h3>A low-level file</h3><p>${esc(bName())} lists nodes and networks itself, with no consul: section, so there's nothing for the canvas to draw. Edit it in the YAML pane; Run brings it up as it is.</p><p class="faint">The Expanded tab shows what parker up runs.</p></div>`;
    world.style.width = ""; world.style.height = "";
    return;
  }
  let h = "";
  const problems = new Set(((BL.val.res && BL.val.res.problems) || []).map((p) => p.id).filter(Boolean));
  for (const d of m.dcs) {
    const r = P.dc[d.id];
    const svcs = d.nodes.filter((n) => n.svc).length;
    h += `<div class="breg ${BL.sel === d.id ? "sel" : ""} ${BL.from === d.id ? "linksrc" : ""} ${problems.has(d.id) ? "bad" : ""}" data-drop="${d.id}:" style="left:${r.x}px;top:${r.y}px;width:${r.w}px;height:${r.h}px">
      <div class="lbl" data-bid="${d.id}" tabindex="0" role="button">${bOrigin(d.id) === "import" ? '<i class="otag" title="From an import">🔒</i>' : ""}${esc(d.name)}<span>${d.servers} server${d.servers > 1 ? "s" : ""} · ${svcs} service${svcs !== 1 ? "s" : ""}</span><span class="port dcport" data-port="${d.id}" title="Drag to another datacenter to federate or peer"></span></div>
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
    // A side's partition: a picker when its dc has partitions.
    const side = (dd, pid, f) => `<b class="mono">${esc(dd.name)}</b>` + (dd.parts.length
      ? ` / <select data-f="${f}" id="bf-${f}" aria-label="Partition" style="width:auto;display:inline-block">${[{ id: "", name: "default" }, ...dd.parts].map((x) => `<option value="${x.id}" ${(pid || "") === x.id ? "selected" : ""}>${esc(x.name)}</option>`).join("")}</select>`
      : "");
    body = fld("Name", txt("name", p.name, Build.peeringName(m, Object.assign({}, p, { name: "" }))), sdoc("Consul", "peering").replace(/,? keyed by.*$/, "."), "bf-name") +
      fld("Dialer", side(d, p.dialPart, "dpart"), sdoc("Peering", "dialer")) +
      fld("Acceptor", side(a, p.accPart, "apart") + ` <button class="btn sm" data-f="swap" style="margin-left:6px">Swap</button>`, sdoc("Peering", "acceptor")) +
      fld("Via mesh gw", seg("pmesh", yes, p.mesh), sdoc("Peering", "via_mesh_gateways")) +
      `<div class="hint">Exports follow from the calls: a service called over this peering is exported by its partition. Each side's partition gets a mesh gateway, in its default namespace; through mesh gateways the control plane uses each datacenter's default-partition gateway.</div>` +
      `<div class="row-btns"><button class="btn sm danger" data-bact="remove">Remove peering</button></div>`;
  }
  const og = f && f.t ? bOrigin(BL.sel) : "";
  if (og === "import") body = body.replace(/<button class="btn sm danger" data-bact="remove">[^<]*<\/button>/, "").replace('id="bf-name"', 'id="bf-name" readonly');
  if (og) body = `<div class="hint onote">${og === "import" ? "This comes from an import. Rename or remove it in the module; what you change here goes in this file and overrides it." : `Made from ${esc(og)}. What you set here goes on this node and overrides the template.`}</div>` + body;
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
      if (f === "swap") { [p.acceptor, p.dialer] = [p.dialer, p.acceptor]; [p.accPart, p.dialPart] = [p.dialPart || "", p.accPart || ""]; }
      if (f === "dpart" || f === "apart") {
        const k = f === "dpart" ? "dialPart" : "accPart", was = p[k];
        p[k] = v;
        const key = (o) => [`${o.acceptor}:${o.accPart || ""}`, `${o.dialer}:${o.dialPart || ""}`].sort().join("|");
        if (m.peerings.some((o) => o !== p && key(o) === key(p))) { p[k] = was; throw new Error("Those two partitions are peered already."); }
      }
      if (f === "pmesh" || f === "dpart" || f === "apart") Build.peeringGateways(m, p);
    }
  });
}

// ---------- adding ----------
function bAdd(kind, dcId, pid) {
  BL.pick = null;
  if (kind === "up" || kind === "peer" || kind === "fed") {
    bMode("link");
    toast(kind === "up" ? "Click the calling service, then the one it calls. Or drag from a service's dot."
      : kind === "peer" ? "Click one datacenter or partition, then one in another datacenter. Or drag from a datacenter's dot onto the other side."
      : "Click one datacenter, then the other. Or drag from a datacenter's dot.");
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
  if (!BL.file) {
    const y = Build.toYAML(m);
    const changed = !BL.y || BL.y.text !== y.text;
    BL.y = y;
    if (changed) bValidate();
  } else if (!BL.chk && !BL.val.busy) bValidate(0);
  bPaintYAML(true);
}

// bLines is what the YAML pane shows: the tab's text, as lines, and where
// each path sits in it (the source tab only).
function bLines() {
  const c = BL.chk;
  if (BL.tab !== "source") {
    const t = (c && (BL.tab === "merged" ? c.merged : c.expanded)) || "";
    return { text: t, lines: t ? t.replace(/\n$/, "").split("\n") : [], spans: {} };
  }
  if (BL.file) return { text: BL.file.text, lines: BL.file.text.replace(/\n$/, "").split("\n"), spans: (c && c.spans) || {} };
  return { text: BL.y.text, lines: BL.y.lines, spans: BL.y.spans };
}

// bPaintYAML draws the YAML pane; ta: also set the textarea's text (not
// while someone types in it).
function bPaintYAML(ta) {
  const y = bLines();
  const src = BL.tab === "source";
  const res = BL.val.res;
  const errLines = new Set(src ? ((res && res.problems) || []).map((p) => p.line).filter(Boolean) : []);
  const old = new Map();
  BL.prev.forEach((l) => old.set(l, (old.get(l) || 0) + 1));
  const added = y.lines.map((l) => { const c = old.get(l) || 0; if (c) { old.set(l, c - 1); return false; } return BL.prev.length > 0 && src; });
  let span = null;
  if (src && BL.sel && BL.sel !== "lab") {
    const p = Build.pathOf(BL.m, BL.sel.replace(/^srv:/, "")) || "";
    span = y.spans[BL.sel.startsWith("srv:") ? p + ".servers" : p] || (BL.sel.startsWith("srv:") ? y.spans[p] : null);
  }
  $("#byname").textContent = BL.file ? (BL.file.path || BL.file.name) + (bDirty() ? " •" : "") : `${BL.m.name}.yaml`;
  $("#byname").title = BL.file ? (bDirty() ? "Changed since it was opened or saved" : "Saved") : "Written from the canvas";
  $("#bupcmd").textContent = `Run ${bName()}`;
  $$("#btabs button").forEach((b) => b.classList.toggle("on", b.dataset.btab === BL.tab));
  bPaintFile();
  const lines = $("#blines"), area = $("#bta");
  const key = [BL.tab, BL.sel, [...errLines].join(), span ? span.join() : ""].join("|");
  if (BL.prev.join("\n") !== y.lines.join("\n") || lines.dataset.key !== key) {
    lines.innerHTML = y.lines.map((l, i) => {
      const n = i + 1;
      const on = span && n >= span[0] && n <= span[1];
      return `<div class="cl ${added[i] ? "add" : ""} ${errLines.has(n) ? "err" : ""} ${on ? "on" : ""}" data-line="${n}"><span>${bHL(l) || " "}</span></div>`;
    }).join("") || `<div class="cl"><span class="faint">${src ? "" : "Shows once the file checks."}</span></div>`;
    lines.dataset.key = key;
    const first = $(".cl.on", lines), code = $("#bcode");
    if (first && span && document.activeElement !== area) { const r = first.getBoundingClientRect(), c = code.getBoundingClientRect(); if (r.top < c.top || r.bottom > c.bottom) first.scrollIntoView({ block: "nearest" }); }
    clearTimeout(BL.addT);
    BL.addT = setTimeout(() => $$("#blines .cl.add").forEach((x) => x.classList.remove("add")), 2400);
  }
  area.hidden = !src;
  if (ta && src && area.value !== y.text) area.value = y.text;
  BL.prev = y.lines;
  bPaintVal();
}

// The file bar: what the pane shows, and what the canvas can't change.
function bPaintFile() {
  const el = $("#bfile");
  const notes = [];
  if (BL.tab !== "source") notes.push(BL.tab === "merged" ? "Merged: imports and templates applied, vars substituted. Read-only; edits go in Source." : "Expanded: the nodes, networks and processes parker up runs. Read-only.");
  else if (BL.file) {
    if (BL.m.raw) { /* the canvas says so */ } else if (BL.file.path && !BL.file.path.startsWith("/")) notes.push("Uploaded: relative imports and file: refs don't resolve. Open it by path to use them.");
    const m = BL.m, locked = [...m.dcs.map((d) => d.id), ...m.dcs.flatMap((d) => [...d.nodes, ...d.gws, ...d.parts].map((x) => x.id))].some((id) => bOrigin(id) === "import");
    if (locked) notes.push("🔒 marks what an import adds: rename or remove it in the module. Settings you change here override it from this file.");
  } else notes.push("Written from the canvas. Type here to edit the file itself; the canvas then edits it in place.");
  el.innerHTML = notes.map((n) => `<div>${esc(n)}</div>`).join("");
  el.hidden = !notes.length;
}

function bValidate(delay) {
  clearTimeout(BL.vt);
  const seq = ++BL.val.seq;
  BL.val.busy = true;
  BL.vt = setTimeout(async () => {
    const m = BL.m, text = bText();
    const local = BL.file ? [] : Build.checks(m).map((c) => ({ message: c.message, id: c.id, line: c.id ? Build.lineOf(BL.y.spans, Build.pathOf(m, c.id)) : 0 }));
    let r = null, err = "";
    try { r = await bpost("check", { yaml: text, path: BL.file ? BL.file.path : "" }); } catch (e) { err = e.message; }
    if (seq !== BL.val.seq) return;
    if (r) {
      BL.chk = r;
      // A file's canvas is what it merges to.
      if (BL.file && r.tree) {
        BL.m = Build.fromTree(r.tree, BL.m);
        if (r.name) BL.m.name = r.name;
        if (BL.sel && BL.sel !== "lab" && !Build.find(BL.m, BL.sel.replace(/^srv:/, ""))) BL.sel = "";
        bSave();
      }
    }
    const spans = BL.file ? (r && r.spans) || {} : BL.y.spans;
    const server = ((r && r.problems) || []).map((e) => ({ message: e.message, line: e.line || Build.lineOf(spans, e.path || ""), id: e.path ? Build.idOf(BL.m, e.path) : "" }));
    BL.val = { seq, busy: false, err, res: r ? { ok: r.ok && !local.length, problems: [...local, ...server], warnings: r.warnings || [], nodes: r.nodes || 0, networks: r.networks || 0 } : null };
    bPaintYAML(false);
    bCanvas();
    bInspector();
  }, delay === undefined ? 280 : delay);
  bPaintVal();
}

function bPaintVal() {
  const v = BL.val, res = v.res;
  let chip, body;
  if (v.err) {
    chip = '<span class="chip warn"><span class="d"></span>Not checked</span>';
    body = `<div class="row"><span class="st starting"><i></i></span><div>Can't check the file: ${esc(v.err)}</div></div>`;
  } else if (!res) {
    chip = '<span class="chip"><span class="d"></span>Checking…</span>';
    body = '<div class="row"><span class="st done"><i></i></span><div>Validating…</div></div>';
  } else if (!res.ok) {
    chip = `<span class="chip bad"><span class="d"></span>${res.problems.length} problem${res.problems.length !== 1 ? "s" : ""}</span>`;
    body = res.problems.map((p) => `<div class="row prob" data-pline="${p.line || 0}" data-pid="${esc(p.id || "")}" tabindex="0" role="button"><span class="st failing"><i></i></span><div><b style="font-weight:600;white-space:pre-wrap">${esc(p.message)}</b>${p.line ? `<br><code>line ${p.line}</code>` : ""}</div></div>`).join("");
  } else {
    chip = `<span class="chip ok"><span class="d"></span>Valid</span>`;
    body = `<div class="row"><span class="st ready"><i></i></span><div><b style="font-weight:600">parker validate passed</b><br><code>${res.nodes} node${res.nodes !== 1 ? "s" : ""} on ${res.networks} network${res.networks !== 1 ? "s" : ""} when expanded · ${bText().replace(/\n$/, "").split("\n").length} lines</code></div></div>`;
  }
  if (res && res.warnings.length) body += res.warnings.map((w) => `<div class="row"><span class="st starting"><i></i></span><div style="white-space:pre-wrap">${esc(w)}</div></div>`).join("");
  if (v.busy && res) chip += ' <span class="faint" style="font-size:11.5px">checking…</span>';
  $("#bvchip").innerHTML = chip;
  $("#bval").innerHTML = body;
  const up = $("#bup");
  if (up) up.disabled = !res || !res.ok || v.busy;
  const dn = $("#bdown");
  if (dn) dn.disabled = !(S.labs || []).some((l) => l.name === bName() && l.namespaces);
}

// ---------- open, save, run ----------
// bDialog shows the builder's dialog with html; returns the element.
function bDialog(html, cls) {
  let d = $("#bdlg");
  if (!d) {
    d = document.createElement("dialog");
    d.id = "bdlg";
    document.body.appendChild(d);
    d.addEventListener("click", (e) => { if (e.target === d || e.target.closest("[data-close]")) d.close(); });
  }
  d.className = cls || "";
  d.innerHTML = html;
  if (!d.open) d.showModal();
  return d;
}

function bOpenDialog() {
  const d = bDialog(`<div class="bd-in">
    <h3>Open a lab file</h3>
    <p>The canvas shows what the file merges to. Edits on the canvas go into the file's text, so its comments, anchors and key order stay as they are.</p>
    <label class="bd-lbl" for="bd-path">Path on the machine serving this page</label>
    <div class="bd-row"><input id="bd-path" placeholder="~/labs/mesh.yaml" spellcheck="false" autocomplete="off" value="${esc(BL.file && BL.file.path.startsWith("/") ? BL.file.path : "")}"><button class="btn primary sm" id="bd-open">Open</button></div>
    <div class="hint" id="bd-err"></div>
    <label class="bd-lbl">Or upload one</label>
    <div class="bd-row"><input type="file" id="bd-up" accept=".yaml,.yml,application/yaml,text/yaml"></div>
    <div class="hint">An uploaded file has no folder, so relative imports and file: refs don't resolve. Open by path to use them.</div>
    <div class="bd-row end"><button class="btn sm" data-close>Cancel</button></div></div>`, "bd-open");
  const go = async () => {
    const path = $("#bd-path", d).value.trim();
    if (!path) return;
    if (!bLeave()) return;
    try {
      const r = await bpost("open", { path });
      d.close();
      bUseFile(r.yaml, r.path);
      toast(`Opened ${r.path}`);
    } catch (e) { $("#bd-err", d).textContent = e.message; }
  };
  $("#bd-open", d).onclick = go;
  $("#bd-path", d).onkeydown = (e) => { if (e.key === "Enter") go(); };
  $("#bd-up", d).onchange = (e) => {
    const f = e.target.files[0];
    if (!f) return;
    if (f.size > 1 << 20) { $("#bd-err", d).textContent = "That file is over 1 MiB."; return; }
    if (!bLeave()) return;
    f.text().then((t) => { d.close(); bUseFile(t, "", f.name); toast(`Opened ${f.name}`); });
  };
  setTimeout(() => $("#bd-path", d).focus(), 0);
}

// bLeave asks before dropping a file's unsaved edits.
function bLeave() {
  return !bDirty() || confirm(`${BL.file.path || BL.file.name} has changes that aren't saved. Drop them?`);
}

// bDiffHTML shows what saving changes: a line diff, unchanged runs folded.
function bDiffHTML(a, b) {
  const d = Build.lineDiff(a, b);
  const keep = d.map((x, i) => x.t !== " " || d.slice(Math.max(0, i - 3), i + 4).some((y) => y.t !== " "));
  let h = "", skipped = 0, add = 0, del = 0;
  d.forEach((x, i) => {
    if (x.t === "+") add++;
    if (x.t === "-") del++;
    if (!keep[i]) { skipped++; return; }
    if (skipped) { h += `<div class="dl fold">⋯ ${skipped} unchanged line${skipped > 1 ? "s" : ""}</div>`; skipped = 0; }
    h += `<div class="dl ${x.t === "+" ? "add" : x.t === "-" ? "del" : ""}"><i>${x.t === " " ? "" : x.t}</i><span>${bHL(x.s) || " "}</span></div>`;
  });
  if (skipped) h += `<div class="dl fold">⋯ ${skipped} unchanged line${skipped > 1 ? "s" : ""}</div>`;
  return { html: h, add, del };
}

function bSaveDialog() {
  const text = bText();
  const was = BL.file && BL.file.saved !== null ? BL.file.saved : "";
  const diff = bDiffHTML(was, text);
  const path = BL.file && BL.file.path.startsWith("/") ? BL.file.path : "";
  const what = !was ? `A new file, ${text.replace(/\n$/, "").split("\n").length} lines.` : diff.add || diff.del ? `${diff.add} line${diff.add !== 1 ? "s" : ""} added, ${diff.del} removed since it was ${BL.file.path ? "opened or saved" : "uploaded"}.` : "No changes since it was opened or saved.";
  const d = bDialog(`<div class="bd-in">
    <h3>Save ${esc(bName())}.yaml</h3>
    <p>${esc(what)}</p>
    <div class="bdiff">${diff.html || '<div class="dl fold">Nothing to show.</div>'}</div>
    <label class="bd-lbl" for="bd-spath">Path on the machine serving this page</label>
    <div class="bd-row"><input id="bd-spath" placeholder="~/labs/${esc(bName())}.yaml" value="${esc(path)}" spellcheck="false" autocomplete="off"></div>
    <div class="hint" id="bd-err"></div>
    <div class="bd-row end"><button class="btn sm" data-close>Cancel</button><button class="btn sm" id="bd-dl">Download instead</button><button class="btn primary sm" id="bd-save">Save</button></div></div>`, "bd-save");
  $("#bd-dl", d).onclick = () => { bDownload(text); d.close(); };
  const go = async () => {
    const p = $("#bd-spath", d).value.trim();
    if (!p) { $("#bd-err", d).textContent = "Give a path, or download the file instead."; return; }
    try {
      const r = await bpost("save", { path: p, yaml: text });
      if (!BL.file) { BL.file = { text, path: r.path, saved: text, name: r.path.split("/").pop() }; BL.undo = []; BL.redo = []; }
      else Object.assign(BL.file, { path: r.path, saved: text, name: r.path.split("/").pop() });
      bSave(); d.close();
      toast(`Saved ${r.path}`);
      bPaintYAML(false);
      bValidate(0);
    } catch (e) { $("#bd-err", d).textContent = e.message; }
  };
  $("#bd-save", d).onclick = go;
  $("#bd-spath", d).onkeydown = (e) => { if (e.key === "Enter") go(); };
}

function bDownload(text) {
  const u = URL.createObjectURL(new Blob([text], { type: "application/yaml" }));
  const l = document.createElement("a");
  l.href = u; l.download = BL.file ? BL.file.name : `${BL.m.name}.yaml`;
  document.body.appendChild(l); l.click(); l.remove();
  setTimeout(() => URL.revokeObjectURL(u), 1000);
}

// bRun brings the lab up, with its progress in the dialog, then opens
// its dashboard.
async function bRun() {
  const name = bName(), text = bText();
  const d = bDialog(`<div class="bd-in">
    <h3>Bringing ${esc(name)} up</h3>
    <pre class="brun" id="bd-log"></pre>
    <div class="bd-row end"><span class="faint" id="bd-st">Starting…</span><div class="grow"></div><button class="btn sm" data-close id="bd-close">Close</button><a class="btn primary sm" id="bd-dash" href="#/lab/${encodeURIComponent(name)}" hidden>Open the dashboard</a></div></div>`, "bd-run");
  const log = $("#bd-log", d), st = $("#bd-st", d);
  const add = (t) => { log.textContent += t; log.scrollTop = log.scrollHeight; };
  const ctl = new AbortController();
  d.addEventListener("close", () => ctl.abort(), { once: true });
  let all = "";
  try {
    const r = await fetch("/ui/build/up", { method: "POST", credentials: "same-origin", signal: ctl.signal, headers: { "Content-Type": "application/json" }, body: JSON.stringify({ yaml: text, path: BL.file ? BL.file.path : "" }) });
    if (r.status === 401) signedOut();
    if (!r.ok) { add((await r.text()).trim()); st.textContent = "Not started"; return; }
    const rd = r.body.getReader(), dec = new TextDecoder();
    for (;;) {
      const { value, done } = await rd.read();
      if (done) break;
      const t = dec.decode(value, { stream: true });
      all += t; add(t);
    }
  } catch (e) {
    if (ctl.signal.aborted) return;
    add("\n" + e.message);
  }
  if (/\nparker: up \S+\n?$/.test(all)) {
    st.textContent = `${name} is up.`;
    $("#bd-dash", d).hidden = false;
    $("#bd-dash", d).onclick = () => d.close();
    $("#bd-close", d).textContent = "Stay here";
    poll();
    // To the live dashboard, as parker up would leave you at its output.
    setTimeout(() => { if (d.open && $("#bd-dash", d)) { d.close(); location.hash = `#/lab/${encodeURIComponent(name)}`; } }, 1500);
  } else st.textContent = "It didn't come up; see above.";
}

async function bDown() {
  const name = bName();
  const r = await confirmDialog({ title: `Take ${name} down?`, body: "Kills every process and removes the lab's namespaces and links. Logs are kept unless you purge.", ok: "Down", check: "Also delete its logs, configs and data (purge)" });
  if (!r.ok) return;
  try { await stream("Down", { lab: name, purge: !!r.checked }, () => {}); toast(`${name} is down.`); poll(); } catch (e) { toast(`Down failed: ${e.message}`); }
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
      if (BL.file) {
        if (!bLeave()) return;
        BL.file = null; BL.chk = null; BL.undo = []; BL.redo = []; BL.tab = "source";
      }
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
    const tb = t.closest("[data-btab]");
    if (tb) { BL.tab = tb.dataset.btab; BL.prev = []; bPaintYAML(true); return; }
    const it = t.closest("#bworld [data-bid]");
    if (it) { bClickItem(it.dataset.bid, e); return; }
    const rg = t.closest("#bworld [data-drop]");
    if (rg && rg.dataset.drop !== "new") { const [d, pid] = rg.dataset.drop.split(":"); bClickItem(pid || d, e); return; }
    if (t.closest("#bworld") && !t.closest(".bpick")) { BL.pick = null; if (BL.mode === "link" && BL.from) { BL.from = ""; } BL.sel = ""; bRender(); }
  });
  root.addEventListener("change", (e) => { if (e.target.matches("input[data-f],select[data-f]")) bField(e.target); });
  // The YAML pane: typing edits the file (a lab written from the canvas
  // becomes a file); the caret's line selects what it belongs to.
  const ta = $("#bta");
  ta.addEventListener("input", () => {
    if (!BL.file) {
      BL.file = { text: BL.y.text, path: "", saved: null, name: `${BL.m.name}.yaml` };
      BL.undo = []; BL.redo = [];
      toast("Editing the file now: canvas edits go into its text.");
    }
    const now = Date.now();
    if (now - BL.typedAt > 1200) { BL.undo.push({ text: BL.file.text }); BL.redo = []; }
    BL.typedAt = now;
    bSetText(ta.value, { typing: true });
  });
  const caret = () => {
    if (BL.tab !== "source") return;
    const line = ta.value.slice(0, ta.selectionStart).split("\n").length;
    const id = Build.idOf(BL.m, bPathAt(line));
    if (id && id !== BL.sel) { BL.sel = id; bCanvas(); bInspector(); bPaintYAML(false); }
  };
  ta.addEventListener("click", caret);
  ta.addEventListener("keyup", (e) => { if (e.key.startsWith("Arrow") || e.key === "PageUp" || e.key === "PageDown") caret(); });
  ta.addEventListener("keydown", (e) => {
    // Two spaces for a tab, as YAML wants.
    if (e.key === "Tab" && !e.shiftKey && !e.ctrlKey && !e.metaKey) { e.preventDefault(); document.execCommand("insertText", false, "  "); }
    if (e.key === "Escape") ta.blur();
  });
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
    // A partition's region names the partition (a peering side).
    const [dropDC, dropPart] = tgt ? (tgt.dataset.drop || "").split(":") : [];
    const id = tgt ? tgt.dataset.bid || dropPart || dropDC || "" : "";
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
    case "copy": bCopy(bText(), "Copied the YAML"); return;
    case "download": bDownload(bText()); return;
    case "open": bOpenDialog(); return;
    case "save": bSaveDialog(); return;
    case "up": bRun(); return;
    case "down": bDown(); return;
  }
}

async function bCopy(text, msg) {
  try { await navigator.clipboard.writeText(text); toast(msg); } catch (e) { toast("Can't copy here; select the text instead."); }
}

// bPathAt is the deepest path whose span holds a YAML line.
function bPathAt(line) {
  let best = "", size = Infinity;
  for (const [p, [a, b]] of Object.entries(bLines().spans)) if (line >= a && line <= b && b - a < size) { best = p; size = b - a; }
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
