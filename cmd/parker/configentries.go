package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Ranjandas/parker/internal/expand"
)

// configEntriesCmd runs on a dc's first server once the servers (and ACLs)
// are ready: it writes the dc's config entries in order with `consul config
// write`, then waits so the process stays up for status.
func configEntriesCmd() *cobra.Command {
	return &cobra.Command{
		Use:    expand.ConfigEntries + " MANIFEST",
		Short:  "Apply a datacenter's config_entries (run by parker up)",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			b, err := os.ReadFile(args[0])
			if err != nil {
				return err
			}
			var refs []expand.EntryRef
			if err := json.Unmarshal(b, &refs); err != nil {
				return fmt.Errorf("%s: %w", args[0], err)
			}
			ctx, stop := signalContext()
			defer stop()
			if err := applyEntries(ctx, refs, consulWrite, 30*time.Second, os.Stderr); err != nil {
				return err
			}
			fmt.Println(expand.ConfigEntriesReadyLine)
			<-ctx.Done()
			return nil
		},
	}
}

func consulWrite(ctx context.Context, r expand.EntryRef) (string, error) {
	args := []string{"config", "write"}
	if r.Partition != "" {
		args = append(args, "-partition", r.Partition)
	}
	out, err := exec.CommandContext(ctx, "consul", append(args, r.File)...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// transient errors are retried: the cluster is still settling.
var transient = regexp.MustCompile(`(?i)no cluster leader|connection refused|EOF|rpc error making call: (ACL not found|Permission denied)`)

func applyEntries(ctx context.Context, refs []expand.EntryRef, write func(context.Context, expand.EntryRef) (string, error), patience time.Duration, log io.Writer) error {
	for _, r := range refs {
		deadline := time.Now().Add(patience)
		for {
			out, err := write(ctx, r)
			if err == nil {
				fmt.Fprintf(log, "applied %s\n", r.Label)
				break
			}
			if !transient.MatchString(out) || time.Now().After(deadline) {
				return fmt.Errorf("%s: %s", r.Label, firstLine(out, err))
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Second):
			}
		}
	}
	return nil
}

func firstLine(out string, err error) string {
	if out == "" {
		return err.Error()
	}
	return strings.Join(strings.Fields(out), " ")
}
