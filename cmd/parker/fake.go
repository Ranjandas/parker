package main

import (
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	parkerv1 "github.com/Ranjandas/parker/internal/api/parkerv1"
	"github.com/Ranjandas/parker/internal/scenario"
	"github.com/Ranjandas/parker/internal/spec"
)

// fakeFlags are parker fake's change flags.
type fakeFlags struct {
	service, latency, errorType, errorDelay string
	health, errorCode, rateLimitCode        int
	errorRate, rateLimit                    float64
	variance                                int
	reset                                   bool
}

// step turns the flags that were set into a fake: step on sel.
func (f *fakeFlags) step(cmd *cobra.Command, sel string) (*spec.Step, error) {
	set := cmd.Flags().Changed
	st := &spec.FakeStep{On: sel, Service: f.service, Reset: f.reset}
	if set("health") {
		st.Health = &spec.FakeHealth{Code: f.health}
	}
	if set("error-rate") || set("error-code") || set("error-type") || set("error-delay") {
		st.Errors = &spec.FakeErrors{Code: f.errorCode, Type: f.errorType, Delay: f.errorDelay}
		if set("error-rate") {
			r := f.errorRate
			st.Errors.Rate = &r
		}
	}
	if set("latency") || set("variance") {
		st.Latency = &spec.FakeLatency{Variance: f.variance}
		for _, kv := range strings.Split(f.latency, ",") {
			if kv = strings.TrimSpace(kv); kv == "" {
				continue
			}
			k, v, ok := strings.Cut(kv, "=")
			if !ok {
				// A bare duration is the median.
				k, v = "p50", kv
			}
			switch k {
			case "p50":
				st.Latency.P50 = v
			case "p90":
				st.Latency.P90 = v
			case "p99":
				st.Latency.P99 = v
			default:
				return nil, fmt.Errorf("--latency %s: want p50=, p90= or p99=", kv)
			}
		}
	}
	if set("rate-limit") || set("rate-limit-code") {
		st.RateLimit = &spec.FakeRateLimit{Code: f.rateLimitCode}
		if set("rate-limit") {
			r := f.rateLimit
			st.RateLimit.RPS = &r
		}
	}
	return &spec.Step{Fake: st}, nil
}

func fakeCmd() *cobra.Command {
	c, _ := newFakeCmd()
	return c
}

func newFakeCmd() (*cobra.Command, *fakeFlags) {
	f := &fakeFlags{}
	c := &cobra.Command{
		Use:   "fake FILE|LAB SELECTOR [flags]",
		Short: "Change a running fake service's faults (health, errors, latency, rate limit)",
		Long: "Changes the faults of the fake services (run: fake-service) on the nodes a\n" +
			"selector matches, while they run, as a scenario fake: step would. Only the\n" +
			"flags given change. With no flags it shows the faults in force.",
		Example: "  parker fake lab.yaml dc1/api --health 503        # Consul check fails, process stays up\n" +
			"  parker fake lab.yaml dc1/api --error-rate 0.3 --error-code 503\n" +
			"  parker fake lab.yaml dc1/api --latency p50=200ms,p99=2s\n" +
			"  parker fake lab.yaml dc1/api --reset\n" +
			"  parker fake lab.yaml dc1/api",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := labName(args[0])
			if err != nil {
				return err
			}
			st, err := f.step(cmd, args[1])
			if err != nil {
				return err
			}
			if err := scenario.Validate(&spec.Scenario{Steps: []*spec.Step{st}}); err != nil {
				return err
			}
			y, err := scenario.Inline(st)
			if err != nil {
				return err
			}
			ctx, stop := signalContext()
			defer stop()
			cl, err := ensureDaemon(ctx)
			if err != nil {
				return err
			}
			stream, err := cl.Run(ctx, connect.NewRequest(&parkerv1.RunRequest{Lab: name, Inline: y}))
			if err != nil {
				return rpcErr(err)
			}
			if err := printLines(stream); err != nil {
				cmd.SilenceUsage = true
				return err
			}
			return nil
		},
	}
	fl := c.Flags()
	fl.StringVar(&f.service, "service", "", "one fake service on the nodes (default: all)")
	fl.IntVar(&f.health, "health", 200, "code /health answers: 503 fails the Consul check")
	fl.Float64Var(&f.errorRate, "error-rate", 0, "share of requests that fail, 0 to 1 (0 stops errors)")
	fl.IntVar(&f.errorCode, "error-code", 0, "code a failed request answers")
	fl.StringVar(&f.errorType, "error-type", "", "http_error or delay")
	fl.StringVar(&f.errorDelay, "error-delay", "", "delay for --error-type delay, e.g. 5s")
	fl.StringVar(&f.latency, "latency", "", "latency percentiles: p50=200ms,p90=,p99=2s (a bare duration is p50; 0s clears)")
	fl.IntVar(&f.variance, "variance", 0, "latency spread, percent")
	fl.Float64Var(&f.rateLimit, "rate-limit", 0, "requests per second (0 turns it off)")
	fl.IntVar(&f.rateLimitCode, "rate-limit-code", 0, "code for requests over the rate limit")
	fl.BoolVar(&f.reset, "reset", false, "go back to the faults the lab started with")
	return c, f
}
