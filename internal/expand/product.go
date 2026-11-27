package expand

import (
	"fmt"
	"slices"

	"github.com/Ranjandas/parker/internal/spec"
)

// Product is one HashiCorp product Parker can lab (Consul now; Vault and
// Boundary are planned, see #186). Each product owns a section of the lab
// file and turns it into low-level networks and nodes; everything that
// runs a lab only ever sees those.
//
// Expansion has two passes so products can refer to each other by name
// (a Consul dc taking its Connect CA from a Vault cluster, a Vault cluster
// storing its data in a Consul dc) without either living inside the other:
//
//  1. Expand: every product in the file adds its own networks and nodes
//     to out and publishes what others may need about it in Facts.
//  2. Wire: every product reads the others' Facts and adjusts its own
//     nodes (a token, an address, a dependency on another node's process).
type Product interface {
	// Name is the product's lab file key and versions: key, e.g. consul.
	Name() string
	// In reports whether the file has this product's section.
	In(in *spec.File) bool
	// Expand adds the product's networks and nodes to out.
	Expand(in *spec.File, out *spec.File, facts Facts) error
	// Wire runs once every product has expanded.
	Wire(in *spec.File, out *spec.File, facts Facts) error
}

// Facts are what each product publishes about itself for the others,
// keyed by product name. A product's facts are its own type; the products
// that read them know it.
type Facts map[string]any

// products are the known products, in expansion order.
var products []Product

// Register adds a product. A product package calls it from init.
func Register(p Product) {
	if slices.ContainsFunc(products, func(q Product) bool { return q.Name() == p.Name() }) {
		panic(fmt.Sprintf("expand: product %s registered twice", p.Name()))
	}
	products = append(products, p)
}

// Products lists the registered products' names, in expansion order.
func Products() []string {
	names := make([]string, len(products))
	for i, p := range products {
		names[i] = p.Name()
	}
	return names
}

func expandProducts(in, out *spec.File) error {
	facts := Facts{}
	var present []Product
	for _, p := range products {
		if p.In(in) {
			present = append(present, p)
		}
	}
	for _, p := range present {
		if err := p.Expand(in, out, facts); err != nil {
			return err
		}
	}
	for _, p := range present {
		if err := p.Wire(in, out, facts); err != nil {
			return err
		}
	}
	return nil
}
