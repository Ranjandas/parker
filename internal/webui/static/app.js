// Parker web UI. Plain JS, no build step: the daemon embeds this file.
// It calls the Parker API (Connect protocol, JSON) under /ui/api with the
// session cookie that /ui/session set.
"use strict";

const $ = (s, el = document) => el.querySelector(s);
const $$ = (s, el = document) => [...el.querySelectorAll(s)];
const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
const API = "/ui/api/parker.v1.ParkerService/";

// ---------- API ----------
class APIError extends Error {
  constructor(code, message) { super(message || code); this.code = code; }
}

async function call(method, body = {}) {
  const r = await fetch(API + method, {
    method: "POST", credentials: "same-origin",
    headers: { "Content-Type": "application/json", "Connect-Protocol-Version": "1" },
    body: JSON.stringify(body),
  });
  const text = await r.text();
  let j = {};
  try { j = text ? JSON.parse(text) : {}; } catch (e) { j = { message: text }; }
  if (!r.ok) {
    if (r.status === 401) signedOut();
    throw new APIError(j.code || String(r.status), j.message);
  }
  return j;
}

// stream calls a server-streaming method and hands each message to onMsg.
// It resolves when the stream ends; abort with signal.
async function stream(method, body, onMsg, signal) {
  const payload = new TextEncoder().encode(JSON.stringify(body));
  const env = new Uint8Array(5 + payload.length);
  new DataView(env.buffer).setUint32(1, payload.length);
  env.set(payload, 5);
  const r = await fetch(API + method, {
    method: "POST", credentials: "same-origin", signal,
    headers: { "Content-Type": "application/connect+json", "Connect-Protocol-Version": "1" },
    body: env,
  });
  if (!r.ok) {
    if (r.status === 401) signedOut();
    let j = {};
    try { j = await r.json(); } catch (e) { /* not JSON */ }
    throw new APIError(j.code || String(r.status), j.message);
  }
  const reader = r.body.getReader();
  let buf = new Uint8Array(0);
  for (;;) {
    const { value, done } = await reader.read();
    if (done) break;
    const nb = new Uint8Array(buf.length + value.length);
    nb.set(buf); nb.set(value, buf.length); buf = nb;
    while (buf.length >= 5) {
      const flags = buf[0];
      const len = new DataView(buf.buffer, buf.byteOffset).getUint32(1);
      if (buf.length < 5 + len) break;
      const msg = JSON.parse(new TextDecoder().decode(buf.subarray(5, 5 + len)) || "{}");
      buf = buf.slice(5 + len);
      if (flags & 2) {
        if (msg.error) throw new APIError(msg.error.code, msg.error.message);
        return;
      }
      onMsg(msg);
    }
  }
}

const b64 = (s) => {
  const bin = atob(s || "");
  const bytes = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
  return bytes;
};

// ---------- state ----------
const S = {
  info: null,
  labs: [],          // [{name, namespaces, supervised}]
  labHealth: {},     // name -> "ok" | "warn" | "bad" | "down"
  status: null,      // StatusResponse of the current lab
  statusErr: null,
  prev: null,        // previous snapshot for the activity feed
  events: {},        // lab -> [{t, kind, html}]
  route: { lab: "", node: "", view: "" },
  graph: null,       // GraphResponse of the current lab
  graphErr: null,
  logs: { key: "", proc: "", lines: [], ctrl: null, follow: true, filter: "", level: "all", err: "" },
  run: { busy: false, out: [] },
  busy: "",
  fakes: null,       // {lab, list: [FakeService]} from the Fake call
  fakeCall: null,    // {node, svc, r, status, err, hits: Set of node names, t}
};

// ---------- derived state ----------
function procState(p) {
  if (p.stopped) return "stopped";
  if (p.paused) return "paused";
  if (!p.alive) return "down";
  if (p.ready === false) return "starting";
  return "ready";
}
// The catalog health of the service a process runs, if Consul knows it.
function procHealth(n, p) {
  const h = n.health || {};
  return h[p.name] || h[p.name.replace(/-sidecar$/, "-sidecar-proxy")] || "";
}
function nodeState(n) {
  const ps = (n.processes || []).map(procState);
  const crit = Object.values(n.health || {}).some((v) => v === "critical");
  if (!n.namespace) return "down";
  if (ps.some((s) => s === "down") || crit) return "bad";
  if (ps.some((s) => s !== "ready")) return "warn";
  return "ok";
}
function labState(st) {
  const ns = (st.nodes || []).map(nodeState);
  if (!ns.length || ns.every((s) => s === "down")) return "down";
  if (ns.includes("bad") || ns.includes("down")) return "bad";
  if (ns.includes("warn")) return "warn";
  return "ok";
}
function roleOf(n, p) {
  if (p.name === "consul") {
    if (n.role === "leader") return { text: "server ★ leader", lead: true };
    if (n.role === "follower") return { text: "server" };
    if (n.role === "client") return { text: "client" };
    return { text: n.role || "agent" };
  }
  if (/-sidecar$/.test(p.name)) return { text: "sidecar → " + p.name.replace(/-sidecar$/, "") };
  if (/gateway$/.test(p.name)) return { text: p.name.replace(/-/g, " ") };
  if (n.health && n.health[p.name]) return { text: "service" };
  return { text: "process" };
}
const dcOf = (n) => n.dc || "lab";
const groupKey = (n) => dcOf(n) + (n.partition && n.partition !== "default" ? " / " + n.partition : "");

// ---------- activity feed: what changed between two polls ----------
function diff(lab, a, b) {
  if (!a || !b || a.lab !== b.lab) return;
  const ev = (S.events[lab] ||= []);
  const t = new Date().toLocaleTimeString([], { hour12: false });
  const prevNodes = new Map((a.nodes || []).map((n) => [n.name, n]));
  for (const n of b.nodes || []) {
    const o = prevNodes.get(n.name);
    if (!o) continue;
    if (n.role === "leader" && o.role !== "leader") ev.unshift({ t, kind: "ok", html: `<b>${esc(n.name)}</b> became leader` });
    const op = new Map((o.processes || []).map((p) => [p.name, p]));
    for (const p of n.processes || []) {
      const q = op.get(p.name);
      if (!q) continue;
      const s1 = procState(q), s2 = procState(p);
      if (p.restarts > q.restarts) ev.unshift({ t, kind: "bad", html: `<b>${esc(n.name)}/${esc(p.name)}</b> restarted (restart ${p.restarts})` });
      else if (s1 !== s2) ev.unshift({ t, kind: s2 === "ready" ? "ok" : s2 === "down" ? "bad" : "acc", html: `<b>${esc(n.name)}/${esc(p.name)}</b> is ${s2}` });
    }
    for (const [svc, h] of Object.entries(n.health || {})) {
      const was = (o.health || {})[svc];
      if (was && was !== h) ev.unshift({ t, kind: h === "passing" ? "ok" : "bad", html: `<b>${esc(svc)}</b> on ${esc(n.name)} is ${esc(h)}` });
    }
  }
  ev.length = Math.min(ev.length, 50);
}

// ---------- shell ----------
const ICON = {
  grid: '<svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6"><rect x="2" y="2" width="5" height="5" rx="1.2"/><rect x="9" y="2" width="5" height="5" rx="1.2"/><rect x="2" y="9" width="5" height="5" rx="1.2"/><rect x="9" y="9" width="5" height="5" rx="1.2"/></svg>',
  topo: '<svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6"><circle cx="3.5" cy="8" r="2"/><circle cx="12.5" cy="3.5" r="2"/><circle cx="12.5" cy="12.5" r="2"/><path d="M5.3 7.1l5.4-2.7M5.3 8.9l5.4 2.7"/></svg>',
  list: '<svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6"><path d="M2.5 3.5h11M2.5 6.5h8M2.5 9.5h11M2.5 12.5h6"/></svg>',
  build: '<svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6"><rect x="2" y="2" width="5" height="5" rx="1.2"/><rect x="9" y="9" width="5" height="5" rx="1.2"/><path d="M7 4.5h2.5a2 2 0 0 1 2 2V9M11.5 2v5M9 4.5h5"/></svg>',
  sun: '<svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6"><circle cx="8" cy="8" r="3.2"/><path d="M8 1.5v1.6M8 12.9v1.6M1.5 8h1.6M12.9 8h1.6M3.4 3.4l1.1 1.1M11.5 11.5l1.1 1.1M3.4 12.6l1.1-1.1M11.5 4.5l1.1-1.1"/></svg>',
  search: '<svg width="13" height="13" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.8"><circle cx="7" cy="7" r="4.5"/><path d="M10.5 10.5l3 3"/></svg>',
  term: '<svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.7"><path d="M3 4.5l3 3-3 3M8 11.5h5"/></svg>',
  svc: '<svg width="22" height="22" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5"><circle cx="8" cy="8" r="5.5"/><circle cx="8" cy="8" r="2"/></svg>',
  server: '<svg width="22" height="22" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5"><rect x="2" y="2.5" width="12" height="4.5" rx="1.2"/><rect x="2" y="9" width="12" height="4.5" rx="1.2"/></svg>',
  // A globe with a browser window: clients outside the lab.
  out: '<svg width="22" height="22" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.3"><circle cx="6.5" cy="6.5" r="5"/><path d="M1.5 6.5h10M6.5 1.5c-2.2 2.6-2.2 7.4 0 10M6.5 1.5c2.2 2.6 2.2 7.4 0 10"/><rect x="8.5" y="8.5" width="6.5" height="5.5" rx="1" fill="var(--panel,#fff)"/><path d="M8.5 10.2h6.5"/></svg>',
  gw: '<svg width="22" height="22" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5"><path d="M8 1.8l5.4 3.1v6.2L8 14.2l-5.4-3.1V4.9z"/><path d="M5.5 8h5M8.5 6l2 2-2 2"/></svg>',
};
const LOGO = '<svg width="30" height="30" viewBox="0 0 60 60" aria-hidden="true"><defs><radialGradient id="sun" cx="35%" cy="35%" r="75%"><stop offset="0" stop-color="#FFC94A"/><stop offset=".7" stop-color="#FF7A1A"/><stop offset="1" stop-color="#FF4D2E"/></radialGradient></defs><circle cx="43" cy="24" r="17" fill="url(#sun)"/><rect x="23" y="7" width="10" height="50" rx="5" fill="var(--logo-ink,#F4F1EA)"/><rect x="3" y="12" width="15" height="5" rx="2.5" fill="var(--logo-ink,#F4F1EA)"/><rect x="7" y="21.5" width="11" height="5" rx="2.5" fill="var(--logo-ink,#F4F1EA)" opacity=".65"/><rect x="11" y="31" width="7" height="5" rx="2.5" fill="var(--logo-ink,#F4F1EA)" opacity=".35"/></svg>';

function toast(m) {
  const t = $("#toast");
  t.textContent = m;
  t.classList.add("on");
  clearTimeout(t._t);
  t._t = setTimeout(() => t.classList.remove("on"), 2600);
}

