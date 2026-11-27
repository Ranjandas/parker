// e2eplan picks the e2e labs a change needs: changed paths on stdin (one
// per line, as `git diff --name-only` prints them), a JSON array of lab
// files on stdout for the CI matrix. "live" stands for the netsetup round
// trips and the rootless-mode e2e.
//
//	git diff --name-only BASE HEAD | go run ./hack/e2eplan
//	go run ./hack/e2eplan -all
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path"
	"slices"
	"strings"
)

// Live is the matrix entry for the tests outside hack/rootless-e2e.sh.
const Live = "live"

// smoke runs beside any area's own labs.
const smoke = "examples/minimal.yaml"

// rule maps paths with a prefix to the labs that exercise them. The first
// matching rule wins; paths no rule matches run everything.
type rule struct {
	prefix string
	labs   []string // nil: no e2e (unit tests cover it)
}

var (
	secure = []string{"examples/acl.yaml", "hack/e2e/mesh-secure.yaml", "hack/e2e/tproxy-secure.yaml",
		"hack/e2e/mesh-federation-acl.yaml", "hack/e2e/peering-secure.yaml",
		"hack/e2e/dataplane-secure.yaml"}
	fakeLabs = []string{"hack/e2e/fake-chain.yaml", "examples/mesh.yaml", "examples/clients.yaml"}
	rules    = []rule{
		{"internal/fake/", fakeLabs},
		{"internal/expand/fake.go", fakeLabs},
		{"internal/lab/fake.go", fakeLabs},
		{"cmd/parker/fake.go", fakeLabs},
		{"cmd/parker/call.go", fakeLabs},
		{"internal/daemon/fake.go", fakeLabs},
		{"internal/lab/loglevel.go", append(slices.Clone(fakeLabs), secure...)},
		{"cmd/parker/loglevel.go", append(slices.Clone(fakeLabs), secure...)},
		{"internal/daemon/appproxy.go", fakeLabs},
		{"internal/tproxy/", []string{"examples/tproxy.yaml", "hack/e2e/tproxy-secure.yaml"}},
		{"cmd/parker/sidecar.go", []string{"examples/tproxy.yaml", "hack/e2e/tproxy-secure.yaml"}},
		{"internal/expand/gateway.go", []string{"examples/gateways.yaml", "examples/terminating-failover.yaml", "examples/mesh-federation.yaml", "hack/e2e/mesh-federation-acl.yaml", "hack/e2e/mesh-federation-gateways.yaml"}},
		{"internal/expand/peering.go", []string{"examples/peering.yaml", "hack/e2e/peering-secure.yaml", "hack/e2e/peering-gateways-ha.yaml"}},
		{"internal/expand/external.go", []string{"examples/terminating-failover.yaml"}},
		{"internal/expand/vault.go", []string{"examples/vault.yaml"}},
		{"internal/spec/vault.go", []string{"examples/vault.yaml"}},
		{"cmd/parker/vaultinit.go", []string{"examples/vault.yaml"}},
		{"internal/expand/dataplane.go", []string{"examples/dataplane.yaml", "hack/e2e/dataplane-secure.yaml"}},
		{"cmd/parker/dataplanehealth.go", []string{"examples/dataplane.yaml", "hack/e2e/dataplane-secure.yaml", "examples/terminating-failover.yaml"}},
		{"cmd/parker/aclsetup.go", secure},
		{"cmd/parker/configentries.go", []string{"examples/config-entries.yaml", "examples/gateways.yaml"}},
		{"examples/entries/", []string{"examples/config-entries.yaml"}},
		{"internal/scenario/", []string{"examples/scenario.yaml", "examples/faults.yaml", "examples/upgrade.yaml"}},
		{"internal/lab/scenario.go", []string{"examples/scenario.yaml", "examples/faults.yaml", "examples/upgrade.yaml"}},
		{"cmd/parker/fault.go", []string{"examples/faults.yaml"}},
		{"hack/e2e/restart.yaml", []string{smoke}},
		{"hack/rootless-mode-e2e.sh", []string{Live}},
		{"internal/tui/", nil},
		{"cmd/parker/tui.go", nil},
		{"hack/e2eplan/", nil},
		{"hack/genschema/", nil},
		{"hack/sitegen/", nil},
		{"docs/", nil},
		{"site/", nil},
		{".github/workflows/pages.yml", nil},
		{".github/ISSUE_TEMPLATE/", nil},
		{"LICENSE", nil},
		{"CLAUDE.md", nil},
		{".gitignore", nil},
		{".goreleaser.yaml", nil},
	}
)

// Plan is the labs for changed paths, in suite order. all is the whole
// matrix (suite plus Live).
func Plan(changed, suite []string) []string {
	all := append(slices.Clone(suite), Live)
	want := map[string]bool{}
	for _, p := range changed {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		switch {
		case strings.HasSuffix(p, ".md"), strings.HasSuffix(p, "_test.go"):
			continue
		case slices.Contains(suite, p):
			want[p] = true
			continue
		}
		labs, ok := match(p)
		if !ok {
			return all
		}
		for _, l := range labs {
			want[l] = true
			if l != Live {
				want[smoke] = true
			}
		}
	}
	var out []string
	for _, l := range all {
		if want[l] {
			out = append(out, l)
		}
	}
	return out
}

func match(p string) ([]string, bool) {
	for _, r := range rules {
		if p == r.prefix || (strings.HasSuffix(r.prefix, "/") && strings.HasPrefix(p, r.prefix)) {
			return r.labs, true
		}
	}
	return nil, false
}

func readLines(r io.Reader) []string {
	var out []string
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		if l := strings.TrimSpace(sc.Text()); l != "" && !strings.HasPrefix(l, "#") {
			out = append(out, l)
		}
	}
	return out
}

func main() {
	all := flag.Bool("all", false, "plan the whole suite")
	suiteFile := flag.String("suite", path.Join("hack", "e2e", "suite.txt"), "the e2e suite, one lab file per line")
	flag.Parse()
	f, err := os.Open(*suiteFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	suite := readLines(f)
	f.Close()
	var labs []string
	if *all {
		labs = append(suite, Live)
	} else {
		labs = Plan(readLines(os.Stdin), suite)
	}
	if labs == nil {
		labs = []string{}
	}
	b, _ := json.Marshal(labs)
	fmt.Println(string(b))
}
