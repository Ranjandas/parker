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
| `limits` | | none | Refuses requests over a size, as an app server does. See [request size limits](#request-size-limits). |
| `seed` | `RAND_SEED` | random | Makes error and latency draws repeatable. |
| `request_faults` | | `true` | Lets a caller ask for faults on one request (below). |
| `facts` | | `true` | Adds a space fact to each answer. `false` keeps shared output clean. |
| `log_level` | `LOG_LEVEL` | `info` | `debug` adds each request's headers and upstream calls; `info` logs every request; `warn` only failed or faulted ones; `error` only failed ones. Changes at runtime with a `log_level:` step or `parker logs --level`. |

The order on each request is: limits, then rate limit, then errors, then
latency, then the upstream calls.

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
- `limit` names the [limit](#request-size-limits) that refused the
  request, and `error` says by how much.
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

## Request size limits

App servers refuse requests that are too large before the app sees
them, and each has its own limits and codes. Tomcat answers 400 to
request headers over 8 KB, Nginx answers 413 to a body over 1 MB. Behind
a mesh this looks like one hop failing for no clear reason. `limits:`
makes a fake refuse requests the same way, so a lab can show it:

```yaml
parker: v1alpha1
name: header-limit
versions: { consul: 1.22.7, envoy: 1.35.12 }
consul:
  datacenters:
    dc1:
      servers: 1
      nodes:
        api:
          service:
            port: 9090
            run: fake-service
            connect: sidecar
            fake:
              limits:
                max_header_bytes: 8KB   # Tomcat's maxHttpHeaderSize
                codes: { header: 400 }  # Tomcat answers 400
```

```console
$ parker call header-limit.yaml dc1/api -H "Cookie: session=$(head -c 9000 /dev/zero | tr '\0' a)"
api  dc1-api · dc1  400  42µs  request headers too large: 9094 bytes > max_header_bytes 8KB
```

| Key | Default | Meaning |
| --- | --- | --- |
| `limits.max_header_bytes` | off | The request line and all headers together, as Tomcat's `maxHttpHeaderSize` and Node's `--max-http-header-size` count them. |
| `limits.max_header_line_bytes` | off | Any one header, `Name: value`, as Nginx's `large_client_header_buffers` and Apache's `LimitRequestFieldSize` count them. |
| `limits.max_header_count` | off | The number of headers, `Host` included (Tomcat's `maxHeaderCount`). |
| `limits.max_url_bytes` | off | The path and query. |
| `limits.max_body_bytes` | off | The request body (Nginx's `client_max_body_size`). With `protocol: grpc`, the request message, refused with `RESOURCE_EXHAUSTED`. |
| `limits.max_form_params` | off | Query and form body parameters together (Tomcat's `maxParameterCount`). |
| `limits.codes.header` | `431` | Code for header size and count. Tomcat, Nginx and Apache answer `400`; Node and Envoy `431`. |
| `limits.codes.url` | `414` | Code for the URL. |
| `limits.codes.body` | `413` | Code for the body. |
| `limits.codes.form` | `400` | Code for form parameters. |

Sizes are bytes, or a string such as `8KB` or `1MB` (a KB is 1024
bytes). `0` turns a limit off. Headers are counted the way they are sent
over HTTP/1.1, CRLFs included, so a limit means the same for every
client.

A refused request doesn't call the service's upstreams, and the fake
logs it at `warn` with the limit's name. `limits:` works with `protocol:
http` and `grpc`; a `tcp` fake has no requests to limit.

Some things to know:

- In the mesh the request passes the service's sidecar first, and Envoy
  has its own limits: 60 KB of headers and 100 headers by default, both
  answered `431`. A fake limit above those is never reached through the
  sidecar.
- A fake doesn't pass its caller's headers on to its upstreams, so a big
  header sent to the front of a chain doesn't reach the service behind
  it. Call the limited service directly, as above.

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
- fake: { on: dc1/api, limits: { max_header_bytes: 4KB } }
- fake: { on: dc1/api, limits: { max_header_bytes: 0 } }   # off again
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
**Open**. The page shows:

- **Stats** for the requests this page has sent, and **Answered by**, the
  split between the places that answered, such as
  `api dc2 64% · api dc3 36%`.
- **Send request**, and **Loop**, which keeps sending them.
- **Break an upstream for requests from this page**: make a service
  further down the chain fail or slow down for this page's requests
  only, through the [`X-Parker-Fault` header](#faults-on-one-request).
  **Copy as parker call** copies the same request as a
  [`parker call`](getting-in.md#calling-services) command.
- **The last answer**: a row per hop with its node, address and time,
  and why a hop failed. Every hop logs the request id as `id=<id>`, so
  you can find the request in each service's logs.
- **Space fact**: the fact from the deepest hop that sent one.
- **Fault injection: web**: the health check, error rate, latency and
  rate limit, applied live to every caller of web.

In a service mesh, a hop is called through its sidecar, so its "from"
address is the sidecar's (`127.0.0.1:<port>`), not the caller's.

The fault panel writes through the daemon to the control socket. Opened
on the service port directly, the page works but the panel is read only,
so nothing in the mesh can change another service's faults.

### The web UI

On a node page (**Nodes & logs**), each fake service gets two cards.

**Fake service** shows what the service calls, and lets you send a
request through it:

- **Send request through web** sends a request to web, which calls its
  upstreams, and **Last call** draws the answer as a waterfall.
- **+ Add a fault to this request** makes services further down the
  chain fail or slow down for that request only, through the
  [`X-Parker-Fault` header](#faults-on-one-request). Their own fault
  settings don't change.
- A service with no upstreams links to the nodes that call it instead.

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

Limits: the list of services to break comes from the lab's topology, so it includes a fake that sets
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
