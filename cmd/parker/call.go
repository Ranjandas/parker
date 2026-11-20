package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	parkerv1 "github.com/Ranjandas/parker/internal/api/parkerv1"
	"github.com/Ranjandas/parker/internal/api/parkerv1/parkerv1connect"
	"github.com/Ranjandas/parker/internal/fake"
)

type callFlags struct {
	service string
	headers []string
	json    bool
	fact    bool
	every   time.Duration
	count   int
}

func callCmd() *cobra.Command {
	f := &callFlags{}
	c := &cobra.Command{
		Use:   "call FILE|LAB NODE [PATH]",
		Short: "Send a request to a fake service and show the chain it went through",
		Long: "Sends one request to the fake service (run: fake-service) on NODE, from inside\n" +
			"the node as a client there would, and prints the chain of upstream calls as a\n" +
			"tree: each hop's node, dc, version, code and time. It runs through the daemon,\n" +
			"so it works against a remote host too. --every repeats it and shows the traffic\n" +
			"split (which dc and version answered), as during a failover or a canary.",
		Example: "  parker call lab.yaml dc1/web\n" +
			"  parker call lab.yaml dc1/web /orders -H 'X-Parker-Fault: api=error-rate:100'\n" +
			"  parker call lab.yaml dc1/web --every 1s\n" +
			"  parker call lab.yaml dc1/web --json",
		Args: cobra.RangeArgs(2, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := labName(args[0])
			if err != nil {
				return err
			}
			req := &parkerv1.FakeRequest{Lab: name, Selector: args[1], Service: f.service, Action: "call", Headers: map[string]string{}}
			if len(args) == 3 {
				req.Path = args[2]
			}
			for _, h := range f.headers {
				k, v, ok := strings.Cut(h, ":")
				if !ok {
					return fmt.Errorf("-H %q: want 'Name: value'", h)
				}
				req.Headers[strings.TrimSpace(k)] = strings.TrimSpace(v)
			}
			ctx, stop := signalContext()
			defer stop()
			cl, err := ensureDaemon(ctx)
			if err != nil {
				return err
			}
			cmd.SilenceUsage = true
			if f.every > 0 {
				return callLoop(ctx, cl, req, f, os.Stdout)
			}
			return callOnce(ctx, cl, req, f, os.Stdout)
		},
	}
	fl := c.Flags()
	fl.StringVar(&f.service, "service", "", "the fake service to call when the node runs several")
	fl.StringArrayVarP(&f.headers, "header", "H", nil, "add a request header, 'Name: value' (repeatable)")
	fl.BoolVar(&f.json, "json", false, "print the raw JSON answer")
	fl.BoolVar(&f.fact, "fact", false, "show each hop's space fact")
	fl.DurationVar(&f.every, "every", 0, "send a request every interval, e.g. 1s, until interrupted")
	fl.IntVar(&f.count, "count", 0, "with --every, stop after this many requests")
	return c
}

func doCall(ctx context.Context, cl parkerv1connect.ParkerServiceClient, req *parkerv1.FakeRequest) (*parkerv1.FakeResponse, *fake.Response, error) {
	res, err := cl.Fake(ctx, connect.NewRequest(req))
	if err != nil {
		return nil, nil, rpcErr(err)
	}
	var r fake.Response
	if json.Unmarshal([]byte(res.Msg.CallBody), &r) != nil || r.Name == "" {
		return res.Msg, nil, nil
	}
	return res.Msg, &r, nil
}

func callOnce(ctx context.Context, cl parkerv1connect.ParkerServiceClient, req *parkerv1.FakeRequest, f *callFlags, w io.Writer) error {
	msg, r, err := doCall(ctx, cl, req)
	if err != nil {
		return err
	}
	if f.json || r == nil {
		fmt.Fprintln(w, strings.TrimRight(msg.CallBody, "\n"))
	} else {
		for _, l := range fake.Tree(r, f.fact) {
			fmt.Fprintln(w, l)
		}
	}
	if msg.CallStatus >= 400 {
		return fmt.Errorf("answered %d", msg.CallStatus)
	}
	return nil
}

// callLoop sends a request every f.every, a line each, with the split so
// far every 10 requests and at the end.
func callLoop(ctx context.Context, cl parkerv1connect.ParkerServiceClient, req *parkerv1.FakeRequest, f *callFlags, w io.Writer) error {
	var split fake.Split
	failed := 0
	report := func() {
		fmt.Fprintf(w, "--- %d requests, %d failed\n", split.Total+failed, failed+split.Failed())
		for _, l := range split.Lines() {
			fmt.Fprintln(w, "    "+l)
		}
	}
	t := time.NewTicker(f.every)
	defer t.Stop()
	for i := 1; ; i++ {
		ts := time.Now().Format("15:04:05")
		_, r, err := doCall(ctx, cl, req)
		switch {
		case ctx.Err() != nil:
			report()
			return nil
		case err != nil:
			failed++
			fmt.Fprintf(w, "%s  %v\n", ts, err)
		case r == nil:
			failed++
			fmt.Fprintf(w, "%s  not a fake service's answer\n", ts)
		default:
			split.Add(r)
			line := ts + "  " + fake.Oneline(r)
			if f.fact && r.Fact != "" {
				line += "  ✦ " + r.Fact
			}
			fmt.Fprintln(w, line)
		}
		if f.count > 0 && i >= f.count {
			report()
			return nil
		}
		if i%10 == 0 {
			report()
		}
		select {
		case <-ctx.Done():
			report()
			return nil
		case <-t.C:
		}
	}
}