function confirmDialog({ title, body, ok, check }) {
  return new Promise((resolve) => {
    const d = $("#confirm");
    $("#cf-title").textContent = title;
    $("#cf-body").textContent = body;
    $("#cf-ok").textContent = ok;
    $("#cf-extra").hidden = !check;
    $("#cf-check").checked = false;
    if (check) $("#cf-check-label").textContent = check;
    d.onclose = () => resolve(d.returnValue === "ok" ? { ok: true, checked: $("#cf-check").checked } : { ok: false });
    d.returnValue = "";
    d.showModal();
  });
}

function toggleTheme() {
  const r = document.documentElement;
  const dark = r.dataset.theme ? r.dataset.theme === "dark" : matchMedia("(prefers-color-scheme: dark)").matches;
  r.dataset.theme = dark ? "light" : "dark";
  try { localStorage.setItem("parker-theme", r.dataset.theme); } catch (e) { /* ignore */ }
}

function shell() {
  $("#root").innerHTML = `
  <div class="app">
    <aside class="side">
      <div class="brand">${LOGO}<b>parker</b><span class="ver">${esc(S.info.version || "")}</span></div>
      <h6>Labs</h6>
      <nav id="labnav"></nav>
      <h6 id="labhead"></h6>
      <nav id="viewnav"></nav>
      <h6>Build</h6>
      <nav id="buildnav"></nav>
      <div class="foot"><div class="daemon"><span class="labdot"></span><b>daemon</b><span>${esc(S.info.where || "")}</span><span id="foot-ver"></span></div></div>
    </aside>
    <main>
      <header class="top" id="top"></header>
      <section class="view on" id="view"></section>
    </main>
  </div>`;
}

function renderNav() {
  $("#labnav").innerHTML = S.labs.length ? S.labs.map((l) => {
    const st = S.labHealth[l.name] || "down";
    const on = l.name === S.route.lab;
    return `<a href="#/lab/${encodeURIComponent(l.name)}" class="labrow ${st} ${on ? "on" : ""}"><span class="labdot"></span>${esc(l.name)}<span class="count">${l.namespaces ? l.namespaces + " nodes" : "down"}</span></a>`;
  }).join("") : `<a class="labrow" style="opacity:.6">No labs yet</a>`;
  $("#buildnav").innerHTML = `<a href="#/build" class="${S.route.view === "build" ? "on" : ""}">${ICON.build}Lab builder</a>`;
  const lab = S.route.lab;
  $("#labhead").textContent = lab || "";
  $("#labhead").hidden = !lab;
  const bad = S.status && S.status.lab === lab ? (S.status.nodes || []).filter((n) => nodeState(n) === "bad").length : 0;
  $("#viewnav").innerHTML = lab ? `
    <a href="#/lab/${encodeURIComponent(lab)}" class="${!S.route.node && !S.route.view ? "on" : ""}">${ICON.grid}Overview</a>
    <a href="#/lab/${encodeURIComponent(lab)}/topology" class="${S.route.view === "topology" ? "on" : ""}">${ICON.topo}Topology</a>
    <a href="#/lab/${encodeURIComponent(lab)}/node/${encodeURIComponent(S.route.node || firstProblemNode() || "")}" class="${S.route.node ? "on" : ""}">${ICON.list}Nodes &amp; logs${bad ? `<span class="count" style="color:#FF5F52">${bad}</span>` : ""}</a>` : "";
}

function firstProblemNode() {
  const ns = (S.status && S.status.nodes) || [];
  const b = ns.find((n) => nodeState(n) === "bad") || ns.find((n) => nodeState(n) === "warn") || ns[0];
  return b ? b.name : "";
}

function stateChip(s) {
  const map = { ok: ["ok", "healthy"], warn: ["warn", "starting"], bad: ["bad", "degraded"], down: ["", "down"] };
  const [c, t] = map[s] || ["", s];
  return `<span class="chip ${c}"><span class="d"></span>${t}</span>`;
}

function renderTop() {
  const st = S.status;
  const lab = S.route.lab;
  let h = "";
  const theme = `<button class="btn icon ghost" data-act="theme" title="Switch light and dark">${ICON.sun}</button>`;
  if (S.route.view === "build") {
    h = `<div class="crumb"><span>Build</span><span class="sep">/</span><b>Lab builder</b></div><div class="meta"><span class="tag">edits a topology file; nothing starts until you Run it</span></div><div class="grow"></div>${theme}`;
  } else if (!lab) {
    h = `<div class="crumb"><b>Labs</b></div><div class="grow"></div>${theme}`;
  } else if (S.route.node) {
    h = `<div class="crumb"><a href="#/lab/${encodeURIComponent(lab)}" style="color:inherit;text-decoration:none">${esc(lab)}</a><span class="sep">/</span><span>${esc(nodeByName(S.route.node) ? groupKey(nodeByName(S.route.node)) : "")}</span><span class="sep">/</span><b>${esc(S.route.node)}</b></div><div class="grow"></div>${theme}`;
  } else {
    const meta = st && st.lab === lab ? `<div class="meta">${stateChip(labState(st))}${st.supervised ? '<span class="tag">supervised</span>' : '<span class="tag">not supervised by this daemon</span>'}${st.runDir ? `<span class="tag">${esc(st.runDir)}</span>` : ""}</div>` : "";
    const crumb = S.route.view === "topology"
      ? `<a href="#/lab/${encodeURIComponent(lab)}" style="color:inherit;text-decoration:none">${esc(lab)}</a><span class="sep">/</span><b>Topology</b>`
      : `<b>${esc(lab)}</b>`;
    h = `<div class="crumb"><span>Labs</span><span class="sep">/</span>${crumb}</div>${meta}<div class="grow"></div>${theme}
      <button class="btn sm" data-act="heal" title="Remove every partition">Heal all</button>
      <button class="btn sm danger" data-act="down">Down</button>`;
  }
  $("#top").innerHTML = h;
}

function nodeByName(name) {
  return S.status && (S.status.nodes || []).find((n) => n.name === name);
}

// ---------- overview ----------
function renderOverview() {
  const st = S.status;
  const v = $("#view");
  if (!S.route.lab) {
    v.innerHTML = `<div class="pad"><div class="card empty"><b>No lab selected</b>${S.labs.length ? "Pick a lab on the left." : "Start one with <code>parker up examples/minimal.yaml</code>; it shows up here."}</div></div>`;
    return;
  }
  if (S.statusErr) {
    v.innerHTML = `<div class="pad"><div class="card empty"><b>Can't load ${esc(S.route.lab)}</b>${esc(S.statusErr)}</div></div>`;
    return;
  }
  if (!st || st.lab !== S.route.lab) { v.innerHTML = `<div class="pad"><div class="card empty">Loading…</div></div>`; return; }
  const nodes = st.nodes || [];
  const procs = nodes.flatMap((n) => (n.processes || []).map((p) => ({ n, p, s: procState(p) })));
  const ready = procs.filter((x) => x.s === "ready").length;
  const groups = [...new Set(nodes.map(groupKey))];
  const dcs = [...new Set(nodes.map(dcOf))];
  const leaders = nodes.filter((n) => n.role === "leader").map((n) => n.name);
  const crit = nodes.flatMap((n) => Object.entries(n.health || {}).filter(([, h]) => h === "critical").map(([svc]) => ({ n, svc })));
  const downs = procs.filter((x) => x.s === "down");
  const restarts = procs.reduce((a, x) => a + (x.p.restarts || 0), 0);
  const pct = procs.length ? (ready / procs.length) * 100 : 0;

  let alert = "";
  const problems = [...downs.map((x) => ({ node: x.n.name, what: `<b>${esc(x.n.name)} / ${esc(x.p.name)}</b> is not running`, why: `restarted ${x.p.restarts} time${x.p.restarts === 1 ? "" : "s"} · ${esc(x.p.logPath || "")}` })),
    ...crit.map((c) => ({ node: c.n.name, what: `<b>${esc(c.n.name)} / ${esc(c.svc)}</b> is failing its Consul health check`, why: "catalog health: critical" }))];
  if (problems.length) {
    const p = problems[0];
    alert = `<div class="alert" role="status"><div class="ic"><svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="2"><path d="M8 4v5M8 11.5v.5"/></svg></div>
      <div class="tx">${p.what}${problems.length > 1 ? ` <span class="muted">and ${problems.length - 1} more</span>` : ""}<div class="why">${p.why}</div></div>
      <a class="btn sm" href="#/lab/${encodeURIComponent(st.lab)}/node/${encodeURIComponent(p.node)}">View logs</a>
      <button class="btn sm" data-act="restart" data-sel="${esc(p.node)}">Restart ${esc(p.node)}</button></div>`;
  }

  let rows = "";
  for (const g of groups) {
    const gn = nodes.filter((n) => groupKey(n) === g);
    const servers = gn.filter((n) => n.role === "leader" || n.role === "follower").length;
    rows += `<tr class="dc"><td colspan="8"><b>${esc(g)}</b> <span class="muted" style="margin-left:8px">${servers ? `${servers} server${servers > 1 ? "s" : ""} · ` : ""}${gn.length - servers} other node${gn.length - servers === 1 ? "" : "s"}</span></td></tr>`;
    for (const n of gn) {
      const ps = n.processes || [];
      const hv = Object.values(n.health || {});
      const pass = hv.filter((x) => x === "passing").length;
      const href = `#/lab/${encodeURIComponent(st.lab)}/node/${encodeURIComponent(n.name)}`;
      (ps.length ? ps : [null]).forEach((p, i) => {
        const first = i === 0, last = i === Math.max(ps.length, 1) - 1;
        const s = p ? procState(p) : "down";
        const hh = p ? procHealth(n, p) : "";
        const role = p ? roleOf(n, p) : { text: "" };
        rows += `<tr class="${first ? "node" : "proc"} ${last ? "last" : ""} ${s === "down" || hh === "critical" ? "sel" : ""}" data-href="${href}${p ? "?p=" + encodeURIComponent(p.name) : ""}" style="cursor:pointer">
          <td>${first ? `<a href="${href}" style="color:inherit;text-decoration:none">${esc(n.name)}</a>` : ""}</td>
          <td class="mono">${first ? esc((n.ips || [])[0] || "") + ((n.ips || []).length > 1 ? ` <span class="faint">+${n.ips.length - 1}</span>` : "") : ""}</td>
          <td class="mono">${p ? esc(p.name) : '<span class="muted">no processes</span>'}</td>
          <td class="num mono muted">${p && p.pid ? p.pid : ""}</td>
          <td>${p ? `<span class="st ${s}"><i></i>${s}</span>` : ""}${hh && hh !== "passing" ? ` <span class="chip ${hh === "critical" ? "bad" : "warn"}">${esc(hh)}</span>` : ""}</td>
          <td class="num mono ${p && p.restarts ? "" : "muted"}" ${p && p.restarts ? 'style="color:var(--bad);font-weight:600"' : ""}>${p ? p.restarts || 0 : ""}</td>
          <td class="role ${role.lead ? "lead" : ""}">${esc(role.text)}</td>
          <td>${first && hv.length ? `<span class="chip ${pass === hv.length ? "ok" : "bad"}" title="Catalog health of this node's services">${pass}/${hv.length}</span>` : first && n.membersTotal ? `<span class="chip" title="LAN members alive / known">${n.membersAlive}/${n.membersTotal}</span>` : ""}</td></tr>`;
      });
    }
  }

  const ev = S.events[st.lab] || [];
  v.innerHTML = `<div class="pad">
    <div class="stats">
      <div class="card stat"><div class="k">Processes</div><div class="v">${ready}<small> / ${procs.length} ready</small></div>
        <div class="bar"><span style="width:${pct}%;background:var(--ok)"></span><span style="width:${100 - pct}%;background:${procs.length - ready ? "var(--bad)" : "transparent"}"></span></div>
        <div class="s">${procs.length - ready ? `${procs.length - ready} not ready` : "Everything is ready"}</div></div>
      <div class="card stat"><div class="k">Nodes</div><div class="v">${nodes.length}<small> in ${dcs.length} datacenter${dcs.length === 1 ? "" : "s"}</small></div>
        <div class="s">${dcs.map((d) => `<span class="tag">${esc(d)} · ${nodes.filter((n) => dcOf(n) === d).length}</span>`).join("")}</div></div>
      <div class="card stat"><div class="k">Leader${leaders.length === 1 ? "" : "s"}</div><div class="v" style="font-size:16px;padding:4px 0 2px;line-height:1.35">${leaders.length ? leaders.map((l) => `<span class="mono" style="color:var(--accent)">${esc(l)}</span>`).join("<br>") : '<span class="muted">none yet</span>'}</div>
        <div class="s">${leaders.length === dcs.length ? "Every datacenter has a leader" : `${dcs.length - leaders.length} datacenter${dcs.length - leaders.length === 1 ? "" : "s"} without a leader`}</div></div>
      <div class="card stat"><div class="k">Restarts</div><div class="v">${restarts}</div>
        <div class="s">${st.supervised ? "Restart policies are on" : "This daemon isn't supervising the lab"}</div></div>
    </div>
    ${alert || '<div style="height:16px"></div>'}
    <div class="grid2">
      <div class="card">
        <div class="hd"><h3>Nodes and processes</h3><span class="chip">${procs.length} processes</span><div class="grow"></div></div>
        <div class="tblwrap"><table class="tbl"><thead><tr><th>Node</th><th>IP</th><th>Process</th><th class="num">PID</th><th>State</th><th class="num">Restarts</th><th>Role</th><th>Consul</th></tr></thead><tbody>${rows}</tbody></table></div>
      </div>
      <div style="display:flex;flex-direction:column;gap:16px;min-width:0">
        <div class="card">
          <div class="hd"><h3>Scenario</h3>${S.run.busy ? '<span class="chip acc"><span class="d"></span>running</span>' : S.run.result ? `<span class="chip ${S.run.result === "passed" ? "ok" : "bad"}"><span class="d"></span>${S.run.result}</span>` : ""}<div class="grow"></div>
            <button class="btn sm" data-act="run" ${S.run.busy ? "disabled" : ""}>${S.run.busy ? "Running…" : "Run scenario"}</button></div>
          ${S.run.out.length ? `<pre class="runout" id="runout">${esc(S.run.out.join("\n"))}</pre>` : '<div class="empty" style="padding:18px 16px;text-align:left">Plays the lab\'s <code>scenario:</code> and streams each step here.</div>'}
        </div>
        <div class="card">
          <div class="hd"><h3>Activity</h3><div class="grow"></div><span class="live"><i></i>Live</span></div>
          ${ev.length ? `<ul class="events">${ev.slice(0, 14).map((e) => `<li><time>${esc(e.t)}</time><i style="background:var(--${e.kind})"></i><span>${e.html}</span></li>`).join("")}</ul>` : '<div class="empty" style="padding:18px 16px;text-align:left">Restarts, state and health changes show up here while this page is open.</div>'}
        </div>
      </div>
    </div></div>`;
  const ro = $("#runout");
  if (ro) ro.scrollTop = ro.scrollHeight;
}

