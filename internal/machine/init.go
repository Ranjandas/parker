package machine

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"
)

// InitOptions are `parker machine init`'s choices.
type InitOptions struct {
	CPUs      uint
	MemoryMiB uint64
	DiskGiB   uint64
	Mounts    []string // host directories to share at the same path
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
	c := Config{
		CPUs: o.CPUs, MemoryMiB: o.MemoryMiB, DiskGiB: o.DiskGiB, MAC: mac,
		Image: img.Name, Mounts: o.Mounts,
	}
	if err := m.SaveConfig(c); err != nil {
		return err
	}
	ok = true
	return nil
}

// VersionPath holds the release the VM should run; its install script
// installs it before the daemon starts when it differs from what it has.
func (m Machine) VersionPath() string { return m.path("share", "version") }

// SetDaemon says which parker the VM runs from its next boot: release
// version (vX.Y.Z or X.Y.Z), or, if binary is set, that Linux binary
// (a dev build; PARKER_MACHINE_BINARY).
func (m Machine) SetDaemon(version, binary string) error {
	if binary == "" {
		_ = os.Remove(m.BinaryPath())
		v := strings.TrimPrefix(version, "v")
		return os.WriteFile(m.VersionPath(), []byte(v+"\n"), 0o644)
	}
	_ = os.Remove(m.VersionPath())
	b, err := os.ReadFile(binary)
	if err != nil {
		return fmt.Errorf("parker binary for the VM: %w", err)
	}
	if len(b) < 4 || string(b[:4]) != "\x7fELF" {
		return fmt.Errorf("%s is not a Linux binary (build one with GOOS=linux)", binary)
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
