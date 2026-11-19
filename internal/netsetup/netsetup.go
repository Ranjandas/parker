// Package netsetup wires the lab's network: one Linux bridge per network on
// the host and one named network namespace per node, joined by veth pairs.
//
// Everything Parker creates is labelled so `parker down` can find it with no
// state file: namespaces are named parker-<lab>-<node>, and host-side links
// carry the alias "parker:<lab>".
package netsetup

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"

	"github.com/Ranjandas/parker/internal/spec"
)

const netnsDir = "/run/netns"

func Label(lab string) string { return "parker:" + lab }

func NetnsName(lab, node string) string { return "parker-" + lab + "-" + node }

func NetnsPath(lab, node string) string { return filepath.Join(netnsDir, NetnsName(lab, node)) }

func short(parts ...string) string {
	h := sha256.Sum256([]byte(strings.Join(parts, "/")))
	return hex.EncodeToString(h[:])[:8]
}

func BridgeName(lab, network string) string { return "pk-" + short(lab, network) }

// Up creates bridges, namespaces and veths. Call Down first for a clean slate.
func Up(lab string, f *spec.File) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	host, err := netns.Get()
	if err != nil {
		return err
	}
	defer host.Close()

	bridges := map[string]*netlink.Bridge{}
	for _, name := range slices.Sorted(maps.Keys(f.Networks)) {
		n := f.Networks[name]
		br := &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: BridgeName(lab, name)}}
		if err := netlink.LinkAdd(br); err != nil {
			return fmt.Errorf("network %s: create bridge %s: %w", name, br.Name, err)
		}
		if err := netlink.LinkSetAlias(br, Label(lab)); err != nil {
			return err
		}
		_, ipnet, _ := net.ParseCIDR(n.Subnet)
		ones, _ := ipnet.Mask.Size()
		addr, err := netlink.ParseAddr(n.Gateway + "/" + strconv.Itoa(ones))
		if err != nil {
			return fmt.Errorf("network %s: gateway: %w", name, err)
		}
		if err := netlink.AddrAdd(br, addr); err != nil {
			return fmt.Errorf("network %s: address bridge: %w", name, err)
		}
		if err := netlink.LinkSetUp(br); err != nil {
			return err
		}
		bridges[name] = br
	}

	for _, node := range slices.Sorted(maps.Keys(f.Nodes)) {
		if err := upNode(lab, node, f, bridges, host); err != nil {
			return fmt.Errorf("node %s: %w", node, err)
		}
	}
	return nil
}

func upNode(lab, node string, f *spec.File, bridges map[string]*netlink.Bridge, host netns.NsHandle) error {
	// NewNamed switches this (locked) thread into the new namespace.
	ns, err := netns.NewNamed(NetnsName(lab, node))
	if err != nil {
		return fmt.Errorf("create netns: %w", err)
	}
	defer ns.Close()
	if err := netns.Set(host); err != nil {
		return err
	}

	in, err := netlink.NewHandleAt(ns)
	if err != nil {
		return err
	}
	defer in.Close()
	lo, err := in.LinkByName("lo")
	if err != nil {
		return err
	}
	if err := in.LinkSetUp(lo); err != nil {
		return err
	}

	for i, netName := range slices.Sorted(maps.Keys(f.Nodes[node].Networks)) {
		att := f.Nodes[node].Networks[netName]
		nw := f.Networks[netName]
		id := short(lab, node, netName)
		veth := &netlink.Veth{
			LinkAttrs: netlink.LinkAttrs{Name: "pk" + id + "h", MasterIndex: bridges[netName].Index},
			PeerName:  "pk" + id + "p",
		}
		if err := netlink.LinkAdd(veth); err != nil {
			return fmt.Errorf("veth on %s: %w", netName, err)
		}
		if err := netlink.LinkSetAlias(veth, Label(lab)); err != nil {
			return err
		}
		if err := netlink.LinkSetUp(veth); err != nil {
			return err
		}
		peer, err := netlink.LinkByName(veth.PeerName)
		if err != nil {
			return err
		}
		if err := netlink.LinkSetNsFd(peer, int(ns)); err != nil {
			return err
		}
		peer, err = in.LinkByName(veth.PeerName)
		if err != nil {
			return err
		}
		ifname := "eth" + strconv.Itoa(i)
		if err := in.LinkSetName(peer, ifname); err != nil {
			return err
		}
		_, ipnet, _ := net.ParseCIDR(nw.Subnet)
		ones, _ := ipnet.Mask.Size()
		addr, err := netlink.ParseAddr(att.IP + "/" + strconv.Itoa(ones))
		if err != nil {
			return fmt.Errorf("%s ip: %w", netName, err)
		}
		if err := in.AddrAdd(peer, addr); err != nil {
			return err
		}
		if err := in.LinkSetUp(peer); err != nil {
			return err
		}
		if i == 0 {
			gw := net.ParseIP(nw.Gateway)
			if err := in.RouteAdd(&netlink.Route{LinkIndex: peer.Attrs().Index, Gw: gw}); err != nil {
				return fmt.Errorf("default route: %w", err)
			}
		}
	}
	return nil
}