// ---------- node ----------
function renderNode() {
  const v = $("#view");
  const st = S.status;
  if (!st || st.lab !== S.route.lab) { v.innerHTML = `<div class="pad"><div class="card empty">Loading…</div></div>`; return; }
  const n = nodeByName(S.route.node);
  if (!n) { v.innerHTML = `<div class="pad"><div class="card empty"><b>No node ${esc(S.route.node)}</b>It isn't in ${esc(st.lab)}.</div></div>`; return; }
  const ns = nodeState(n);
  const ps = n.processes || [];
  if (!S.logs.proc || !ps.find((p) => p.name === S.logs.proc)) {
    const bad = ps.find((p) => procState(p) === "down" || procHealth(n, p) === "critical");
    S.logs.proc = (bad || ps.find((p) => p.name !== "consul") || ps[0] || {}).name || "";
  }
  const isServer = n.role === "leader" || n.role === "follower";
  const kind = isServer ? "server" : ps.some((p) => /gateway$/.test(p.name)) ? "gw" : "svc";
  const anyPaused = ps.some((p) => p.paused), anyStopped = ps.some((p) => p.stopped);
  const health = Object.entries(n.health || {});
  const keep = $("#logbody") && S.logs.key === st.lab + "/" + n.name + "/" + S.logs.proc;
  const logScroll = keep ? $("#logbody").scrollTop : 0;

  v.innerHTML = `<div class="pad">
    <div class="nhead">
      <div class="big" style="${ns === "bad" ? "background:var(--bad-soft);color:var(--bad)" : kind === "server" ? "background:var(--accent-soft);color:var(--accent)" : ""}">${ICON[kind]}</div>
      <div style="flex:1;min-width:240px">
        <h1>${esc(n.name)} ${stateChip(ns)}</h1>
        <p>${(n.ips || []).map((ip) => `<span class="tag">${esc(ip)}</span>`).join("")}<span class="tag">${esc(dcOf(n))}${n.partition ? " · " + esc(n.partition) + " partition" : ""}</span>${n.role ? `<span class="tag">${esc(n.role)}</span>` : ""}${n.membersTotal ? `<span class="tag">members ${n.membersAlive}/${n.membersTotal}</span>` : ""}</p>
      </div>
      <div class="actions">
        <button class="btn sm" data-act="shell" data-sel="${esc(n.name)}">${ICON.term}Shell</button>
        <button class="btn sm" data-act="restart" data-sel="${esc(n.name)}">Restart</button>
        <button class="btn sm" data-act="${anyStopped ? "start" : "stop"}" data-sel="${esc(n.name)}">${anyStopped ? "Start" : "Stop"}</button>
        <button class="btn sm" data-act="${anyPaused ? "resume" : "pause"}" data-sel="${esc(n.name)}">${anyPaused ? "Resume" : "Pause"}</button>
        <button class="btn sm" data-act="partition" data-sel="${esc(n.name)}">Partition</button>
        <button class="btn sm danger" data-act="kill" data-sel="${esc(n.name)}">Kill</button>
      </div>
    </div>
    <div class="ngrid">
      <div style="display:flex;flex-direction:column;gap:16px;min-width:0">
        <div class="card">
          <div class="hd"><h3>Processes</h3><div class="grow"></div><span class="chip">${ps.length}</span></div>
          <div class="plist" style="padding:12px">${ps.map((p) => {
            const s = procState(p), h = procHealth(n, p), bad = s === "down" || h === "critical";
            return `<div class="prow ${bad ? "bad" : ""} ${p.name === S.logs.proc ? "on" : ""}" data-proc="${esc(p.name)}" tabindex="0"><span><b>${esc(p.name)}</b><br><span class="mono">${p.pid ? "pid " + p.pid : "no pid"}${h ? " · " + esc(h) : ""}</span></span><span class="st ${s}"><i></i>${s}</span><span class="mono" ${p.restarts ? 'style="color:var(--bad)"' : ""}>${p.restarts || 0}↻</span></div>`;
          }).join("") || '<span class="muted">No processes</span>'}</div>
        </div>
        <div class="card">
          <div class="hd"><h3>Consul health</h3><div class="grow"></div>${health.some(([, h]) => h === "critical") ? `<span class="chip bad">${health.filter(([, h]) => h === "critical").length} critical</span>` : health.length ? '<span class="chip ok">passing</span>' : ""}</div>
          <div class="checks" style="padding:14px 16px">${health.length ? health.sort().map(([svc, h]) => `<div><span class="st ${esc(h)}"><i></i></span><span class="mono">${esc(svc)}</span><span class="st ${esc(h)}" style="font-size:12px">${esc(h)}</span></div>`).join("") : `<span class="muted">${n.role ? "No services registered on this node." : "No reachable Consul agent on this node."}</span>`}</div>
        </div>
        ${fakeSection(n.name, true, (h) => `<div class="card"><div class="hd"><h3>Fake service</h3><div class="grow"></div><span class="muted" style="font-size:12px">faults change live</span></div><div style="padding:12px 16px">${h}</div></div>`)}
        ${openSection(n.name, (h) => `<div class="card"><div class="hd"><h3>Open</h3><div class="grow"></div><span class="muted" style="font-size:12px">through the daemon</span></div><div style="padding:12px">${h}</div></div>`)}
        <div class="card">
          <div class="hd"><h3>Describe</h3><div class="grow"></div><button class="btn sm ghost" data-act="describe">${S.describe && S.describe.node === n.name ? "Refresh" : "Load"}</button></div>
          ${S.describe && S.describe.node === n.name ? `<pre class="yaml">${esc(S.describe.yaml)}</pre>` : '<div class="empty" style="padding:14px 16px;text-align:left">The node\'s expanded topology: networks, rendered files and processes.</div>'}
        </div>
      </div>
      <div class="card logs" style="min-width:0">
        <div class="logbar">
          <div class="tabs">${ps.map((p) => `<button data-proc="${esc(p.name)}" class="${p.name === S.logs.proc ? "on" : ""}"><i style="background:var(--${procState(p) === "ready" && procHealth(n, p) !== "critical" ? "ok" : "bad"})"></i>${esc(p.name)}</button>`).join("")}</div>
          <div style="flex:1"></div>
          ${levelTarget(n.name, S.logs.proc) ? `<select id="setlevel" class="lvsel" aria-label="Change ${esc(S.logs.proc)}'s log level" title="Change ${esc(S.logs.proc)}'s log level while it runs"><option value="">Log level…</option>${["trace", "debug", "info", "warn", "error"].map((l) => `<option value="${l}">${l}</option>`).join("")}<option value="reset">reset</option><option value="show">show</option></select>` : ""}
          <div class="seg">${["all", "warn", "error"].map((l) => `<button data-level="${l}" class="${S.logs.level === l ? "on" : ""}">${l[0].toUpperCase() + l.slice(1)}</button>`).join("")}</div>
          <label class="lf">${ICON.search}<input id="logfilter" placeholder="Filter lines" value="${esc(S.logs.filter)}" aria-label="Filter log lines"></label>
          <button class="toggle ${S.logs.follow ? "" : "off"}" data-act="follow"><i></i>Follow</button>
        </div>
        <div class="logbody" id="logbody"></div>
        <div class="logfoot"><span class="live" ${S.logs.ctrl ? "" : 'style="color:var(--muted)"'}><i></i>${S.logs.ctrl ? "Streaming" : "Stopped"}</span><span class="mono" style="overflow:hidden;text-overflow:ellipsis;white-space:nowrap">${esc((ps.find((p) => p.name === S.logs.proc) || {}).logPath || "")}</span><span style="margin-left:auto" id="logcount"></span></div>
      </div>
    </div></div>`;
  ensureLogs(st.lab, n.name, S.logs.proc);
  paintLogs(keep ? logScroll : null);
  const sl = $("#setlevel");
  if (sl) sl.onchange = () => { const l = sl.value; sl.value = ""; if (l) setLogLevel(n.name, S.logs.proc, l); };
  const f = $("#logfilter");
  f.oninput = () => { S.logs.filter = f.value; paintLogs(null); };
  if (S.focusFilter) { f.focus(); f.setSelectionRange(f.value.length, f.value.length); S.focusFilter = false; }
}

