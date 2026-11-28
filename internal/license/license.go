// Package license finds the Enterprise licenses a lab needs, one per
// product (Consul, Vault), and hands each to its product's processes
// without it leaking: the client reads it, the daemon writes it to one
// 0600 file in the lab's run dir (removed by down), and the processes find
// it through <PRODUCT>_LICENSE_PATH. It never goes into the topology,
// expand output, state or logs.
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

// Licenses holds a lab's licenses by product ("consul", "vault").
type Licenses map[string][]byte

// Product is the product whose license a binary takes: vault for vault,
// consul for everything else (consul, consul-dataplane, envoy).
func Product(binary string) string {
	if binary == "vault" {
		return "vault"
	}
	return "consul"
}

// Name is the product's name for people: Consul, Vault.
func Name(product string) string {
	if product == "" {
		return ""
	}
	return strings.ToUpper(product[:1]) + product[1:]
}

// EnvPath is the variable a product reads its license file from, and
// Parker's fallback: CONSUL_LICENSE_PATH, VAULT_LICENSE_PATH.
func EnvPath(product string) string { return strings.ToUpper(product) + "_LICENSE_PATH" }

// Env is the variable holding the license itself: CONSUL_LICENSE, ...
func Env(product string) string { return strings.ToUpper(product) + "_LICENSE" }

// FileName is the product's license file in the lab's run dir.
func FileName(product string) string { return product + ".hclic" }

// Path is where a lab's license for product lives while it runs.
func Path(runDir, product string) string { return filepath.Join(runDir, FileName(product)) }

// IsEnterprise says whether a version is an Enterprise build ("1.22.7+ent").
func IsEnterprise(version string) bool {
	_, meta, ok := strings.Cut(version, "+")
	return ok && slices.Contains(strings.Split(meta, "."), "ent")
}

// Needed lists, by product, why the lab needs a license: the Enterprise
// versions it pins or runs (extra holds more, [binary, version] pairs such
// as upgrade targets), or a license it declares. An empty map means it
// needs none.
func Needed(f *spec.File, extra ...[2]string) map[string][]string {
	why := map[string][]string{}
	add := func(product, s string) {
		if !slices.Contains(why[product], s) {
			why[product] = append(why[product], s)
		}
	}
	for _, k := range slices.Sorted(maps.Keys(f.Versions)) {
		if IsEnterprise(f.Versions[k]) {
			add(Product(k), fmt.Sprintf("versions.%s is %s", k, f.Versions[k]))
		}
	}
	for _, n := range slices.Sorted(maps.Keys(f.Nodes)) {
		for _, p := range slices.Sorted(maps.Keys(f.Nodes[n].Processes)) {
			pr := f.Nodes[n].Processes[p]
			if IsEnterprise(pr.Version) {
				bin := ""
				if len(pr.Run) > 0 {
					bin = pr.Run[0]
				}
				add(Product(bin), fmt.Sprintf("nodes.%s.processes.%s.version is %s", n, p, pr.Version))
			}
		}
	}
	for _, pv := range extra {
		if IsEnterprise(pv[1]) {
			add(Product(pv[0]), fmt.Sprintf("the scenario runs %s %s", pv[0], pv[1]))
		}
	}
	for _, p := range spec.LicenseProducts {
		if f.License.For(p) != "" {
			where := "license." + p
			if p == "consul" && f.License.Consul == nil {
				where = "license.file"
			}
			add(p, where+" is set")
		}
	}
	return why
}

// ErrMissing is returned when a lab needs a license and none is set.
var ErrMissing = errors.New("no Enterprise license")

// Resolve reads product's license: the lab file's license entry, else the
// file at <PRODUCT>_LICENSE_PATH, else <PRODUCT>_LICENSE itself. getenv is
// os.Getenv in production.
func Resolve(f *spec.File, product string, getenv func(string) string) (lic []byte, from string, err error) {
	switch file := f.License.For(product); {
	case file != "":
		from = "license " + file
		lic, err = os.ReadFile(file)
	case getenv(EnvPath(product)) != "":
		from = EnvPath(product) + " " + getenv(EnvPath(product))
		lic, err = os.ReadFile(getenv(EnvPath(product)))
	case getenv(Env(product)) != "":
		from = Env(product)
		lic = []byte(getenv(Env(product)))
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

// For checks the lab and returns the licenses it needs, none when it
// needs none. The error says which license is missing, why it's needed
// and how to give it.
func For(f *spec.File, getenv func(string) string, extra ...[2]string) (Licenses, error) {
	need := Needed(f, extra...)
	var out Licenses
	for _, p := range spec.LicenseProducts {
		why := need[p]
		if len(why) == 0 {
			continue
		}
		lic, _, err := Resolve(f, p, getenv)
		if errors.Is(err, ErrMissing) {
			return nil, fmt.Errorf("%s needs a %s Enterprise license: set %s (a license file) or %s, or add license: {%s: {file: PATH}}", strings.Join(why, ", "), Name(p), EnvPath(p), Env(p), p)
		}
		if err != nil {
			return nil, err
		}
		if out == nil {
			out = Licenses{}
		}
		out[p] = lic
	}
	return out, nil
}

// Missing says which needed licenses ls lacks, as an error, or nil.
func Missing(f *spec.File, ls Licenses) error {
	need := Needed(f)
	for _, p := range spec.LicenseProducts {
		if why := need[p]; len(why) > 0 && len(ls[p]) == 0 {
			return fmt.Errorf("%s needs a %s Enterprise license, and none came with the request: set %s or %s where you run parker up", strings.Join(why, ", "), Name(p), EnvPath(p), Env(p))
		}
	}
	return nil
}

// Write puts the licenses in the lab's run dir, each readable by its
// owner only, and returns their paths by product.
func Write(runDir string, ls Licenses) (map[string]string, error) {
	paths := map[string]string{}
	for _, p := range slices.Sorted(maps.Keys(ls)) {
		if len(ls[p]) == 0 {
			continue
		}
		path := Path(runDir, p)
		if err := os.WriteFile(path, ls[p], 0o600); err != nil {
			return nil, err
		}
		if err := os.Chmod(path, 0o600); err != nil { // in case it was there with looser bits
			return nil, err
		}
		paths[p] = path
	}
	return paths, nil
}

// Remove deletes the lab's licenses; down calls it, as the run dir and its
// logs stay.
func Remove(runDir string) error {
	var errs []error
	for _, p := range spec.LicenseProducts {
		if err := os.Remove(Path(runDir, p)); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Redact replaces each license in s with [license], for anything that
// might echo one back (an agent's error, say).
func Redact(s string, ls Licenses) string {
	for _, lic := range ls {
		if len(lic) >= 16 {
			s = strings.ReplaceAll(s, string(lic), "[license]")
		}
	}
	return s
}
