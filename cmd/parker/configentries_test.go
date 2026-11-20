package main

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/Ranjandas/parker/internal/expand"
)

func TestApplyEntries(t *testing.T) {
	refs := []expand.EntryRef{{File: "a", Label: "entry a"}, {File: "b", Label: "entry b (line 9)"}, {File: "c", Label: "entry c"}}
	var order []string
	calls := map[string]int{}
	write := func(_ context.Context, r expand.EntryRef) (string, error) {
		f := r.File
		calls[f]++
		order = append(order, f)
		switch {
		case f == "a" && calls[f] == 1:
			return "Error writing config entry: Unexpected response code: 500 (No cluster leader)", errors.New("exit 1")
		case f == "b":
			return "Failed to decode config entry input:\n\t* invalid config key \"Bogus\"", errors.New("exit 1")
		}
		return "Config entry written", nil
	}
	err := applyEntries(context.Background(), refs, write, 5*time.Second, io.Discard)
	if err == nil || err.Error() != `entry b (line 9): Failed to decode config entry input: * invalid config key "Bogus"` {
		t.Fatalf("err %v", err)
	}
	if strings.Join(order, ",") != "a,a,b" {
		t.Errorf("order %v: transient errors retry, real ones stop at once", order)
	}
}

func TestApplyEntriesGivesUp(t *testing.T) {
	write := func(context.Context, expand.EntryRef) (string, error) {
		return "Unexpected response code: 500 (No cluster leader)", errors.New("exit 1")
	}
	err := applyEntries(context.Background(), []expand.EntryRef{{File: "a", Label: "entry a"}}, write, 0, io.Discard)
	if err == nil || !strings.HasPrefix(err.Error(), "entry a: ") {
		t.Errorf("err %v", err)
	}
}