function levelOf(line) {
  // Explicit levels first (Consul [ERROR]/[WARN], Envoy [error]/[warning],
  // level=...), so "connect error" inside a warning stays a warning.
  const m = /\[(error|err|critical|warn|warning)\]|level=(error|warn|warning)/i.exec(line);
  if (m) return /^(warn|warning)$/i.test(m[1] || m[2]) ? "warn" : "error";
  if (/^parker[: ]/i.test(line)) return "parker";
  if (/\b(panic|fatal)\b/i.test(line)) return "error";
  return "info";
}

function paintLogs(keepScroll) {
  const body = $("#logbody");
  if (!body) return;
  const q = S.logs.filter.toLowerCase();
  const lvl = S.logs.level;
  const rows = [];
  S.logs.lines.forEach((line, i) => {
    const lv = levelOf(line);
    if (lvl === "warn" && lv !== "warn" && lv !== "error") return;
    if (lvl === "error" && lv !== "error") return;
    if (q && !line.toLowerCase().includes(q)) return;
    rows.push(`<div class="ll ${lv === "error" ? "e" : lv === "parker" ? "pk" : ""}" style="grid-template-columns:52px 1fr"><span class="n">${i + 1}</span><span${lv === "warn" ? ' class="lv warn"' : lv === "error" ? ' class="lv error"' : ""} style="font-weight:400">${esc(line)}</span></div>`);
  });
  body.innerHTML = rows.join("") || `<div class="empty" style="padding:18px">${S.logs.err ? esc(S.logs.err) : S.logs.lines.length ? "No lines match." : "No output yet."}</div>`;
  const c = $("#logcount");
  if (c) c.textContent = `${S.logs.lines.length.toLocaleString()} lines`;
  if (S.logs.follow) body.scrollTop = body.scrollHeight;
  else if (keepScroll != null) body.scrollTop = keepScroll;
}

function ensureLogs(lab, node, proc) {
  const key = lab + "/" + node + "/" + proc;
  if (S.logs.key === key && S.logs.ctrl) return;
  if (S.logs.key === key && !S.logs.ctrl && S.logs.err) return;
  stopLogs();
  S.logs.key = key; S.logs.lines = []; S.logs.err = "";
  if (!proc) return;
  const ctrl = new AbortController();
  S.logs.ctrl = ctrl;
  let partial = "";
  let pending = false;
  const dec = new TextDecoder();
  stream("Logs", { lab, node, process: proc, tailLines: 500, follow: true }, (m) => {
    const text = partial + dec.decode(b64(m.data), { stream: true });
    const parts = text.split("\n");
    partial = parts.pop();
    S.logs.lines.push(...parts);
    if (S.logs.lines.length > 5000) S.logs.lines.splice(0, S.logs.lines.length - 5000);
    if (!pending) { pending = true; requestAnimationFrame(() => { pending = false; if (S.logs.key === key) paintLogs(null); }); }
  }, ctrl.signal).catch((e) => {
    if (ctrl.signal.aborted) return;
    S.logs.err = e.message;
  }).finally(() => {
    if (S.logs.ctrl === ctrl) { S.logs.ctrl = null; if (S.route.node) render(); }
  });
}
function stopLogs() {
  if (S.logs.ctrl) { const c = S.logs.ctrl; S.logs.ctrl = null; c.abort(); }
}

// ---------- app UIs ----------
// openSection lists the web UIs a node serves (from the graph). Each opens
// through the daemon on an origin of its own (see webui/apps.go).
function openSection(nodeName, wrap) {
  const g = S.graph && S.graph.lab === S.route.lab ? S.graph : null;
  const gn = g && (g.nodes || []).find((n) => n.name === nodeName);
  const links = (gn && gn.links) || [];
  if (!links.length) return "";
  const rows = links.map((l) => `<div class="app-row">
      <button class="app-l" data-act="open" data-sel="${esc(nodeName)}" data-port="${l.port}" data-scheme="${esc(l.scheme || "http")}" data-path="${esc(l.path || "/")}">
        <span><b>${esc(l.name)}</b><small>${esc(nodeName)}:${l.port}${esc(l.path || "/")}${l.scheme === "https" ? " · TLS" : ""}</small></span><span class="go" aria-hidden="true">↗</span></button>
      ${l.token ? `<button class="btn sm ghost" data-act="copytoken" data-token="${esc(l.token)}" title="Copy the ACL token to sign in to the Consul UI">Copy token</button>` : ""}</div>`).join("");
  return wrap(`<div class="apps">${rows}</div>`);
}

async function openApp(btn) {
  // Open the window now, while the click still counts as a user gesture,
  // and point it at the app once the daemon hands out the link.
  const win = window.open("about:blank", "_blank");
  try {
    const r = await fetch("/ui/open", {
      method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ lab: S.route.lab, node: btn.dataset.sel, port: +btn.dataset.port, scheme: btn.dataset.scheme, path: btn.dataset.path }),
    });
    if (r.status === 401) { if (win) win.close(); signedOut(); return; }
    if (!r.ok) throw new Error((await r.text()).trim() || r.statusText);
    const { url } = await r.json();
    if (win) { win.opener = null; win.location.href = url; } else location.href = url;
  } catch (e) {
    if (win) win.close();
    toast("Can't open it: " + e.message);
  }
}

// ---------- log levels ----------
// levelTarget is a process whose log level can change while it runs: the
// Consul agent, an Envoy (sidecar or gateway) or a fake service.
function levelTarget(nodeName, proc) {
  return proc === "consul" || /-(sidecar|gateway)$/.test(proc || "") || fakesOn(nodeName).some((f) => f.service === proc);
}

// setLogLevel plays a log_level: step on one process and toasts the level
// in force after it.
async function setLogLevel(node, proc, level) {
  const step = { log_level: { on: node, process: proc } };
  if (level === "reset") step.log_level.reset = true;
  else if (level !== "show") step.log_level.level = level;
  let got = "", fail = "";
  try {
    await stream("Run", { lab: S.route.lab, inline: JSON.stringify({ steps: [step] }) }, (m) => {
      const line = (m.line || "").replace(/^\[[^\]]*\]\s*/, "").trim();
      if (line.startsWith("FAIL ")) fail = line.slice(5);
      else if (line && !/^(step \d+:|PASS)/.test(line)) got = line;
    });
    toast(got || `${proc}: log level ${level}`);
  } catch (e) {
    toast("Can't change the log level: " + (fail || e.message));
  }
}

// ---------- fake services ----------
// The fake services (run: fake-service) on a node, from the Fake call.
function fakesOn(nodeName) {
  const f = S.fakes && S.fakes.lab === S.route.lab ? S.fakes.list : [];
  return f.filter((x) => x.node === nodeName);
}

const goMs = (d) => {
  const u = { ns: 1e-6, us: 1e-3, "µs": 1e-3, ms: 1, s: 1e3, m: 6e4, h: 3.6e6 };
  let t = 0;
  for (const m of String(d || "").matchAll(/([\d.]+)(ns|us|µs|ms|s|m|h)/g)) t += parseFloat(m[1]) * u[m[2]];
  return t;
};
const fmtDur = (v) => (v >= 1000 ? (v / 1000).toFixed(2) + "s" : v >= 10 ? Math.round(v) + "ms" : v.toFixed(1) + "ms");
function fakeHops(r, depth = 0, out = []) {
  out.push({ r, depth });
  for (const k of Object.keys(r.upstream_calls || {}).sort()) fakeHops(r.upstream_calls[k], depth + 1, out);
  return out;
}

// waterfall draws one answer's hops, each bar placed by its start and
// sized by its duration within the whole request.
function waterfall(r) {
  const t0 = Date.parse(r.start_time) || 0, span = Math.max(goMs(r.duration), 0.001);
  return `<div class="wf">${fakeHops(r).map(({ r: h, depth }) => {
    const bad = h.code < 0 || h.code >= 400;
    const left = Math.min(100, Math.max(0, (Date.parse(h.start_time) || t0) - t0) / span * 100);
    const width = Math.max(1.5, Math.min(100 - left, goMs(h.duration) / span * 100));
    const meta = [h.node, h.dc, h.version, h.type && h.type !== "HTTP" ? h.type : "", h.injected ? "injected " + h.injected : "", h.cycle ? "cycle" : "", !h.injected && h.error ? h.error : ""].filter(Boolean).join(" · ");
    return `<div class="wfr" style="padding-left:${depth * 14}px"><div class="wfn"><b>${esc(h.name || h.uri)}</b><span class="chip ${bad ? "bad" : "ok"}">${h.code < 0 ? "failed" : h.code}</span><small>${esc(meta)}</small></div>
      <div class="wfb"><i style="left:${left}%;width:${width}%;${bad ? "background:var(--bad)" : ""}"></i></div><span class="mono">${h.duration ? esc(fmtDur(goMs(h.duration))) : ""}</span></div>`;
  }).join("")}</div>${r.fact ? `<p class="fact">✦ ${esc(r.fact)}</p>` : ""}`;
}

