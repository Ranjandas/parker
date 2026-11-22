---
title: The fake service
description: A built-in demo service that shows every hop of a request, with faults you can inject while it runs.
---

`run: fake-service` runs Parker's built-in fake service on a node: a small
server that answers with who it is (name, node, dc, version) and, on each
request, calls its upstreams and nests their answers with the time each hop
took. One request to the front of a chain shows the whole path, which makes
mesh, failover and traffic-split repros visible without writing an app.

It follows [nicholasjackson/fake-service](https://github.com/nicholasjackson/fake-service)'s
env var names and response shape, so habits carry over, and takes a few
ideas from [consul-up/birdwatcher](https://github.com/consul-up/birdwatcher)
(per-request faults, a page with fault controls, a traffic loop). No code is
copied from either.

```yaml
nodes:
  web:
    service: { port: 8080, run: fake-service, connect: sidecar, upstreams: { api: 9091 } }
  api:
    service:
      port: 9090
      run: fake-service
      connect: sidecar
      fake:
        version: v2
        latency: { p50: 20ms, p99: 400ms }
        errors: { rate: 0.1, code: 503 }
```

```console
$ parker call lab.yaml dc1/web
web  dc1-web · dc1  200  23ms
└─ api  dc1-api · dc1 · v2  200  21ms
```

See [`examples/fake-chain.yaml`](https://github.com/Ranjandas/parker/blob/main/examples/fake-chain.yaml) for a two-DC
failover demo and [`hack/e2e/fake-chain.yaml`](https://github.com/Ranjandas/parker/blob/main/hack/e2e/fake-chain.yaml)
for HTTP, gRPC and TCP hops in one chain.

## The `fake:` block

`fake:` sits on a service (or in `service_template`, merged field by field)
and needs `run: fake-service`. Every key has a fake-service env var
equivalent where one exists; set env vars under the service's `env:` if
you prefer them. The `fake:` block wins over env vars.

| Key | Env var | Default | Meaning |
| --- | --- | --- | --- |
| `protocol` | | `http` | `http`; `grpc` (fake-service's `FakeService.Handle` and `grpc.health.v1` over h2c, next to the HTTP API on the same port); or `tcp` (a line echo server). |
| `message` | `MESSAGE` | | The answer's `body`. JSON nests as JSON. |
| `version` | `VERSION` | | Shown on every answer; also picks the space facts set. |
| `upstreams` | `UPSTREAM_URIS` | the service's `upstreams:` through its sidecar | URIs called on each request: `http://`, `grpc://` or `tcp://`. |
| `workers` | `UPSTREAM_WORKERS` | `1` | Upstreams called at once. |
| | `HTTP_CLIENT_REQUEST_TIMEOUT` | `30s` | Bound on each upstream call. |
| `upstream_failure` | | `fail` | `fail` answers 500 when an upstream fails; `ignore` keeps 200. |
| `health.code` | `HEALTH_CHECK_RESPONSE_CODE` | `200` | What `/health` answers. Parker's Consul check uses `/health`, so `503` makes the service critical while the process keeps running. |
| `ready.delay` | `READY_CHECK_RESPONSE_DELAY` | `0s` | How long after start `/ready` keeps failing. `parker up` waits on `/ready`. |
| `ready.code` | `READY_CHECK_RESPONSE_SUCCESS_CODE` | `200` | `/ready` once ready. |
| `ready.failure_code` | `READY_CHECK_RESPONSE_FAILURE_CODE` | `503` | `/ready` before that. |
| `latency.p50` | `TIMING_50_PERCENTILE` | `0s` | Latency for half the requests. |
| `latency.p90` | `TIMING_90_PERCENTILE` | p50 | Latency up to the 90th percentile. |
| `latency.p99` | `TIMING_99_PERCENTILE` | p90 | Latency beyond. |
| `latency.variance` | `TIMING_VARIANCE` | `0` | Spread of each draw, in percent. |
| `errors.rate` | `ERROR_RATE` | `0` | Share of requests that fail, 0 to 1. |
| `errors.code` | `ERROR_CODE` | `500` | Code of a failed request. |
| `errors.type` | `ERROR_TYPE` | `http_error` | `http_error`, or `delay`: the request waits `errors.delay`, then succeeds. |
| `errors.delay` | `ERROR_DELAY` | `0s` | The wait for `type: delay`. |
| `rate_limit.rps` | `RATE_LIMIT` | `0` (off) | Requests per second; a burst of one second's worth. |
| `rate_limit.code` | `RATE_LIMIT_CODE` | `503` | Code for requests over the limit. |
| `seed` | `RAND_SEED` | random | Makes error and latency draws repeatable. |
| `request_faults` | | `true` | Lets a caller ask for faults on one request (below). |
| `facts` | | `true` | Adds a space fact to each answer. `false` keeps shared output clean. |
| `log_level` | `LOG_LEVEL` | `info` | `debug` adds each request's headers and upstream calls; `info` logs every request; `warn` only failed or faulted ones; `error` only failed ones. Changes at runtime with a `log_level:` step or `parker logs --level`. |

The order on each request is: rate limit, then errors, then latency, then
the upstream calls.

Parker also sets things up around the fake:

- The Consul check is `http` on `/health`. For `protocol: grpc` it is
  Consul's `grpc` check, and for `protocol: tcp` it is `tcp`. `check:`
  overrides it.
- A `grpc` fake in the mesh gets `service-defaults` with `Protocol: grpc`,
  unless the lab's own `config_entries` set service-defaults for it.
- In a mesh with no `fake.upstreams`, the fake calls each of the service's
  `upstreams:` at `http://127.0.0.1:<port>/`, through its sidecar.

## The answer

```json
{
  "name": "web", "node": "dc1-web", "dc": "dc1", "version": "v1",
  "uri": "/", "type": "HTTP", "ip_addresses": ["10.1.0.104"],
  "start_time": "...", "end_time": "...", "duration": "2.1ms",
  "from": "127.0.0.1:41234", "body": "hello", "code": 200,
  "upstream_calls": {
    "http://127.0.0.1:9091/": { "name": "api", "dc": "dc1", "code": 503, "injected": "error", ... }
  },
  "fact": "Sunlight takes about 8 minutes and 20 seconds to reach Earth."
}
```

- `upstream_calls` is keyed by URI. Each answer is the upstream's own,
  except that `uri` and `code` are what the caller saw (through Envoy, the
  code may differ). An upstream that can't be reached has `code: -1` and an
  `error`.
- `injected` names a fault injected at that hop: `error`, `delay` or
  `rate_limit`. A failure deep in a chain shows where it came from.
- `cycle: true` means the service was already on the request's path, so
  it answered without calling its upstreams again. The path travels in
  `X-Parker-Trail`, and a chain stops at 16 hops.
- `X-Request-Id` is passed down the chain, so every hop logs the same id.

Endpoints: `/` (any path) answers as above, `/health` and `/ready`
answer `{"status","code"}`, and `/ui` serves the fake's own page (HTTP and
gRPC modes).

### Faults on one request

Unless `request_faults: false`:

- `?error-rate=50&delay=2s` applies to this hop only. `error-rate` is a
  percentage.
- `X-Parker-Fault: api=error-rate:100;db=delay:2s` targets hops by name
  anywhere in the chain. The header is passed down with the request.

```console
$ parker call lab.yaml dc1/web -H 'X-Parker-Fault: db=error-rate:100'
```

## Changing faults while it runs

Each fake listens on a control socket in its node's run dir
(`<svc>.fake.sock`), never on the service port. Three things use it, and a
change only touches the fields it names.

The CLI:

```console
$ parker fake lab.yaml dc1/api                         # show the faults in force
$ parker fake lab.yaml dc1/api --health 503            # Consul check fails, process stays up
$ parker fake lab.yaml dc1/api --error-rate 0.3 --error-code 503
$ parker fake lab.yaml dc1/api --latency p50=200ms,p99=2s --variance 10
$ parker fake lab.yaml dc1/api --rate-limit 50 --rate-limit-code 429
$ parker fake lab.yaml dc1/api --reset                 # back to the lab's settings
```

A scenario step:

```yaml
- fake: { on: dc1/api, health: { code: 503 } }
- fake: { on: dc1/api, errors: { rate: 0.5 }, latency: { p50: 1s } }
- fake: { on: dc1/api, reset: true }
```

The fake's page and the web UI, both described below.

## Seeing the chain

`parker call FILE|LAB NODE [PATH]` sends one request from inside the node
and prints the chain as a tree. It goes through the daemon, so it works on
a remote host too.

- `--json` prints the raw answer.
- `--fact` shows each hop's fact.
- `-H 'Name: value'` adds a header.
- `--every 1s [--count N]` sends a request every interval and prints a line
  for each. Every 10 requests, and at the end, it prints the split, for
  example `api: dc1 70% · dc2 30%`.

A scenario can check a chain with `call:`, retried with `within:`:

```yaml
- assert: { call: { on: dc1/web, code: 200, reaches: dc2/api, body: rows }, within: 60s }
```

`reaches` takes a service name, `dc/name` or a node name. `code` defaults
to 200. `path`, `header` and `service` work like the CLI's options.

### The fake's page (`/ui`)

Open it from the web UI (`parker ui`): pick the node, then the fake under
**Open**.
The page shows:

- **Request**: Send request, and a loop that sends one every 0.5 to 5
  seconds. A strip of recent answers is coloured by who answered, with the
  traffic split under it.
- **Chain**: the last answer as a waterfall.
- **Faults**: a health toggle, sliders for error rate and latency, the
  error code, the rate limit, and Reset. Changes apply live.
- **Space fact**: the fact from the last answer.

The fault panel writes through the daemon to the control socket. Opened
on the service port directly, the page works but the panel is read only,
so nothing in the mesh can change another service's faults.

### The web UI

On a node page (**Nodes & logs**), each fake service gets two cards.

**Fake service** shows what the service calls, and lets you send a
request through it:

- **Test the path** appears when the service has upstreams. It lists
  them, and **Send request through web** sends a request to web, which
  calls its upstreams. **Last call** draws the answer as a waterfall, one
  row per hop with its status and time.
- **+ Add a fault to this request** breaks services further down the
  chain for that request only, through the
  [`X-Parker-Fault` header](#faults-on-one-request). Pick a service, then
  **Fail** (10%, 50% or 100% of the time) and **Delay** (200ms, 1s or
  3s), and add more rows for more services. The picker lists every fake
  the request can reach, so from web you can break db behind api
  (`db (via api)`). A line under the button sums up what will be sent,
  such as `2 faults: api fails 100% · db +1s`, and the hops you broke are
  marked *this request* in **Last call**. Their own fault settings don't
  change.
- A service with no upstreams has nothing to send a request through. It
  says so, and **Called by** links to the nodes that call it, where you
  can send one and watch its faults.

**Fault injection: web** changes how web answers the requests it gets,
for every caller, until you change it back:

| Control | Does |
|---|---|
| Health check | `passing` or `critical`: what web's health check answers, so Consul takes it out of the pool. |
| Fail requests | 0%, 10%, 50% or 100% of requests to web fail with its error code. |
| Slow responses | Adds 200ms, 1s or 3s before web answers. |
| Reset faults | Back to the lab file's `fake:` settings. |

These never touch web's upstreams: to make api fail for web, set api's
faults on api's node, or add a fault to one request as above.

On the topology, a node with a fake service gets the same **Fake
service** section, without the per-request faults. Sending a request
there also highlights the nodes it went through. A **⚡ faults** badge
marks nodes whose fakes have faults injected.

Limits: the picker reads the lab's topology, so it lists a fake that sets
`request_faults: false` too, and that fake ignores the fault. A TCP fake
has no requests to send, so it only gets the fault card.

## Space facts

Each answer carries a short fact, a new one each time, taken in turn from
about 60 checked against NASA's mission pages. Each version gets its own
set: odd versions (and no version) get the Parker Solar Probe and the Sun,
and even versions get light and deep space. A canary split shows at a
glance, and a fact that changes proves a fresh answer came back.
`facts: false` drops them.

## gRPC and TCP

`protocol: grpc` serves fake-service's `FakeService.Handle` (`Request{bytes
data}` and `Response{string Message}`, where the message is the JSON
answer), so fake-service's clients work. It also serves
`grpc.health.v1.Health/Check`, which reports NOT_SERVING while health is
400 or more. An injected error comes back as a gRPC status (503 is
Unavailable, 429 is ResourceExhausted, and so on). `grpc://host:port`
upstreams are called over gRPC.

`protocol: tcp` greets each connection with `<name> <node> <dc>`, then
echoes each line. The rate limit and injected errors drop the connection,
and latency delays the greeting. While health is 400 or more it stops
listening, so the `tcp` check goes critical. A `tcp://` upstream is
checked with a connect, the greeting and a line round trip.

## Compared with fake-service

| fake-service | Parker |
| --- | --- |
| Name, message, upstreams, workers, client timeout | Same env vars, plus `fake:` keys |
| Response JSON (`upstream_calls`, `duration`, `code`, ...) | Same shape, plus `node`, `dc`, `version`, `injected`, `cycle`, `fact` |
| Timing percentiles and variance | Same |
| Error injection (rate, code, type, delay) | Same |
| Rate limiting | Same |
| Health and ready checks, ready delay | Same |
| gRPC `FakeService.Handle` | Same, plus `grpc.health.v1` |
| HTTP and gRPC on one port | Same (h2c) |
| — | TCP echo mode |
| — | Runtime fault changes (`parker fake`, `fake:` steps, the page) |
| — | Per-request faults (`?error-rate=`, `X-Parker-Fault`) |
| — | Cycle guard |
| Web UI (`/ui`) | Parker's own page with fault controls and a traffic loop |
| Tracing (Zipkin, Jaeger, Datadog) | Left out |
| Metrics (StatsD, DogStatsD) | Left out |
| CPU and memory load generation | Left out |
| TLS on the fake's own listener | Left out: the mesh provides mTLS |
| Request and response body sizes, `HTTP_SERVER_*` timeouts, CORS | Left out |
