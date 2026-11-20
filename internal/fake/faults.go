package fake

import (
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Ranjandas/parker/internal/spec"
)

// HeaderFault asks for faults on one request at named hops:
// "api=error-rate:50,delay:2s;db=delay:1s". It is passed down the chain.
const HeaderFault = "X-Parker-Fault"

// Faults are the faults a fake service injects. They can change while it
// runs (see Server.SetFaults).
type Faults struct {
	// HealthCode is what /health answers.
	HealthCode int `json:"health_code"`
	// Latency percentiles and their spread in percent.
	P50      time.Duration `json:"p50,omitempty"`
	P90      time.Duration `json:"p90,omitempty"`
	P99      time.Duration `json:"p99,omitempty"`
	Variance int           `json:"variance,omitempty"`
	// ErrorRate is the share of requests that fail, 0 to 1.
	ErrorRate  float64       `json:"error_rate,omitempty"`
	ErrorCode  int           `json:"error_code"`
	ErrorType  string        `json:"error_type"`
	ErrorDelay time.Duration `json:"error_delay,omitempty"`
	// RateLimit is requests per second; 0 is off.
	RateLimit     float64 `json:"rate_limit,omitempty"`
	RateLimitCode int     `json:"rate_limit_code"`
}

// DefaultFaults is a healthy service with no faults.
func DefaultFaults() Faults {
	return Faults{HealthCode: 200, ErrorCode: 500, ErrorType: "http_error", RateLimitCode: 503}
}

func parseDur(field, v string) (time.Duration, error) {
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("%s: want a duration like 200ms, got %q", field, v)
	}
	return d, nil
}

func checkCode(field string, c int) error {
	if c != 0 && (c < 100 || c > 599) {
		return fmt.Errorf("%s: want an HTTP status code, got %d", field, c)
	}
	return nil
}

// Apply sets the fields a fake: block (or a runtime change in the same
// shape) names, leaving the rest.
func (f *Faults) Apply(sf *spec.Fake) error {
	if h := sf.Health; h != nil && h.Code != 0 {
		if err := checkCode("health.code", h.Code); err != nil {
			return err
		}
		f.HealthCode = h.Code
	}
	if l := sf.Latency; l != nil {
		for _, p := range []struct {
			name, v string
			dst     *time.Duration
		}{{"latency.p50", l.P50, &f.P50}, {"latency.p90", l.P90, &f.P90}, {"latency.p99", l.P99, &f.P99}} {
			if p.v == "" {
				continue
			}
			d, err := parseDur(p.name, p.v)
			if err != nil {
				return err
			}
			*p.dst = d
		}
		if l.Variance < 0 || l.Variance > 100 {
			return fmt.Errorf("latency.variance: want 0 to 100, got %d", l.Variance)
		}
		if l.Variance != 0 {
			f.Variance = l.Variance
		}
	}
	if e := sf.Errors; e != nil {
		if e.Rate != nil {
			if *e.Rate < 0 || *e.Rate > 1 {
				return fmt.Errorf("errors.rate: want 0 to 1 (0.2 is 20%%), got %v", *e.Rate)
			}
			f.ErrorRate = *e.Rate
		}
		if err := checkCode("errors.code", e.Code); err != nil {
			return err
		}
		if e.Code != 0 {
			f.ErrorCode = e.Code
		}
		switch e.Type {
		case "":
		case "http_error", "delay":
			f.ErrorType = e.Type
		default:
			return fmt.Errorf("errors.type: want http_error or delay, got %q", e.Type)
		}
		if e.Delay != "" {
			d, err := parseDur("errors.delay", e.Delay)
			if err != nil {
				return err
			}
			f.ErrorDelay = d
		}
	}
	if r := sf.RateLimit; r != nil {
		if r.RPS != nil {
			if *r.RPS < 0 {
				return fmt.Errorf("rate_limit.rps: want 0 or more, got %v", *r.RPS)
			}
			f.RateLimit = *r.RPS
		}
		if err := checkCode("rate_limit.code", r.Code); err != nil {
			return err
		}
		if r.Code != 0 {
			f.RateLimitCode = r.Code
		}
	}
	return nil
}