// fakeSection shows a node's fakes: their faults, Send request and the
// page link; full adds the fault controls (the node page).
function fakeSection(nodeName, full, wrap) {
  const fs = fakesOn(nodeName);
  if (!fs.length) return "";
  const body = fs.map((f) => {
    let faults = {};
    try { faults = JSON.parse(f.faultsJson || "{}"); } catch (e) { /* down */ }
    const d = `data-sel="${esc(nodeName)}" data-svc="${esc(f.service)}"`;
    const seg = (label, opts, cur) => `<div class="frow"><span>${label}</span><div class="seg">${opts.map(([t, ch, on]) => `<button data-act="fakeset" ${d} data-change="${esc(JSON.stringify(ch))}" class="${on ? "on" : ""}">${t}</button>`).join("")}</div></div>`;
    const rate = Math.round((faults.error_rate || 0) * 100), p50 = Math.round((faults.p50 || 0) / 1e6);
    const controls = full && f.faultsJson ? `
      ${seg("Health", [["passing", { health: { code: 200 } }, (faults.health_code || 200) < 400], ["critical", { health: { code: 503 } }, faults.health_code >= 400]])}
      ${seg("Errors", [0, 10, 50, 100].map((n) => [n + "%", { errors: { rate: n / 100 } }, rate === n]))}
      ${seg("Latency", [0, 200, 1000, 3000].map((n) => [n ? (n >= 1000 ? n / 1000 + "s" : n + "ms") : "off", { latency: { p50: n + "ms", p90: n + "ms", p99: n + "ms" } }, p50 === n]))}
      <div class="frow"><span></span><button class="btn sm ghost" data-act="fakereset" ${d}>Reset faults</button></div>` : "";
    const fc = S.fakeCall && S.fakeCall.node === nodeName && S.fakeCall.svc === f.service ? S.fakeCall : null;
    return `<div class="fake">
      <div class="fhd"><b>${esc(f.service)}</b><span class="chip">${esc(f.protocol)}${f.port ? " :" + f.port : ""}</span>${f.faulted ? '<span class="chip bad">faults</span>' : f.faultsJson ? '<span class="chip ok">healthy</span>' : ""}</div>
      <div class="mono fsum">${esc(f.summary)}</div>
      <div class="actions">${f.protocol !== "tcp" ? `<button class="btn sm" data-act="fakecall" ${d}>Send request</button><button class="btn sm ghost" data-act="open" data-sel="${esc(nodeName)}" data-port="${f.port}" data-scheme="http" data-path="/ui">Open page ↗</button>` : ""}</div>
      ${controls}
      ${fc ? (fc.err ? `<p class="err">${esc(fc.err)}</p>` : waterfall(fc.r)) : ""}</div>`;
  }).join("");
  return wrap(body);
}

async function fakeCall(node, svc) {
  try {
    const res = await call("Fake", { lab: S.route.lab, selector: node, service: svc, action: "call" });
    let r = null;
    try { r = JSON.parse(res.callBody || ""); } catch (e) { /* not JSON */ }
    if (!r || !r.name) throw new Error(`answered ${res.callStatus || "?"}: ${(res.callBody || "").slice(0, 200)}`);
    const hits = new Set(fakeHops(r).map(({ r: h }) => h.node).filter(Boolean));
    S.fakeCall = { node, svc, r, hits, t: Date.now() };
  } catch (e) {
    S.fakeCall = { node, svc, err: e.message, hits: new Set(), t: Date.now() };
  }
  render();
}

async function fakeChange(node, svc, change, reset) {
  try {
    const res = await call("Fake", { lab: S.route.lab, selector: node, service: svc, action: reset ? "reset" : "set", changeJson: change || "" });
    const got = (res.services || [])[0];
    if (got) {
      S.fakes.list = S.fakes.list.map((x) => (x.node === got.node && x.service === got.service ? got : x));
      toast(`${node}/${svc}: ${got.summary}`);
    }
  } catch (e) { toast("Can't change faults: " + e.message); }
  render();
}

// ---------- topology ----------
// The lab's architecture comes from the Graph call (fetched once per lab and
// refreshed now and then); health comes from the Status poll, so a failing
// sidecar turns its paths red within a poll.
const TW = 210, TH = 88, GX = 40, GY = 60, PAD = 28, DCGAP = 120;
const T = { layers: { upstream: true, failover: true, wan: true, peering: true }, sel: "", view: null, drag: null };

async function loadGraph(lab) {
  try {
    const g = await call("Graph", { lab });
    if (S.route.lab === lab) { S.graph = g; S.graphErr = null; }
  } catch (e) {
    if (S.route.lab === lab) { S.graphErr = e.message; }
  }
}

function graphRole(gn) {
  if (gn.role === "outside") return "out";
  if (gn.role === "server") return "srv";
  if (gn.role === "gateway" || (gn.gateways || []).length) return "gw";
  if ((gn.services || []).length) return "svc";
  return "";
}

// What's wrong with a node, in a few words, for the badge on a broken path.
function problemOf(n) {
  if (!n) return "";
  if (!n.namespace) return "not running";
  const down = (n.processes || []).find((p) => procState(p) === "down");
  if (down) return /-sidecar$/.test(down.name) ? "sidecar down" : down.name + " down";
  const crit = Object.entries(n.health || {}).find(([, h]) => h === "critical");
  if (crit) return /-sidecar-proxy$/.test(crit[0]) ? "sidecar failing" : crit[0] + " failing";
  const idle = (n.processes || []).find((p) => p.stopped || p.paused);
  if (idle) return idle.name + (idle.stopped ? " stopped" : " paused");
  return "";
}

// Namespaces (#134): a Consul namespace scopes a service registration, not
// a node, so each service and gateway gets its own chip. The graph sets
// them only where a partition has namespaces other than default.
const nsOf = (gn, key) => (gn.namespaces || {})[key] || "";
const nsChip = (gn, key) => nsOf(gn, key) ? ` <span class="nsc" title="Consul namespace">ns ${esc(nsOf(gn, key))}</span>` : "";
// nsChips: one chip per namespace on a node card, titled with what sits in it.
function nsChips(gn) {
  const by = new Map();
  for (const [k, ns] of Object.entries(gn.namespaces || {}).sort()) by.set(ns, [...(by.get(ns) || []), k]);
  return [...by].map(([ns, ks]) => `<span class="nsc" title="Consul namespace of ${esc(ks.join(", "))}">ns ${esc(ns)}</span>`).join("");
}

