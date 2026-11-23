package machine

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
)

// InitOptions are `parker machine init`'s choices.
type InitOptions struct {
	CPUs          uint
	MemoryMiB     uint64
	DiskGiB       uint64
	Mounts        []string // host directories to share at the same path
	ParkerBinary  string   // linux binary to run in the VM, "" = a release
	ParkerVersion string   // release to install when no binary; "" = latest
}

// Init creates the machine: disk from the pinned image, SSH key, cloud-init
// seed. It does not boot it.
func (m Machine) Init(o InitOptions, out io.Writer) error {
	if m.Initialized() {
		return fmt.Errorf("a parker machine already exists in %s (parker machine rm first)", m.Dir)
	}
	img, err := HostImage()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(m.ShareDir(), 0o700); err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.RemoveAll(m.Dir)
		}
	}()
	cached, err := Download(img, out)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "creating a %d GiB disk\n", o.DiskGiB)
	if err := ConvertDisk(cached, m.DiskPath(), o.DiskGiB); err != nil {
		return err
	}
	pub, err := m.NewKey()
	if err != nil {
		return err
	}
	id := make([]byte, 8)
	if _, err := rand.Read(id); err != nil {
		return err
	}
	if err := WriteSeed(m.SeedPath(), UserData(pub, o.Mounts), MetaData("parker-"+hex.EncodeToString(id))); err != nil {
		return err
	}
	mac, err := RandomMAC()
	if err != nil {
		return err
	}
	if o.ParkerVersion != "" && o.ParkerBinary == "" {
		if err := os.WriteFile(m.path("share", "version"), []byte(o.ParkerVersion+"\n"), 0o644); err != nil {
			return err
		}
	}
	c := Config{
		CPUs: o.CPUs, MemoryMiB: o.MemoryMiB, DiskGiB: o.DiskGiB, MAC: mac,
		Image: img.Name, Mounts: o.Mounts, ParkerBinary: o.ParkerBinary, ParkerVersion: o.ParkerVersion,
	}
	if err := m.SaveConfig(c); err != nil {
		return err
	}
	ok = true
	return nil
}

// CopyBinary refreshes the dev binary in the share, if the machine uses one.
func (m Machine) CopyBinary(c Config) error {
	if c.ParkerBinary == "" {
		return nil
	}
	b, err := os.ReadFile(c.ParkerBinary)
	if err != nil {
		return fmt.Errorf("parker binary for the VM: %w", err)
	}
	if len(b) < 4 || string(b[:4]) != "\x7fELF" {
		return fmt.Errorf("%s is not a Linux binary (build one with GOOS=linux)", c.ParkerBinary)
	}
	if err := os.MkdirAll(m.path("share", "bin"), 0o755); err != nil {
		return err
	}
	tmp := m.BinaryPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o755); err != nil {
		return err
	}
	return os.Rename(tmp, m.BinaryPath())
}
