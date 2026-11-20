// Package compat knows which Envoy versions each Consul release supports,
// so a lab with a pair Consul would refuse (or misbehave with) gets a
// warning before anything starts.
package compat

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/Ranjandas/parker/internal/spec"
)

// envoyByConsul maps a Consul minor to the Envoy minors it supports: the
// union over its patch releases of envoyextensions/xdscommon/ENVOY_VERSIONS
// (proxysupport.go before 1.19) in hashicorp/consul.
var envoyByConsul = map[string][]string{
	"1.16": {"1.23", "1.24", "1.25", "1.26"},
	"1.17": {"1.24", "1.25", "1.26", "1.27"},
	"1.18": {"1.25", "1.26", "1.27", "1.28"},
	"1.19": {"1.26", "1.27", "1.28", "1.29"},
	"1.20": {"1.28", "1.29", "1.30", "1.31", "1.32", "1.33"},
	"1.21": {"1.28", "1.29", "1.30", "1.31", "1.32", "1.33", "1.34"},
	"1.22": {"1.32", "1.33", "1.34", "1.35"},
	"2.0":  {"1.34", "1.35", "1.36", "1.37", "1.38"},
	"2.1":  {"1.36", "1.37", "1.38", "1.39"},
}

// minor is "1.22" for "1.22.7", "1.22.7+ent" or "v1.22.7"; "" when it
// isn't a version (e.g. path:/my/build).
func minor(v string) string {
	v = strings.TrimPrefix(v, "v")
	if i := strings.IndexAny(v, "+-"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) < 2 {
		return ""
	}
	for _, p := range parts[:2] {
		if _, err := strconv.Atoi(p); err != nil {
			return ""
		}
	}
	return parts[0] + "." + parts[1]
}

// AtLeast reports whether version v is minor min ("1.14") or later; ok is
// false when v isn't a version (a path: build), which callers let pass.
func AtLeast(v, min string) (yes, ok bool) {
	m := minor(v)
	if m == "" {
		return false, false
	}
	num := func(s string) (int, int) {
		a, b, _ := strings.Cut(s, ".")
		x, _ := strconv.Atoi(a)
		y, _ := strconv.Atoi(b)
		return x, y
	}
	ma, mb := num(m)
	na, nb := num(min)
	return ma > na || ma == na && mb >= nb, true
}

// EnvoyFor lists the Envoy minors a Consul version supports, newest first;
// ok is false when Parker doesn't know that Consul release.
func EnvoyFor(consul string) (envoy []string, ok bool) {
	e, ok := envoyByConsul[minor(consul)]
	if !ok {
		return nil, false
	}
	out := slices.Clone(e)
	slices.Reverse(out)
	return out, true
}

// CheckEnvoy is nil when consul supports envoy, or when either isn't a
// release Parker can judge (a path: build, a Consul newer than this table).
func CheckEnvoy(consul, envoy string) error {
	em := minor(envoy)
	if em == "" {
		return nil
	}
	sup, ok := EnvoyFor(consul)
	if !ok || slices.Contains(sup, em) {
		return nil
	}
	return fmt.Errorf("envoy %s is not supported by consul %s, which supports envoy %s",
		envoy, consul, strings.Join(sup, ", "))
}

// Warnings checks a topology's Consul and Envoy pins: versions:, and the
// per-process overrides against the lab's other pin.
func Warnings(f *spec.File) []string {
	consul, envoy := f.Versions["consul"], f.Versions["envoy"]
	seen := map[string]bool{}
	var out []string
	check := func(c, e string) {
		if c == "" || e == "" {
			return
		}
		if err := CheckEnvoy(c, e); err != nil && !seen[err.Error()] {
			seen[err.Error()] = true
			out = append(out, err.Error())
		}
	}
	check(consul, envoy)
	for _, n := range slices.Sorted(maps.Keys(f.Nodes)) {
		ps := f.Nodes[n].Processes
		nodeConsul, nodeEnvoy := consul, envoy
		for _, pn := range slices.Sorted(maps.Keys(ps)) {
			p := ps[pn]
			if len(p.Run) == 0 || p.Version == "" {
				continue
			}
			switch p.Run[0] {
			case "consul":
				nodeConsul = p.Version
			case "envoy":
				nodeEnvoy = p.Version
			}
		}
		check(nodeConsul, nodeEnvoy)
	}
	return out
}