// layout places datacenters side by side; inside one, servers come first,
// then each partition's gateways and other nodes.
function layout(g) {
  const nodes = g.nodes || [];
  const dcs = [...new Set(nodes.map((n) => n.dc || "lab"))].sort();
  const pos = {}, regions = [];
  let x = 0, maxH = 0;
  const part = (n) => (n.partition && n.partition !== "default" ? n.partition : "default");
  const plural = (k, s) => `${k} ${s}${k === 1 ? "" : "s"}`;
  for (const dc of dcs) {
    const dn = nodes.filter((n) => (n.dc || "lab") === dc);
    const servers = dn.filter((n) => n.role === "server");
    const rest = dn.filter((n) => n.role !== "server");
    const parts = [...new Set(rest.map(part))].sort((a, b) => (a === "default" ? -1 : b === "default" ? 1 : a.localeCompare(b)));
    const boxed = parts.length > 1 || (parts.length === 1 && parts[0] !== "default");
    const groups = parts.map((p) => {
      const pn = rest.filter((n) => part(n) === p);
      return { p, gws: pn.filter((n) => n.role === "gateway"), others: pn.filter((n) => n.role !== "gateway") };
    });
    const widest = Math.max(servers.length, ...groups.map((gr) => Math.max(gr.gws.length, gr.others.length)));
    // Without partitions everything shares one grid; with them, servers
    // sit on top and each partition gets a box of its own, side by side.
    const scols = Math.max(1, Math.min(3, boxed ? servers.length : widest));
    let y = PAD + 8;
    const place = (list, x0, cols, y0) => {
      list.forEach((n, i) => { pos[n.name] = { x: x0 + (i % cols) * (TW + GX), y: y0 + Math.floor(i / cols) * (TH + GY) }; });
      return list.length ? Math.ceil(list.length / cols) * (TH + GY) : 0;
    };
    y += place(servers, x + PAD, scols, y);
    let w = scols * TW + (scols - 1) * GX;
    if (!boxed) {
      for (const gr of groups) {
        y += place(gr.gws, x + PAD, scols, y);
        y += place(gr.others, x + PAD, scols, y);
      }
    } else {
      const inset = 16;
      let bx = x + PAD, bottom = y;
      for (const gr of groups) {
        const cols = Math.max(1, Math.min(2, Math.max(gr.gws.length, gr.others.length)));
        const innerW = cols * TW + (cols - 1) * GX;
        let by = y + 30;
        by += place(gr.gws, bx + inset, cols, by);
        by += place(gr.others, bx + inset, cols, by);
        const h = by - GY + 16 - y;
        const k = gr.gws.length + gr.others.length;
        regions.push({ part: true, x: bx, y, w: innerW + 2 * inset, h, label: gr.p, sub: plural(k, "node") });
        bottom = Math.max(bottom, y + h + GY);
        bx += innerW + 2 * inset + GX;
      }
      w = Math.max(w, bx - GX - (x + PAD));
      y = bottom;
    }
    const h = y - GY + PAD;
    const gws = dn.filter((n) => n.role === "gateway").length;
    const others = dn.length - servers.length - gws;
    const sub = [servers.length && plural(servers.length, "server"), gws && plural(gws, "gateway"), others && plural(others, "other node")].filter(Boolean).join(" · ");
    // External services and the outside world sit beside the datacenters.
    const beyond = dn.length > 0 && dn.every((n) => n.role === "outside" || n.role === "external");
    regions.unshift({ x, y: 0, w: w + 2 * PAD, h, label: beyond ? "outside Consul" : dc, sub: beyond ? plural(dn.length, "node") : sub });
    maxH = Math.max(maxH, h);
    x += w + 2 * PAD + DCGAP;
  }
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

function topoState() {
  const st = S.status && S.status.lab === S.route.lab ? S.status : null;
  const byName = new Map(((st && st.nodes) || []).map((n) => [n.name, n]));
  const labDown = !st || labState(st) === "down";
  // The outside world isn't a node of the lab: it's always there.
  const outside = new Set(((S.graph && S.graph.nodes) || []).filter((n) => n.role === "outside").map((n) => n.name));
  const state = (name) => (outside.has(name) ? "ok" : byName.has(name) ? nodeState(byName.get(name)) : "down");
  // A node with a stopped or paused process carries no traffic, though it
  // isn't failing: a stopped gateway is how you test a failover.
  const idle = (name) => byName.has(name) && (byName.get(name).processes || []).some((p) => p.stopped || p.paused);
  return { st, byName, labDown, state, idle };
}

// hops of an edge, and the first of them that is broken.
function edgeHealth(e, ts) {
  const hops = [e.from, ...(e.via || []), e.to];
  if (ts.labDown || (e.kind !== "upstream" && e.kind !== "route" && e.kind !== "failover")) return { hops, bad: -1 };
  const bad = hops.findIndex((h) => ["bad", "down"].includes(ts.state(h)) || ts.idle(h));
  return { hops, bad };
}

function renderTopology() {
  const v = $("#view");
  const g = S.graph && S.graph.lab === S.route.lab ? S.graph : null;
  if (!g) {
    v.innerHTML = `<div class="pad"><div class="card empty">${S.graphErr ? `<b>Can't load the topology</b>${esc(S.graphErr)}` : "Loading…"}</div></div>`;
    return;
  }
  const ts = topoState();
  const L = layout(g);
  const kinds = new Set((g.edges || []).map((e) => (e.kind === "route" ? "upstream" : e.kind)));
  if (!T.sel || !L.pos[T.sel]) {
    const bad = (g.nodes || []).find((n) => ts.state(n.name) === "bad");
    T.sel = (bad || (g.nodes || [])[0] || {}).name || "";
  }
  const shown = (e) => T.layers[e.kind === "route" ? "upstream" : e.kind] !== false;
  // A failover path carries the traffic once every primary path of its
  // caller to that service is broken.
  const svcOf = (e) => (e.label || "").split(" ")[0];
  const active = (f) => {
    const prim = (g.edges || []).filter((e) => e.kind === "upstream" && e.from === f.from && svcOf(e) === svcOf(f));
    return !ts.labDown && prim.length > 0 && prim.every((e) => edgeHealth(e, ts).bad >= 0);
  };

  // Spread edges that share a pair of cards so they don't draw on top of each other.
  const segs = [];
  for (const e of g.edges || []) {
    if (!shown(e)) continue;
    const { hops, bad } = edgeHealth(e, ts);
    if (!hops.every((h) => L.pos[h])) continue;
    // The label goes on the hop with the most room left around it.
    const lw = 14 + (e.label || "").length * 6.6;
    const len = (i) => {
      const A = anchors(L.pos[hops[i]], L.pos[hops[i + 1]], 0);
      return Math.hypot(A.p[0] - A.q[0], A.p[1] - A.q[1]) - (A.h ? lw : 20);
    };
    let longest = 0;
    for (let i = 1; i + 1 < hops.length; i++) if (len(i) > len(longest) + 1) longest = i;
    for (let i = 0; i + 1 < hops.length; i++) {
      // The segment into the first broken hop and everything after it are broken.
      const broken = bad >= 0 && i + 1 >= bad;
      segs.push({ e, a: hops[i], b: hops[i + 1], broken, last: i + 2 === hops.length, mid: i === longest, badNode: bad >= 0 ? hops[bad] : "" });
    }
  }
  const pairN = {}, pairI = {};
  const pk = (s) => [s.a, s.b].sort().join("|");
  for (const s of segs) pairN[pk(s)] = (pairN[pk(s)] || 0) + 1;

  const color = (s) => (s.broken ? "var(--bad)" : ts.labDown ? "var(--line-2)" : s.e.kind === "wan" ? "var(--fed)" : s.e.kind === "peering" ? "var(--peer)" : s.e.kind === "failover" ? "var(--fo)" : "var(--accent)");
  let svg = `<svg class="edges" width="${L.w}" height="${L.h}"><defs>${["accent", "bad", "line-2", "fo"].map((c) => `<marker id="ah-${c}" viewBox="0 0 10 10" refX="8" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse"><path d="M0,0 L10,5 L0,10 z" fill="var(--${c})"/></marker>`).join("")}</defs>`;
  let labels = "";
  const badges = new Set(), labelled = new Set();
  for (const s of segs) {
    const k = pk(s);
    const i = (pairI[k] = (pairI[k] || 0) + 1) - 1;
    const off = (i - (pairN[k] - 1) / 2) * 14;
    const A = anchors(L.pos[s.a], L.pos[s.b], off);
    const d = curve(A.p, A.q, A.h);
    const c = color(s);
    const fo = s.e.kind === "failover", live = fo && active(s.e);
    // A standby failover path is a quiet dashed line; it flows once it's live.
    const traffic = s.e.kind === "upstream" || s.e.kind === "route" || live;
    const dash = s.e.kind === "wan" ? "5 5" : s.e.kind === "peering" ? "2 5" : fo && !live ? "1 5" : "";
    if (traffic) svg += `<path d="${d}" fill="none" stroke="${c}" stroke-opacity=".16" stroke-width="7.4" stroke-linecap="round"/>`;
    const marker = (traffic || fo) && s.last ? `marker-end="url(#ah-${s.broken ? "bad" : ts.labDown ? "line-2" : fo ? "fo" : "accent"})"` : "";
    svg += `<path d="${d}" fill="none" stroke="${c}" stroke-width="${traffic ? 2.4 : 1.8}" stroke-linecap="round" ${dash ? `stroke-dasharray="${dash}"` : ""} class="${traffic && !s.broken && !ts.labDown ? "flow" : ""}" ${marker}/>`;
    if (s.e.label && s.mid && !labelled.has(pk(s) + "|" + s.e.label)) {
      labelled.add(pk(s) + "|" + s.e.label);
      const mx = (A.p[0] + A.q[0]) / 2, my = (A.p[1] + A.q[1]) / 2 + (A.h ? (off < 0 ? -12 : off > 0 ? 12 : -12) : 0);
      labels += `<div class="elbl ${esc(s.e.kind)} ${s.broken ? "broken" : ""}" style="left:${mx}px;top:${my}px">${esc(s.e.label)}</div>`;
    }
    if (s.broken && s.badNode) badges.add(s.badNode);
  }
  for (const name of badges) {
    const p = L.pos[name], why = problemOf(ts.byName.get(name)) || "failing";
    const w = 14 + why.length * 6.8;
    svg += `<g transform="translate(${p.x + TW / 2},${p.y - 13})"><rect x="${-w / 2}" y="-11" width="${w}" height="22" rx="11" fill="var(--bad)"/><text x="0" y="4" text-anchor="middle" font-family="JetBrains Mono,ui-monospace,monospace" font-size="11" font-weight="600" fill="#fff">${esc(why)}</text></g>`;
  }
  svg += "</svg>";

  let regions = "";
  for (const r of L.regions) {
    regions += `<div class="region ${r.part ? "part" : ""}" style="left:${r.x}px;top:${r.y}px;width:${r.w}px;height:${r.h}px"><div class="lbl">${esc(r.label)}${r.part ? " partition" : ""}<span>${esc(r.sub)}</span></div></div>`;
  }
  let cards = "";
  for (const gn of g.nodes || []) {
    const p = L.pos[gn.name];
    const n = ts.byName.get(gn.name);
    const s = ts.state(gn.name);
    const ico = graphRole(gn);
    const icon = ico === "srv" ? ICON.server : ico === "gw" ? ICON.gw : ico === "out" ? ICON.out : ICON.svc;
    const hs = { ok: "var(--ok)", warn: "var(--warn)", bad: "var(--bad)", down: "var(--line-2)" }[s];
    const what = gn.role === "outside" ? "browsers, curl" : gn.role === "server" ? (n && n.role === "leader" ? "leader" : n && n.role === "follower" ? "follower" : "server")
      : (gn.gateways || []).length ? gn.gateways.join(", ") : (gn.services || []).join(", ") || gn.role;
    const procs = (n && n.processes) || [];
    const pills = procs.slice(0, 4).map((pr) => {
      const ps = procState(pr), bad = ps === "down" || procHealth(n, pr) === "critical";
      return `<span class="p"><i style="background:var(--${bad ? "bad" : ps === "ready" ? "ok" : ps === "stopped" ? "line-2" : "warn"})"></i>${esc(pr.name.replace(new RegExp("^" + (gn.services || [])[0] + "-sidecar$"), "sidecar"))}</span>`;
    }).join("") + (procs.length > 4 ? `<span class="p">+${procs.length - 4}</span>` : "") + ((!n || !n.namespace) && gn.role !== "outside" ? '<span class="p">not running</span>' : "");
    const ff = fakesOn(gn.name).filter((f) => f.faulted);
    const hit = S.fakeCall && S.fakeCall.hits && Date.now() - S.fakeCall.t < 10000 && S.fakeCall.hits.has(gn.name);
    const fbadge = ff.length ? `<span class="fbadge" title="${esc(ff.map((f) => f.service + ": " + f.summary).join("\n"))}">⚡ faults</span>` : "";
    cards += `<div class="nd ${T.sel === gn.name ? "sel" : ""} ${s === "bad" ? "bad" : ""} ${s === "down" ? "down" : ""} ${hit ? "hit" : ""}" data-tnode="${esc(gn.name)}" style="left:${p.x}px;top:${p.y}px" tabindex="0" role="button" aria-label="${esc(gn.name)}, ${s === "ok" ? "healthy" : s}">
      <div class="h"><div class="ico ${ico}">${icon}</div><div style="min-width:0"><div class="nm">${esc(gn.name)}${n && n.role === "leader" ? ' <span class="star">★</span>' : ""}</div><div class="sub"><span class="t">${esc([(gn.ips || [])[0], what].filter(Boolean).join(" · "))}</span>${nsChips(gn)}</div></div><span class="hs" style="background:${hs};box-shadow:0 0 0 3px color-mix(in srgb,${hs} 20%,transparent)"></span></div>
      <div class="ps">${pills}</div>${fbadge}</div>`;
  }

  const layerBtn = (k, label, sw) => kinds.has(k) ? `<button data-layer="${k}" class="${T.layers[k] !== false ? "on" : ""}" aria-pressed="${T.layers[k] !== false}"><span class="sw" style="background:var(${sw})"></span>${label}</button>` : "";
  const layerBtns = layerBtn("upstream", "Upstreams", "--accent") + layerBtn("failover", "Failover", "--fo") + layerBtn("wan", g.federation === "mesh-gateways" ? "WAN federation (mesh gateways)" : "WAN federation", "--fed") + layerBtn("peering", "Peering", "--peer");
  const keep = $("#stage");
  v.innerHTML = `<div class="topo">
    <div class="stage" id="stage">
      <div class="tools">${layerBtns ? `<div class="seg">${layerBtns}</div>` : ""}</div>
      <div class="world" id="world" style="width:${L.w}px;height:${L.h}px">${regions}${svg}${labels}${cards}</div>
      ${(g.nodes || []).length ? "" : '<div class="tempty"><div class="card empty"><b>No nodes</b>This lab has nothing to draw.</div></div>'}
      <div class="card legend">
        <span><i style="background:var(--ok)"></i>Healthy</span>${kinds.has("upstream") ? '<span><em style="border-color:var(--accent)"></em>Upstream path</span>' : "<span></span>"}
        <span><i style="background:var(--warn)"></i>Starting</span>${kinds.has("wan") ? '<span><em style="border-color:var(--fed);border-top-style:dashed"></em>WAN federation</span>' : kinds.has("peering") ? '<span><em style="border-color:var(--peer);border-top-style:dotted"></em>Peering</span>' : "<span></span>"}
        <span><i style="background:var(--bad)"></i>Failing</span>${kinds.has("upstream") ? '<span><em style="border-color:var(--bad)"></em>Path broken</span>' : "<span></span>"}
        ${kinds.has("wan") && kinds.has("peering") ? '<span></span><span><em style="border-color:var(--peer);border-top-style:dotted"></em>Peering</span>' : ""}
        ${kinds.has("failover") ? '<span></span><span><em style="border-color:var(--fo);border-top-style:dotted"></em>Failover (flows when used)</span>' : ""}
      </div>
      <div class="seg zoom"><button data-zoom="out" aria-label="Zoom out">−</button><button data-zoom="fit" class="${T.view && T.view.user ? "" : "on"}">Fit</button><button data-zoom="in" aria-label="Zoom in">+</button></div>
    </div>
    <aside class="tpanel" id="tpanel">${topoPanel(g, ts)}</aside></div>`;
  T.size = { w: L.w, h: L.h };
  if (!T.view || !T.view.user || !keep) fitTopo(); else applyView();
}

function fitTopo() {
  const st = $("#stage");
  if (!st || !T.size) return;
  const W = st.clientWidth, H = st.clientHeight;
  const top = 64, bottom = 24;
  const sc = Math.max(0.2, Math.min((W - 48) / Math.max(T.size.w, 1), (H - top - bottom) / Math.max(T.size.h, 1), 1.2));
  T.view = { sc, tx: (W - T.size.w * sc) / 2, ty: top + Math.max(0, (H - top - bottom - T.size.h * sc) / 2), user: false };
  applyView();
}
function applyView() {
  const w = $("#world");
  if (w && T.view) w.style.transform = `translate(${T.view.tx}px,${T.view.ty}px) scale(${T.view.sc})`;
  const f = $('[data-zoom="fit"]');
  if (f) f.classList.toggle("on", !T.view.user);
}
function zoomAt(factor, cx, cy) {
  if (!T.view) return;
  const sc = Math.max(0.2, Math.min(2.5, T.view.sc * factor));
  const k = sc / T.view.sc;
  T.view = { sc, tx: cx - (cx - T.view.tx) * k, ty: cy - (cy - T.view.ty) * k, user: true };
  applyView();
}

function topoPanel(g, ts) {
  const gn = (g.nodes || []).find((n) => n.name === T.sel);
  if (!gn) return '<section><span class="muted">Pick a node to see its details.</span></section>';
  const n = ts.byName.get(gn.name);
  const s = ts.state(gn.name);
  const ico = graphRole(gn);
  const lab = encodeURIComponent(S.route.lab);
  const nodeHref = `#/lab/${lab}/node/${encodeURIComponent(gn.name)}`;
  const kind = gn.role === "outside" ? "Clients outside the lab" : gn.role === "external" ? "External service" : gn.role === "server" ? "Consul server" : (gn.gateways || []).length ? gn.gateways.map((x) => x[0].toUpperCase() + x.slice(1)).join(", ") + " gateway"
    : gn.role === "dataplane" ? "Dataplane node" : (gn.services || []).length ? "Service node" : gn.role === "client" ? "Client agent" : "Node";
  const ps = (n && n.processes) || [];
  const health = Object.entries((n && n.health) || {}).sort();
  const conn = (e, dir) => {
    const { bad } = edgeHealth(e, ts);
    const other = dir === "out" ? e.to : e.from;
    const k = { upstream: dir === "out" ? "calls" : "called by", route: dir === "out" ? "routes to" : "routed from", failover: dir === "out" ? "fails over to" : "failover from", wan: "wan", peering: dir === "out" ? "peers with" : "peered from" }[e.kind] || e.kind;
    const via = (e.via || []).length ? `via ${e.via.join(" → ")}` : "";
    return `<a href="#" data-tnode="${esc(other)}" class="${bad >= 0 ? "bad" : ""}"><span class="k" style="color:var(--${e.kind === "wan" ? "fed" : e.kind === "peering" ? "peer" : bad >= 0 ? "bad" : e.kind === "failover" ? "fo" : "accent"})">${esc(k)}</span><span class="mono">${esc(other)}${e.label && e.kind !== "wan" ? " · " + esc(e.label) : ""}</span>${via || bad >= 0 ? `<small>${esc(via)}${bad >= 0 ? `${via ? " · " : ""}<span style="color:var(--bad)">broken at ${esc(edgeHealth(e, ts).hops[bad])}: ${esc(problemOf(ts.byName.get(edgeHealth(e, ts).hops[bad])) || "failing")}</span>` : ""}</small>` : ""}</a>`;
  };
  const outs = (g.edges || []).filter((e) => e.from === gn.name), ins = (g.edges || []).filter((e) => e.to === gn.name);
  const through = (g.edges || []).filter((e) => (e.via || []).includes(gn.name));
  if (gn.role === "outside") {
    // Not a node: no processes, logs or shell. Just what it calls.
    return `<section><div style="display:flex;gap:10px;align-items:center"><div class="ico out" style="width:34px;height:34px">${ICON.out}</div>
      <div style="min-width:0"><div style="font-weight:650;font-size:15px">${esc(gn.name)}</div><div class="muted" style="font-size:12.5px">${esc(kind)}</div></div></div>
      <p class="muted" style="margin:10px 0 0;font-size:12.5px">Browsers and other clients outside the lab, calling the ingress and API gateways' listeners.</p></section>
    ${outs.length ? `<section><h4>Connections</h4><div class="conns">${outs.map((e) => conn(e, "out")).join("")}</div></section>` : ""}`;
  }
  return `<section><div style="display:flex;gap:10px;align-items:center"><div class="ico ${ico}" style="width:34px;height:34px">${ico === "srv" ? ICON.server : ico === "gw" ? ICON.gw : ico === "out" ? ICON.out : ICON.svc}</div>
      <div style="min-width:0"><div style="font-weight:650;font-size:15px;overflow-wrap:anywhere">${esc(gn.name)}</div><div class="muted" style="font-size:12.5px">${esc(gn.dc || "lab")}${gn.partition && gn.partition !== "default" ? " · " + esc(gn.partition) : ""} · ${esc(kind)}</div></div>
      <span style="margin-left:auto">${stateChip(s)}</span></div>
      <dl class="kv" style="margin-top:12px">${(gn.ips || []).length ? `<dt>IP</dt><dd>${gn.ips.map(esc).join("<br>")}</dd>` : ""}${(gn.services || []).length ? `<dt>services</dt><dd>${gn.services.map((sv) => esc(sv) + nsChip(gn, sv)).join(", ")}</dd>` : ""}${(gn.gateways || []).some((k) => nsOf(gn, k + "-gateway")) ? `<dt>gateway</dt><dd>${gn.gateways.map((k) => esc(k) + nsChip(gn, k + "-gateway")).join(", ")}</dd>` : ""}${n && n.role ? `<dt>agent</dt><dd>${esc(n.role)}${n.membersTotal ? ` · members ${n.membersAlive}/${n.membersTotal}` : ""}</dd>` : ""}</dl></section>
    <section><h4>Processes</h4><div class="plist">${ps.map((p) => {
      const st = procState(p), h = procHealth(n, p), bad = st === "down" || h === "critical";
      return `<a class="prow ${bad ? "bad" : ""}" href="${nodeHref}?p=${encodeURIComponent(p.name)}" style="color:inherit;text-decoration:none"><span><b>${esc(p.name)}</b> <span class="mono">${p.pid ? "pid " + p.pid : ""}${h ? " · " + esc(h) : ""}</span></span><span class="st ${st}"><i></i>${st}</span><span class="mono" ${p.restarts ? 'style="color:var(--bad)"' : ""}>${p.restarts || 0}↻</span></a>`;
    }).join("") || `<span class="muted">${n ? "No processes" : "Not running"}</span>`}</div></section>
    ${health.length ? `<section><h4>Consul health</h4><div class="checks">${health.map(([svc, h]) => `<div><span class="st ${esc(h)}"><i></i></span><span class="mono">${esc(svc)}</span><span class="st ${esc(h)}" style="font-size:12px">${esc(h)}</span></div>`).join("")}</div></section>` : ""}
    ${outs.length || ins.length || through.length ? `<section><h4>Connections</h4><div class="conns">${outs.map((e) => conn(e, "out")).join("")}${ins.map((e) => conn(e, "in")).join("")}${through.map((e) => `<a href="#" data-tnode="${esc(e.from)}"><span class="k" style="color:var(--fed)">carries</span><span class="mono">${esc(e.from)} → ${esc(e.to)}</span><small>${esc(e.label)}</small></a>`).join("")}</div></section>` : ""}
    ${fakeSection(gn.name, false, (h) => `<section><h4>Fake service</h4>${h}</section>`)}
    ${openSection(gn.name, (h) => `<section><h4>Open</h4>${h}</section>`)}
    <section><h4>Actions</h4><div class="actions"><a class="btn sm" href="${nodeHref}">Logs</a><button class="btn sm" data-act="shell" data-sel="${esc(gn.name)}">${ICON.term}Shell</button><button class="btn sm" data-act="restart" data-sel="${esc(gn.name)}">Restart</button><button class="btn sm" data-act="partition" data-sel="${esc(gn.name)}">Partition</button><button class="btn sm danger" data-act="kill" data-sel="${esc(gn.name)}">Kill</button></div></section>`;
}

// Pan by dragging the background, zoom with the wheel or the buttons.
document.addEventListener("pointerdown", (e) => {
  const st = e.target.closest("#stage");
  if (!st || e.button !== 0 || e.target.closest(".nd,.tools,.zoom,.legend") || !T.view) return;
  T.drag = { x: e.clientX, y: e.clientY, tx: T.view.tx, ty: T.view.ty, id: e.pointerId };
  st.setPointerCapture(e.pointerId);
  st.classList.add("drag");
});
document.addEventListener("pointermove", (e) => {
  if (!T.drag || e.pointerId !== T.drag.id) return;
  T.view = { ...T.view, tx: T.drag.tx + e.clientX - T.drag.x, ty: T.drag.ty + e.clientY - T.drag.y, user: true };
  applyView();
});
const endDrag = (e) => {
  if (!T.drag || e.pointerId !== T.drag.id) return;
  T.drag = null;
  const st = $("#stage");
  if (st) st.classList.remove("drag");
};
document.addEventListener("pointerup", endDrag);
document.addEventListener("pointercancel", endDrag);
document.addEventListener("wheel", (e) => {
  const st = e.target.closest("#stage");
  if (!st || e.target.closest(".tools,.legend")) return;
  e.preventDefault();
  const r = st.getBoundingClientRect();
  zoomAt(Math.exp(-e.deltaY * 0.0015), e.clientX - r.left, e.clientY - r.top);
}, { passive: false });
addEventListener("resize", () => { if (S.route.view === "topology" && T.view && !T.view.user) fitTopo(); });

// ---------- actions ----------
async function fault(verb, sel) {
  const lab = S.route.lab;
  const step = verb === "partition" ? { partition: [[sel]] } : verb === "heal" ? { heal: sel || "all" } : { [verb]: sel };
  const label = `${verb} ${sel || ""}`.trim();
  if (S.busy) { toast("Still running: " + S.busy); return; }
  S.busy = label;
  toast(label + "…");
  let last = "";
  try {
    await stream("Run", { lab, inline: JSON.stringify({ steps: [step] }) }, (m) => { if (m.line && m.line.trim()) last = m.line; });
    toast(last ? last.trim() : label + ": done");
  } catch (e) {
    toast(`${label} failed: ${e.message}`);
  } finally {
    S.busy = "";
    poll();
  }
}

async function onAction(btn) {
  const act = btn.dataset.act, sel = btn.dataset.sel;
  const lab = S.route.lab;
  switch (act) {
    case "theme": toggleTheme(); return;
    case "open": openApp(btn); return;
    case "fakecall": fakeCall(sel, btn.dataset.svc); return;
    case "fakeset": fakeChange(sel, btn.dataset.svc, btn.dataset.change, false); return;
    case "fakereset": fakeChange(sel, btn.dataset.svc, "", true); return;
    case "copytoken":
      try { await navigator.clipboard.writeText(btn.dataset.token); toast("Copied the ACL token. Paste it in the Consul UI's Log in."); } catch (e) { toast("Token: " + btn.dataset.token); }
      return;
    case "follow": S.logs.follow = !S.logs.follow; btn.classList.toggle("off", !S.logs.follow); paintLogs(null); return;
    case "describe":
      try { const r = await call("Describe", { lab, node: S.route.node }); S.describe = { node: S.route.node, yaml: r.yaml || "" }; render(); } catch (e) { toast(e.message); }
      return;
    case "shell": {
      const cmd = `parker exec ${lab} ${sel}`;
      try { await navigator.clipboard.writeText(cmd); toast("Copied: " + cmd); } catch (e) { toast("Run in a terminal: " + cmd); }
      return;
    }
    case "kill": {
      const r = await confirmDialog({ title: `Kill ${sel}?`, body: "Sends SIGKILL to every process on the node. Restart policies bring them back.", ok: "Kill" });
      if (r.ok) fault("kill", sel);
      return;
    }
    case "stop": {
      const r = await confirmDialog({ title: `Stop ${sel}?`, body: "Its processes stay down until you start them again.", ok: "Stop" });
      if (r.ok) fault("stop", sel);
      return;
    }
    case "partition": {
      const r = await confirmDialog({ title: `Partition ${sel}?`, body: "Cuts the network between this node and the rest of the lab. Heal all puts it back.", ok: "Partition" });
      if (r.ok) fault("partition", sel);
      return;
    }
    case "restart": case "start": case "pause": case "resume": fault(act, sel); return;
    case "heal": fault("heal", "all"); return;
    case "run": runScenario(); return;
    case "down": {
      const r = await confirmDialog({ title: `Take ${lab} down?`, body: "Kills every process and removes the lab's namespaces and links. Logs are kept unless you purge.", ok: "Down", check: "Also delete its logs, configs and data (purge)" });
      if (!r.ok) return;
      toast(`down ${lab}…`);
      try {
        await stream("Down", { lab, purge: !!r.checked }, () => {});
        toast(`${lab} is down`);
      } catch (e) { toast("down failed: " + e.message); }
      await loadLabs();
      poll();
      return;
    }
  }
}

async function runScenario() {
  S.run = { busy: true, out: [], result: "" };
  render();
  try {
    await stream("Run", { lab: S.route.lab }, (m) => { S.run.out.push(m.line || ""); if (!S.route.node) render(); });
    S.run.result = "passed";
  } catch (e) {
    S.run.out.push(e.message);
    S.run.result = "failed";
  }
  S.run.busy = false;
  render();
}

// ---------- routing and polling ----------
function parseRoute() {
  const h = location.hash.replace(/^#\/?/, "");
  const [path, query] = h.split("?");
  const parts = path.split("/").map(decodeURIComponent);
  const r = { lab: "", node: "", view: "" };
  if (parts[0] === "build") r.view = "build";
  if (parts[0] === "lab") {
    r.lab = parts[1] || "";
    if (parts[2] === "node") r.node = parts[3] || "";
    if (parts[2] === "topology") r.view = "topology";
  }
  const qp = new URLSearchParams(query || "");
  if (qp.get("p")) S.logs.proc = qp.get("p");
  return r;
}

function render() {
  if (!S.info) return;
  renderNav();
  renderTop();
  $("#view").classList.toggle("full", S.route.view === "topology" || S.route.view === "build");
  if (S.route.view === "build") { stopLogs(); S.logs.key = ""; renderBuild(); return; }
  if (S.route.node) renderNode();
  else {
    stopLogs(); S.logs.key = "";
    if (S.route.view === "topology") renderTopology(); else renderOverview();
  }
}

async function loadLabs() {
  try {
    const r = await call("ListLabs");
    S.labs = (r.labs || []).sort((a, b) => a.name.localeCompare(b.name));
  } catch (e) { if (e.code !== "unauthenticated") toast("Can't list labs: " + e.message); }
}

let pollTimer = null, sideTick = 0;
async function poll() {
  clearTimeout(pollTimer);
  const lab = S.route.lab;
  if (lab) {
    try {
      const st = await call("Status", { lab });
      if (S.route.lab === lab) {
        diff(lab, S.status, st);
        const same = S.status && JSON.stringify(S.status) === JSON.stringify(st);
        S.status = st; S.statusErr = null;
        S.labHealth[lab] = labState(st);
        S.unchanged = same;
      }
    } catch (e) {
      if (S.route.lab === lab) { S.statusErr = e.message; S.status = null; }
    }
  }
  if (lab && (S.route.view === "topology" || S.route.node)) {
    try {
      const fr = await call("Fake", { lab });
      const list = fr.services || [];
      if (S.route.lab === lab) {
        if (!S.fakes || JSON.stringify(S.fakes.list) !== JSON.stringify(list)) S.unchanged = false;
        S.fakes = { lab, list };
      }
    } catch (e) { if (S.route.lab === lab) S.fakes = { lab, list: [] }; }
  }
  // The architecture only changes when the lab is brought up again.
  if (lab && (S.route.view === "topology" || S.route.node) && (sideTick % 5 === 0 || !S.graph || S.graph.lab !== lab)) {
    const before = JSON.stringify(S.graph);
    await loadGraph(lab);
    if (JSON.stringify(S.graph) !== before) S.unchanged = false;
  }
  if (sideTick++ % 5 === 0) {
    await loadLabs();
    await Promise.all(S.labs.filter((l) => l.name !== lab).map(async (l) => {
      try { S.labHealth[l.name] = labState(await call("Status", { lab: l.name })); } catch (e) { S.labHealth[l.name] = "down"; }
    }));
  }
  // Don't redraw a node page under the user's text selection or typing.
  const typing = document.activeElement && document.activeElement.id === "logfilter";
  const selecting = String(window.getSelection && window.getSelection()).length > 0;
  // Nor under an open log level picker.
  const picking = document.activeElement && document.activeElement.id === "setlevel";
  // On a node page, redraw only when something changed: the log view
  // updates itself as lines stream in.
  // The topology redraws only on change too, and never mid-drag.
  const still = (S.route.node || S.route.view === "topology") && S.unchanged;
  if (S.route.view === "build") renderNav(); // the builder draws itself
  else if (!selecting && !picking && !still && !T.drag) { if (typing) S.focusFilter = true; render(); }
  S.unchanged = false;
  pollTimer = setTimeout(poll, document.hidden ? 10000 : 2000);
}

function onRoute() {
  const r = parseRoute();
  if (!r.lab && r.view !== "build" && S.labs.length) {
    location.replace("#/lab/" + encodeURIComponent(S.labs[0].name));
    return;
  }
  if (r.lab !== S.route.lab) { S.status = null; S.statusErr = null; S.run = { busy: false, out: [] }; S.describe = null; S.graph = null; S.graphErr = null; T.sel = ""; T.view = null; }
  S.route = r;
  render();
  poll();
}

// ---------- sign in ----------
function signedOut() {
  if (!S.info) return;
  S.info = null;
  stopLogs();
  clearTimeout(pollTimer);
  loginPage("Your session ended. Sign in again.");
}

function loginPage(err) {
  $("#root").innerHTML = `<div class="login"><form id="login">
    <div style="display:flex;gap:10px;align-items:center">${LOGO}<h1>Parker</h1></div>
    <p>Paste the daemon's API token. On the daemon host, <code>parker daemon credentials</code> prints it. Or run <code>parker ui</code> on your machine, which signs you in.</p>
    <input id="tok" type="password" autocomplete="off" placeholder="PARKER_TOKEN" aria-label="API token" required>
    ${err ? `<div class="err">${esc(err)}</div>` : ""}
    <button class="btn primary" type="submit">Sign in</button></form></div>`;
  $("#login").onsubmit = async (e) => {
    e.preventDefault();
    const ok = await signIn($("#tok").value);
    if (ok) start(); else loginPage("That token didn't work.");
  };
  $("#tok").focus();
}

async function signIn(token) {
  const r = await fetch("/ui/session", { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ token }) });
  return r.ok;
}