// Namespaces lists the lab's namespaces (just names) found on the host.
func Namespaces(lab string) ([]string, error) {
	ents, err := os.ReadDir(netnsDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	prefix := NetnsName(lab, "")
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), prefix) {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// Down kills every process inside the lab's namespaces, deletes the
// namespaces and deletes every link labelled for the lab. It is idempotent
// and works without any state file.
func Down(lab string, log func(string, ...any)) error {
	names, err := Namespaces(lab)
	if err != nil {
		return err
	}
	inodes := map[uint64]string{}
	for _, n := range names {
		var st syscall.Stat_t
		if err := syscall.Stat(filepath.Join(netnsDir, n), &st); err == nil {
			inodes[st.Ino] = n
		}
	}
	if pids := pidsIn(inodes); len(pids) > 0 {
		log("stopping %d processes", len(pids))
		signalAll(pids, syscall.SIGTERM)
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) && len(pidsIn(inodes)) > 0 {
			time.Sleep(200 * time.Millisecond)
		}
		if left := pidsIn(inodes); len(left) > 0 {
			log("killing %d processes that ignored SIGTERM", len(left))
			signalAll(left, syscall.SIGKILL)
			time.Sleep(300 * time.Millisecond)
		}
	}
	var errs []error
	for _, n := range names {
		if err := netns.DeleteNamed(n); err != nil {
			errs = append(errs, fmt.Errorf("delete netns %s: %w", n, err))
		}
	}
	if len(names) > 0 {
		log("deleted %d namespaces", len(names))
	}
	links, err := netlink.LinkList()
	if err != nil {
		return err
	}
	deleted := 0
	for _, l := range links {
		if l.Attrs().Alias == Label(lab) {
			if err := netlink.LinkDel(l); err != nil && !errors.Is(err, syscall.ENODEV) {
				errs = append(errs, fmt.Errorf("delete link %s: %w", l.Attrs().Name, err))
				continue
			}
			deleted++
		}
	}
	if deleted > 0 {
		log("deleted %d links", deleted)
	}
	return errors.Join(errs...)
}

// pidsIn returns the pids whose network namespace is one of inodes.
func pidsIn(inodes map[uint64]string) []int {
	if len(inodes) == 0 {
		return nil
	}
	ents, _ := os.ReadDir("/proc")
	var pids []int
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == os.Getpid() {
			continue
		}
		var st syscall.Stat_t
		if err := syscall.Stat(filepath.Join("/proc", e.Name(), "ns/net"), &st); err != nil {
			continue
		}
		if _, ok := inodes[st.Ino]; ok {
			pids = append(pids, pid)
		}
	}
	return pids
}

func signalAll(pids []int, sig syscall.Signal) {
	for _, p := range pids {
		_ = syscall.Kill(p, sig)
	}
}
