// Package fake is Parker's built-in fake service (`run: fake-service`): an
// HTTP server that answers with who it is and, on each request, calls its
// upstreams and nests their answers with the time each hop took. It reads
// nicholasjackson/fake-service's env vars, so habits carry over, and the
// lab's `fake:` block (PARKER_FAKE, JSON), which wins.
package fake

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Ranjandas/parker/internal/spec"
)

// EnvConfig is the env var Parker passes the service's fake: block in.
const EnvConfig = "PARKER_FAKE" // expand.FakeEnv

// Config is a fake service's settings.
type Config struct {
	Name    string
	DC      string
	Node    string
	Port    int
	Message string
	Version string
	// Upstreams are URIs called on each request.
	Upstreams []string
	// Workers is how many upstreams are called at once.
	Workers int
	// IgnoreUpstreamErrors keeps the answer 200 when an upstream fails.
	IgnoreUpstreamErrors bool
	// ClientTimeout bounds each upstream call.
	ClientTimeout time.Duration
	// Protocol is http (default), grpc (HTTP plus gRPC on the same port)
	// or tcp (a line echo server).
	Protocol string
	// Upstream shapes the requests to the upstreams.
	Upstream UpstreamRequest
	// Faults are the faults injected at start (they can change later).
	Faults Faults
	// Seed for the random draws; 0 is a new one each start.
	Seed int64
	// RequestFaults lets callers ask for faults on one request.
	RequestFaults bool
	// Facts adds a space fact to each answer.
	Facts bool
	// ReadyDelay is how long after start /ready keeps failing.
	ReadyDelay time.Duration
	// ReadyCode and ReadyFailureCode are /ready's answers after and
	// before ReadyDelay.
	ReadyCode, ReadyFailureCode int
	// LogLevel is the level the service starts at (LOG_LEVEL).
	LogLevel Level
}

// Load builds the config from fake-service's env vars, then the fake:
// block in PARKER_FAKE on top. getenv is os.Getenv outside tests.
func Load(name, dc string, port int, getenv func(string) string) (*Config, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	c := &Config{Name: name, DC: dc, Port: port, Node: getenv("PARKER_NODE"), Workers: 1, ClientTimeout: 30 * time.Second,
		LogLevel: LevelInfo, Protocol: "http", Faults: DefaultFaults(), RequestFaults: true, Facts: true, ReadyCode: 200, ReadyFailureCode: 503}
	if v := getenv("NAME"); v != "" && name == "" {
		c.Name = v
	}
	c.Message = getenv("MESSAGE")
	c.Version = getenv("VERSION")
	for _, u := range strings.Split(getenv("UPSTREAM_URIS"), ",") {
		if u = strings.TrimSpace(u); u != "" {
			c.Upstreams = append(c.Upstreams, u)
		}
	}
	if v := getenv("UPSTREAM_WORKERS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return nil, fmt.Errorf("UPSTREAM_WORKERS=%q: want a number from 1", v)
		}
		c.Workers = n
	}
	if v := getenv("HTTP_CLIENT_REQUEST_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("HTTP_CLIENT_REQUEST_TIMEOUT=%q: %w", v, err)
		}
		c.ClientTimeout = d
	}
	if err := faultsFromEnv(&c.Faults, getenv); err != nil {
		return nil, err
	}
	if v := getenv("RAND_SEED"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("RAND_SEED=%q: want a number", v)
		}
		c.Seed = n
	}
	for k, dst := range map[string]*int{"READY_CHECK_RESPONSE_SUCCESS_CODE": &c.ReadyCode, "READY_CHECK_RESPONSE_FAILURE_CODE": &c.ReadyFailureCode} {
		if v := getenv(k); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 100 || n > 599 {
				return nil, fmt.Errorf("%s=%q: want an HTTP status code", k, v)
			}
			*dst = n
		}
	}
	if v := getenv("READY_CHECK_RESPONSE_DELAY"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("READY_CHECK_RESPONSE_DELAY=%q: %w", v, err)
		}
		c.ReadyDelay = d
	}
	if v := getenv("LOG_LEVEL"); v != "" {
		l, err := ParseLevel(v)
		if err != nil {
			return nil, fmt.Errorf("LOG_LEVEL: %w", err)
		}
		c.LogLevel = l
	}
	if raw := getenv(EnvConfig); raw != "" {
		var f spec.Fake
		if err := json.Unmarshal([]byte(raw), &f); err != nil {
			return nil, fmt.Errorf("%s: %w", EnvConfig, err)
		}
		if err := c.apply(&f); err != nil {
			return nil, err
		}
		if err := c.Faults.Apply(&f); err != nil {
			return nil, err
		}
	}
	return c, nil
}

func (c *Config) apply(f *spec.Fake) error {
	if f.Message != "" {
		c.Message = f.Message
	}
	if f.Version != "" {
		c.Version = f.Version
	}
	if len(f.Upstreams) > 0 {
		c.Upstreams = f.Upstreams
	}
	if f.Workers > 0 {
		c.Workers = f.Workers
	}
	switch f.Protocol {
	case "":
	case "http", "grpc", "tcp":
		c.Protocol = f.Protocol
	default:
		return fmt.Errorf("protocol: want http, grpc or tcp, got %q", f.Protocol)
	}
	if f.Seed != 0 {
		c.Seed = f.Seed
	}
	if f.LogLevel != "" {
		l, err := ParseLevel(f.LogLevel)
		if err != nil {
			return err
		}
		c.LogLevel = l
	}
	if f.RequestFaults != nil {
		c.RequestFaults = *f.RequestFaults
	}
	if f.Facts != nil {
		c.Facts = *f.Facts
	}
	if r := f.Ready; r != nil {
		if r.Delay != "" {
			d, err := time.ParseDuration(r.Delay)
			if err != nil {
				return fmt.Errorf("ready.delay: %w", err)
			}
			c.ReadyDelay = d
		}
		if r.Code != 0 {
			c.ReadyCode = r.Code
		}
		if r.FailureCode != 0 {
			c.ReadyFailureCode = r.FailureCode
		}
	}
	if err := c.Upstream.apply(f); err != nil {
		return err
	}
	switch f.UpstreamFailure {
	case "", "fail":
	case "ignore":
		c.IgnoreUpstreamErrors = true
	default:
		return fmt.Errorf("upstream_failure: want fail or ignore, got %q", f.UpstreamFailure)
	}
	return nil
}