async function start() {
  const r = await fetch("/ui/info", { credentials: "same-origin" });
  const info = await r.json();
  if (!info.signedIn) { loginPage(""); return; }
  S.info = info;
  shell();
  await loadLabs();
  onRoute();
}

document.addEventListener("click", (e) => {
  const b = e.target.closest("[data-act]");
  if (b) { e.preventDefault(); onAction(b); return; }
  const lv = e.target.closest("[data-level]");
  if (lv) { S.logs.level = lv.dataset.level; $$("[data-level]").forEach((x) => x.classList.toggle("on", x === lv)); paintLogs(null); return; }
  const tn = e.target.closest("[data-tnode]");
  if (tn) {
    e.preventDefault();
    T.sel = tn.dataset.tnode;
    $$("#world .nd").forEach((x) => x.classList.toggle("sel", x.dataset.tnode === T.sel));
    if (S.graph) $("#tpanel").innerHTML = topoPanel(S.graph, topoState());
    return;
  }
  const ly = e.target.closest("[data-layer]");
  if (ly) { T.layers[ly.dataset.layer] = T.layers[ly.dataset.layer] === false; render(); return; }
  const zm = e.target.closest("[data-zoom]");
  if (zm) {
    const st = $("#stage");
    if (zm.dataset.zoom === "fit") fitTopo();
    else zoomAt(zm.dataset.zoom === "in" ? 1.2 : 1 / 1.2, st.clientWidth / 2, st.clientHeight / 2);
    return;
  }
  const pr = e.target.closest("[data-proc]");
  if (pr) { S.logs.proc = pr.dataset.proc; render(); return; }
  const row = e.target.closest("tr[data-href]");
  if (row && !e.target.closest("a")) { location.hash = row.dataset.href; }
});
document.addEventListener("keydown", (e) => {
  if (e.key === "Enter" && e.target.matches(".prow")) e.target.click();
  if ((e.key === "Enter" || e.key === " ") && e.target.matches(".nd[data-tnode]")) { e.preventDefault(); e.target.click(); }
});
document.addEventListener("dblclick", (e) => {
  const nd = e.target.closest(".nd[data-tnode]");
  if (nd) location.hash = `#/lab/${encodeURIComponent(S.route.lab)}/node/${encodeURIComponent(nd.dataset.tnode)}`;
});
addEventListener("hashchange", onRoute);
document.addEventListener("visibilitychange", () => { if (!document.hidden && S.info) poll(); });

(async () => {
  // A link from `parker ui` carries a one-time token in the fragment: swap
  // it for a session cookie and drop it from the address bar and history.
  const frag = new URLSearchParams(location.hash.replace(/^#/, ""));
  if (frag.get("token")) {
    const lab = frag.get("lab");
    const ok = await signIn(frag.get("token"));
    history.replaceState(null, "", location.pathname + (lab ? "#/lab/" + encodeURIComponent(lab) : ""));
    if (!ok) { loginPage("The sign-in link didn't work. It is only valid while that parker ui runs."); return; }
  }
  start();
})();
