// Package license finds the Consul Enterprise license a lab needs and
// hands it to the agents without it leaking: the client reads it, the
// daemon writes it to one 0600 file in the lab's run dir (removed by down),
// and agents find it through CONSUL_LICENSE_PATH. It never goes into the
// topology, expand output, state or logs.
package license

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Ranjandas/parker/internal/spec"
)

// FileName is the license's file in the lab's run dir.
const FileName = "consul.hclic"

// Path is where a lab's license lives while it runs.
func Path(runDir string) string { return filepath.Join(runDir, FileName) }

// IsEnterprise says whether a version is an Enterprise build ("1.22.7+ent").
func IsEnterprise(version string) bool {
	_, meta, ok := strings.Cut(version, "+")
	return ok && slices.Contains(strings.Split(meta, "."), "ent")
}

// Needed lists why the lab needs a license: the Enterprise versions it
// pins or runs (extra holds more, such as upgrade targets), or a license:
// it declares. None means it doesn't.
func Needed(f *spec.File, extra ...string) []string {
	var why []string
	add := func(s string) {
		if !slices.Contains(why, s) {
			why = append(why, s)
		}
	}
	for _, k := range slices.Sorted(maps.Keys(f.Versions)) {
		if IsEnterprise(f.Versions[k]) {
			add(fmt.Sprintf("versions.%s is %s", k, f.Versions[k]))
		}
	}
	for _, n := range slices.Sorted(maps.Keys(f.Nodes)) {
		for _, p := range slices.Sorted(maps.Keys(f.Nodes[n].Processes)) {
			if v := f.Nodes[n].Processes[p].Version; IsEnterprise(v) {
				add(fmt.Sprintf("nodes.%s.processes.%s.version is %s", n, p, v))
			}
		}
	}
	for _, v := range extra {
		if IsEnterprise(v) {
			add(fmt.Sprintf("the scenario runs %s", v))
		}
	}
	if f.License != nil {
		add("license: is set")
	}
	return why
}

// ErrMissing is returned when a lab needs a license and none is set.
var ErrMissing = errors.New("no Consul Enterprise license")

// Resolve reads the license: license.file, else the file at
// CONSUL_LICENSE_PATH, else CONSUL_LICENSE itself. getenv is os.Getenv in
// production.
func Resolve(f *spec.File, getenv func(string) string) (lic []byte, from string, err error) {
	switch {
	case f.License != nil && f.License.File != "":
		from = "license.file " + f.License.File
		lic, err = os.ReadFile(f.License.File)
	case getenv("CONSUL_LICENSE_PATH") != "":
		from = "CONSUL_LICENSE_PATH " + getenv("CONSUL_LICENSE_PATH")
		lic, err = os.ReadFile(getenv("CONSUL_LICENSE_PATH"))
	case getenv("CONSUL_LICENSE") != "":
		from = "CONSUL_LICENSE"
		lic = []byte(getenv("CONSUL_LICENSE"))
	default:
		return nil, "", ErrMissing
	}
	if err != nil {
		return nil, from, fmt.Errorf("%s: %w", from, err)
	}
	lic = bytes.TrimSpace(lic)
	if len(lic) == 0 {
		return nil, from, fmt.Errorf("%s: the license is empty", from)
	}
	return lic, from, nil
}

// For checks the lab and returns its license, or nil when it needs none.
// The error says why one is needed and how to give it.
func For(f *spec.File, getenv func(string) string, extra ...string) ([]byte, error) {
	why := Needed(f, extra...)
	if len(why) == 0 {
		return nil, nil
	}
	lic, _, err := Resolve(f, getenv)
	if errors.Is(err, ErrMissing) {
		return nil, fmt.Errorf("%s needs a Consul Enterprise license: set CONSUL_LICENSE_PATH (a license file) or CONSUL_LICENSE, or add license: {file: PATH}", strings.Join(why, ", "))
	}
	return lic, err
}

// Write puts the license in the lab's run dir, readable by its owner only.
func Write(runDir string, lic []byte) error {
	p := Path(runDir)
	if err := os.WriteFile(p, lic, 0o600); err != nil {
		return err
	}
	return os.Chmod(p, 0o600) // in case it was there with looser bits
}

// Remove deletes the lab's license; down calls it, as the run dir and its
// logs stay.
func Remove(runDir string) error {
	err := os.Remove(Path(runDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Redact replaces the license in s with [license], for anything that might
// echo it back (an agent's error, say).
func Redact(s string, lic []byte) string {
	if len(lic) < 16 {
		return s
	}
	return strings.ReplaceAll(s, string(lic), "[license]")
}
