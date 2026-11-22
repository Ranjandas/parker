// The web UI's fake panel logic: what a fake service calls, who calls it,
// and the faults to send on one request (X-Parker-Fault). Plain JS with
// no DOM, so node can run it too (see webui/fakepanel_test.go).
//
// g is the Graph response; fakes are the lab's fake services
// ({node, service, protocol}) from the Fake call.
"use strict";

(function (root) {
// Upstream and failover edges go from the calling node to the called one,
// labelled with the service's name first ("api :9091", "api failover (dc2)").
const edgeSvc = (e) => String(e.label || "").split(" ")[0];
const callEdge = (e) => e.kind === "upstream" || e.kind === "failover";
const fakesOn = (fakes, node) => (fakes || []).filter((f) => f.node === node);
// callable: a fake on that node answers HTTP or gRPC, so a request can start there.
const callable = (fakes, node) => fakesOn(fakes, node).some((f) => f.protocol !== "tcp");

// upstreamsOf names the services a node calls directly.
function upstreamsOf(g, node) {
  return [...new Set(((g && g.edges) || []).filter((e) => e.kind === "upstream" && e.from === node).map(edgeSvc))].sort();
}

// callersOf are the nodes with a fake that call svc on node, directly or
// as its failover.
function callersOf(g, fakes, node, svc) {
  return [...new Set(((g && g.edges) || []).filter((e) => callEdge(e) && e.to === node && edgeSvc(e) === svc && callable(fakes, e.from)).map((e) => e.from))].sort();
}

// fakeChain lists the fakes a request to node can reach, nearest first:
// [{name, via}], via being the service that calls it ("" for a direct
// upstream). Per-request faults target a service by name, so each name
// is listed once, and never self.
function fakeChain(g, fakes, node, self) {
  const out = new Map(), seen = new Set([node]);
  let frontier = [{ node, svc: "" }];
  while (frontier.length) {
    const next = [];
    for (const f of frontier) {
      for (const e of (g && g.edges) || []) {
        if (!callEdge(e) || e.from !== f.node) continue;
        const svc = edgeSvc(e);
        if (svc !== self && !out.has(svc) && fakesOn(fakes, e.to).some((x) => x.service === svc)) out.set(svc, f.svc);
        if (!seen.has(e.to)) { seen.add(e.to); next.push({ node: e.to, svc }); }
      }
    }
    frontier = next;
  }
  return [...out].map(([name, via]) => ({ name, via }));
}

// Faults on one request: rows of {svc, fail, delay}, fail in percent and
// delay in ms. A row with neither sends nothing.
const msLabel = (n) => (n ? (n >= 1000 ? n / 1000 + "s" : n + "ms") : "off");
const rqActive = (rows) => (rows || []).filter((r) => r.svc && (r.fail || r.delay));
function rqHeader(rows) {
  return rqActive(rows).map((r) => r.svc + "=" + [r.fail ? "error-rate:" + r.fail : "", r.delay ? "delay:" + r.delay + "ms" : ""].filter(Boolean).join(",")).join(";");
}
const rqDesc = (r) => [r.fail ? "fails " + r.fail + "%" : "", r.delay ? "+" + msLabel(r.delay) : ""].filter(Boolean).join(", ");
function rqSummary(rows) {
  const a = rqActive(rows);
  return a.length ? `${a.length} fault${a.length > 1 ? "s" : ""}: ${a.map((r) => r.svc + " " + rqDesc(r)).join(" · ")}` : "";
}

const api = { edgeSvc, upstreamsOf, callersOf, fakeChain, msLabel, rqActive, rqHeader, rqDesc, rqSummary };
if (typeof module === "object" && module.exports) module.exports = api;
else root.FakePanel = api;
})(typeof window === "object" ? window : globalThis);
