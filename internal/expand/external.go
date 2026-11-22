package expand

import (
	"errors"
	"fmt"
	"maps"
	"net/netip"
	"regexp"
	"slices"

	"github.com/Ranjandas/parker/internal/spec"
)

// ExternalRole labels the node an external service runs on.
const ExternalRole = "external"

// ExternalNodeMeta marks the catalog node of an external service, as
// consul-esm expects.
var ExternalNodeMeta = map[string]string{"external-node": "true", "external-probe": "true"}

var externalNameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// external is one service under consul.external.
type external struct {
	name string
	svc  *spec.Service       // merged with consul.defaults.service_template
	byDC map[string][]string // dc -> partitions it registers in ("" default)
	ips  map[string]string   // dc -> address on the dc's LAN
}

type externals []*external

// externalServices checks consul.external.
func externalServices(c *spec.Consul) (externals, error) {
	if len(c.External) == 0 {
		return nil, nil
	}
	var defaultSD *spec.Service
	if c.Defaults != nil && c.Defaults.ServiceTemplate != nil {
		d := *c.Defaults.ServiceTemplate
		// The mesh settings of the defaults don't apply outside it.
		d.Connect, d.Upstreams = "", nil
		defaultSD = &d
	}
	var out externals
	var errs []error
	for _, name := range slices.Sorted(maps.Keys(c.External)) {
		x := c.External[name]
		where := "consul.external." + name
		if !externalNameRe.MatchString(name) || len(name) > 48 {
			errs = append(errs, fmt.Errorf("%s: names are lowercase letters, digits and '-'", where))
			continue
		}
		if x == nil || x.Service == nil {
			errs = append(errs, fmt.Errorf("%s: service: required (at least a port)", where))
			continue
		}
		own := x.Service
		svc := mergeService(defaultSD, own)
		if svc.Port <= 0 {
			errs = append(errs, fmt.Errorf("%s.service.port: required", where))
		}
		if own.Connect != "" && own.Connect != "none" {
			errs = append(errs, fmt.Errorf("%s.service.connect: an external service is outside the mesh; reach it through a terminating gateway", where))
		}
		if len(own.Upstreams) > 0 {
			errs = append(errs, fmt.Errorf("%s.service.upstreams: an external service has no sidecar to bind them", where))
		}
		errs = append(errs, checkFake(where+".service", svc, own)...)
		if len(x.In) == 0 {
			errs = append(errs, fmt.Errorf("%s.in: list where to register it, e.g. [dc1] or [dc1/part-a]", where))
		}
		e := &external{name: name, svc: svc, byDC: map[string][]string{}, ips: map[string]string{}}
		seen := map[string]bool{}
		for _, in := range x.In {
			s, err := parseSide(c, in)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s.in: %w", where, err))
				continue
			}
			if seen[s.String()] {
				errs = append(errs, fmt.Errorf("%s.in: %s is listed twice", where, s))
				continue
			}
			seen[s.String()] = true
			dc := c.Datacenters[s.dc]
			for _, n := range dc.AllNodes() {
				if partitionName(n.Partition) == s.part && n.Name == name {
					errs = append(errs, fmt.Errorf("%s.in: %s already has a node named %s, and Consul node names are unique in a partition", where, s, name))
				}
			}
			for _, g := range dc.AllGateways() {
				if partitionName(g.Partition) == s.part && g.Name == name {
					errs = append(errs, fmt.Errorf("%s.in: %s already has a gateway named %s, and Consul node names are unique in a partition", where, s, name))
				}
			}
			e.byDC[s.dc] = append(e.byDC[s.dc], s.part)
		}
		out = append(out, e)
	}
	return out, errors.Join(errs...)
}

// place gives each external service that dcName registers an address on
// the dc's LAN, from the top of the subnet down (clients count up from
// .100 and must stay below), and returns their catalog registrations.
func (xs externals) place(dcName string, subnet netip.Prefix, clientOff int) ([]DataplaneEntry, error) {
	hosts := 1<<(32-subnet.Bits()) - 2
	base := subnet.Masked().Addr()
	var out []DataplaneEntry
	k := 0
	for _, x := range xs {
		parts, ok := x.byDC[dcName]
		if !ok {
			continue
		}
		off := hosts - k
		k++
		if off < clientOff {
			return nil, fmt.Errorf("consul.external.%s: no room left on %s's LAN (%s) after its nodes; use a larger subnet", x.name, dcName, subnet)
		}
		ip := addrAt(base, off)
		x.ips[dcName] = ip
		for _, part := range parts {
			svc := *x.svc
			if svc.Namespace == spec.DefaultNamespace {
				svc.Namespace = ""
			}
			out = append(out, dataplaneEntries(x.name, ip, dcName, part, ExternalNodeMeta, x.name, &svc, 0)...)
		}
	}
	return out, nil
}

// nodes adds the node each external service runs on: it joins the LAN of
// every datacenter that registers it.
func (xs externals) nodes(out *spec.File) error {
	for _, x := range xs {
		if _, clash := out.Nodes[x.name]; clash {
			return fmt.Errorf("consul.external.%s: a node is already named %s", x.name, x.name)
		}
		rn := &spec.RawNode{Networks: map[string]*spec.Attachment{}, Files: map[string]string{}, Processes: map[string]*spec.Process{}}
		for dc, ip := range x.ips {
			rn.Networks[dc+"-lan"] = &spec.Attachment{IP: ip}
		}
		_, proc := expandService(x.name, "", x.svc, spec.ConsulCheck{}, 0)
		if proc != nil {
			if proc.ReadyWhen != nil {
				proc.ReadyWhen.Consul = nil // no agent to ask
			}
			rn.Processes[x.name] = proc
		}
		var nss []string
		if ns := x.svc.Namespace; ns != "" && ns != spec.DefaultNamespace {
			nss = []string{ns}
		}
		rn.Labels = &spec.NodeLabels{Name: x.name, Role: ExternalRole, Group: x.name, Services: []string{x.name}, Namespaces: nss}
		out.Nodes[x.name] = rn
	}
	return nil
}
