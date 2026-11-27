package expand

import "github.com/Ranjandas/parker/internal/spec"

// ProductConsul is Consul's product name: its lab file key and the
// product label of its nodes.
const ProductConsul = "consul"

func init() { Register(consulProduct{}) }

// consulProduct expands the consul: section (expandConsul and the files
// next to it).
type consulProduct struct{}

func (consulProduct) Name() string { return ProductConsul }

func (consulProduct) In(in *spec.File) bool { return in.Consul != nil }

func (consulProduct) Expand(in, out *spec.File, facts Facts) error {
	wanSubnet, wanAuto := WANSubnet, true
	if w := in.Networks[WANNetwork]; w != nil && w.Subnet != "" {
		wanSubnet, wanAuto = w.Subnet, false
	}
	if err := expandConsul(in.Consul, wanSubnet, out, facts, in.ConsulStorage()); err != nil {
		return err
	}
	// A Vault server's client agent goes by the server's node name: the
	// Vault product puts the server on it.
	for dc, keys := range in.ConsulStorage() {
		for _, k := range keys {
			from := spec.NodeKey(dc, "", k)
			out.Nodes[k] = out.Nodes[from]
			delete(out.Nodes, from)
		}
	}
	if w := out.Networks[WANNetwork]; w != nil && wanAuto {
		w.Auto = true
	}
	return nil
}

// Wire: Consul reads nothing from other products yet.
func (consulProduct) Wire(_, _ *spec.File, _ Facts) error { return nil }