// faultsFromEnv reads fake-service's fault env vars.
func faultsFromEnv(f *Faults, getenv func(string) string) error {
	for k, dst := range map[string]*time.Duration{"TIMING_50_PERCENTILE": &f.P50, "TIMING_90_PERCENTILE": &f.P90,
		"TIMING_99_PERCENTILE": &f.P99, "ERROR_DELAY": &f.ErrorDelay} {
		if v := getenv(k); v != "" {
			d, err := parseDur(k, v)
			if err != nil {
				return err
			}
			*dst = d
		}
	}
	for k, dst := range map[string]*int{"TIMING_VARIANCE": &f.Variance, "ERROR_CODE": &f.ErrorCode,
		"RATE_LIMIT_CODE": &f.RateLimitCode, "HEALTH_CHECK_RESPONSE_CODE": &f.HealthCode} {
		if v := getenv(k); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				return fmt.Errorf("%s=%q: want a number", k, v)
			}
			if strings.HasSuffix(k, "CODE") {
				if err := checkCode(k, n); err != nil || n == 0 {
					return fmt.Errorf("%s=%q: want an HTTP status code", k, v)
				}
			}
			*dst = n
		}
	}
	for k, dst := range map[string]*float64{"ERROR_RATE": &f.ErrorRate, "RATE_LIMIT": &f.RateLimit} {
		if v := getenv(k); v != "" {
			n, err := strconv.ParseFloat(v, 64)
			if err != nil || n < 0 || (k == "ERROR_RATE" && n > 1) {
				return fmt.Errorf("%s=%q: want a number (ERROR_RATE from 0 to 1)", k, v)
			}
			*dst = n
		}
	}
	switch v := getenv("ERROR_TYPE"); v {
	case "":
	case "http_error", "delay":
		f.ErrorType = v
	default:
		return fmt.Errorf("ERROR_TYPE=%q: want http_error or delay", v)
	}
	return nil
}

// Validate checks a fake: block the way the service will read it.
func Validate(sf *spec.Fake) error {
	f := DefaultFaults()
	if err := f.Apply(sf); err != nil {
		return err
	}
	c := &Config{}
	return c.apply(sf)
}

// injector draws a request's faults from the current settings.
type injector struct {
	mu     sync.Mutex
	rng    *rand.Rand
	tokens float64
	last   time.Time
}

func newInjector(seed int64) *injector {
	if seed == 0 {
		seed = rand.Int64()
	}
	return &injector{rng: rand.New(rand.NewPCG(uint64(seed), uint64(seed)>>1|1))}
}

// allow takes a token from a bucket refilled at rps (burst: one second's
// worth, at least 1).
func (in *injector) allow(rps float64, now time.Time) bool {
	if rps <= 0 {
		return true
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	burst := max(1, rps)
	if in.last.IsZero() {
		in.tokens = burst
	} else {
		in.tokens = min(burst, in.tokens+now.Sub(in.last).Seconds()*rps)
	}
	in.last = now
	if in.tokens < 1 {
		return false
	}
	in.tokens--
	return true
}

func (in *injector) float() float64 {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.rng.Float64()
}

// latency draws a delay: p50 for half the requests, p90 up to the 90th,
// p99 beyond, each spread by Variance percent.
func (in *injector) latency(f Faults) time.Duration {
	p90, p99 := f.P90, f.P99
	if p90 == 0 {
		p90 = f.P50
	}
	if p99 == 0 {
		p99 = p90
	}
	r := in.float()
	d := f.P50
	switch {
	case r >= 0.9:
		d = p99
	case r >= 0.5:
		d = p90
	}
	if f.Variance > 0 && d > 0 {
		spread := (in.float()*2 - 1) * float64(f.Variance) / 100
		d = time.Duration(float64(d) * (1 + spread))
	}
	return d
}

// requestFaults are faults a caller asked for on one request.
type requestFaults struct {
	errorRate float64 // 0 to 1
	delay     time.Duration
}

// parseRequestFaults reads ?error-rate=50&delay=2s (for this hop) and
// the X-Parker-Fault header entry for name.
func parseRequestFaults(name string, q url.Values, h http.Header) (requestFaults, error) {
	var rf requestFaults
	set := func(k, v string) error {
		switch k {
		case "error-rate":
			n, err := strconv.ParseFloat(v, 64)
			if err != nil || n < 0 || n > 100 {
				return fmt.Errorf("error-rate=%q: want a percentage, 0 to 100", v)
			}
			rf.errorRate = n / 100
		case "delay":
			d, err := time.ParseDuration(v)
			if err != nil {
				// birdwatcher sends seconds as a plain number.
				secs, err2 := strconv.ParseFloat(v, 64)
				if err2 != nil || secs < 0 {
					return fmt.Errorf("delay=%q: want a duration like 2s", v)
				}
				d = time.Duration(secs * float64(time.Second))
			}
			rf.delay = d
		default:
			return fmt.Errorf("unknown fault %q (want error-rate or delay)", k)
		}
		return nil
	}
	for _, k := range []string{"error-rate", "delay"} {
		if v := q.Get(k); v != "" {
			if err := set(k, v); err != nil {
				return rf, err
			}
		}
	}
	for _, hop := range strings.Split(h.Get(HeaderFault), ";") {
		target, list, ok := strings.Cut(strings.TrimSpace(hop), "=")
		if !ok || strings.TrimSpace(target) != name {
			continue
		}
		for _, kv := range strings.Split(list, ",") {
			k, v, ok := strings.Cut(strings.TrimSpace(kv), ":")
			if !ok {
				return rf, fmt.Errorf("%s: %q: want key:value", HeaderFault, kv)
			}
			if err := set(strings.TrimSpace(k), strings.TrimSpace(v)); err != nil {
				return rf, err
			}
		}
	}
	return rf, nil
}
