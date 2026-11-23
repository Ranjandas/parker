//go:build darwin && cgo

package machine

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/Code-Hex/vz/v3"
)

// Supported reports whether this build can run a machine.
const Supported = true

// Unsupported says why this build can't run a machine: it can.
func Unsupported() error { return nil }

// Run boots the VM and blocks until it stops: the guest powers off, or ctx
// ends (then it asks the guest to shut down, and forces it after a while).
// The calling binary needs the com.apple.security.virtualization
// entitlement (see hack/machine/sign.sh).
func (m Machine) Run(ctx context.Context, log func(string, ...any)) error {
	c, err := m.LoadConfig()
	if err != nil {
		return err
	}
	cfg, err := m.vmConfig(c)
	if err != nil {
		return err
	}
	vm, err := vz.NewVirtualMachine(cfg)
	if err != nil {
		return err
	}
	if err := os.WriteFile(m.PIDPath(), []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644); err != nil {
		return err
	}
	defer os.Remove(m.PIDPath())
	if err := vm.Start(); err != nil {
		return fmt.Errorf("start VM: %w", err)
	}
	log("VM started (%d CPUs, %d MiB, mac %s)", c.CPUs, c.MemoryMiB, c.MAC)
	states := vm.StateChangedNotify()
	for {
		select {
		case s := <-states:
			log("VM state: %s", s)
			switch s {
			case vz.VirtualMachineStateStopped:
				return nil
			case vz.VirtualMachineStateError:
				return errors.New("the VM stopped with an error")
			}
		case <-ctx.Done():
			return m.shutdown(vm, states, log)
		}
	}
}

func (m Machine) shutdown(vm *vz.VirtualMachine, states <-chan vz.VirtualMachineState, log func(string, ...any)) error {
	if vm.CanRequestStop() {
		log("asking the guest to shut down")
		if _, err := vm.RequestStop(); err != nil {
			log("request stop: %v", err)
		}
		timeout := time.After(30 * time.Second)
	wait:
		for {
			select {
			case s := <-states:
				if s == vz.VirtualMachineStateStopped {
					return nil
				}
			case <-timeout:
				break wait
			}
		}
	}
	if vm.CanStop() {
		log("forcing the VM off")
		return vm.Stop()
	}
	return nil
}

func (m Machine) vmConfig(c Config) (*vz.VirtualMachineConfiguration, error) {
	var store *vz.EFIVariableStore
	var err error
	if _, statErr := os.Stat(m.EFIVarsPath()); statErr == nil {
		store, err = vz.NewEFIVariableStore(m.EFIVarsPath())
	} else {
		store, err = vz.NewEFIVariableStore(m.EFIVarsPath(), vz.WithCreatingEFIVariableStore())
	}
	if err != nil {
		return nil, fmt.Errorf("EFI variable store: %w", err)
	}
	boot, err := vz.NewEFIBootLoader(vz.WithEFIVariableStore(store))
	if err != nil {
		return nil, err
	}
	cfg, err := vz.NewVirtualMachineConfiguration(boot, c.CPUs, c.MemoryMiB<<20)
	if err != nil {
		return nil, err
	}

	// A stable machine identifier, like the MAC, keeps the guest stable.
	var mid *vz.GenericMachineIdentifier
	if b, err := os.ReadFile(m.MachineIDPath()); err == nil {
		mid, err = vz.NewGenericMachineIdentifierWithData(b)
		if err != nil {
			return nil, err
		}
	} else {
		mid, err = vz.NewGenericMachineIdentifier()
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(m.MachineIDPath(), mid.DataRepresentation(), 0o644); err != nil {
			return nil, err
		}
	}
	plat, err := vz.NewGenericPlatformConfiguration(vz.WithGenericMachineIdentifier(mid))
	if err != nil {
		return nil, err
	}
	cfg.SetPlatformVirtualMachineConfiguration(plat)

	// Disks: the root disk, then the cloud-init seed.
	var disks []vz.StorageDeviceConfiguration
	for _, d := range []struct {
		path string
		ro   bool
	}{{m.DiskPath(), false}, {m.SeedPath(), true}} {
		att, err := vz.NewDiskImageStorageDeviceAttachment(d.path, d.ro)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", d.path, err)
		}
		dev, err := vz.NewVirtioBlockDeviceConfiguration(att)
		if err != nil {
			return nil, err
		}
		disks = append(disks, dev)
	}
	cfg.SetStorageDevicesVirtualMachineConfiguration(disks)

	// NAT: the Mac reaches the VM's address directly.
	nat, err := vz.NewNATNetworkDeviceAttachment()
	if err != nil {
		return nil, err
	}
	nic, err := vz.NewVirtioNetworkDeviceConfiguration(nat)
	if err != nil {
		return nil, err
	}
	hw, err := net.ParseMAC(c.MAC)
	if err != nil {
		return nil, err
	}
	mac, err := vz.NewMACAddress(hw)
	if err != nil {
		return nil, err
	}
	nic.SetMACAddress(mac)
	cfg.SetNetworkDevicesVirtualMachineConfiguration([]*vz.VirtioNetworkDeviceConfiguration{nic})

	// Shares: the machine's own directory, and any --mount directories.
	shares := []vz.DirectorySharingDeviceConfiguration{}
	addShare := func(tag, dir string) error {
		sd, err := vz.NewSharedDirectory(dir, false)
		if err != nil {
			return fmt.Errorf("share %s: %w", dir, err)
		}
		single, err := vz.NewSingleDirectoryShare(sd)
		if err != nil {
			return err
		}
		fs, err := vz.NewVirtioFileSystemDeviceConfiguration(tag)
		if err != nil {
			return err
		}
		fs.SetDirectoryShare(single)
		shares = append(shares, fs)
		return nil
	}
	if err := addShare(shareTag, m.ShareDir()); err != nil {
		return nil, err
	}
	for i, dir := range c.Mounts {
		if err := addShare(MountTag(i), dir); err != nil {
			return nil, err
		}
	}
	cfg.SetDirectorySharingDevicesVirtualMachineConfiguration(shares)

	// Console to a file, for debugging boots.
	att, err := vz.NewFileSerialPortAttachment(m.ConsolePath(), false)
	if err != nil {
		return nil, err
	}
	console, err := vz.NewVirtioConsoleDeviceSerialPortConfiguration(att)
	if err != nil {
		return nil, err
	}
	cfg.SetSerialPortsVirtualMachineConfiguration([]*vz.VirtioConsoleDeviceSerialPortConfiguration{console})

	entropy, err := vz.NewVirtioEntropyDeviceConfiguration()
	if err != nil {
		return nil, err
	}
	cfg.SetEntropyDevicesVirtualMachineConfiguration([]*vz.VirtioEntropyDeviceConfiguration{entropy})
	balloon, err := vz.NewVirtioTraditionalMemoryBalloonDeviceConfiguration()
	if err != nil {
		return nil, err
	}
	cfg.SetMemoryBalloonDevicesVirtualMachineConfiguration([]vz.MemoryBalloonDeviceConfiguration{balloon})

	if ok, err := cfg.Validate(); !ok || err != nil {
		return nil, fmt.Errorf("VM configuration: %w", err)
	}
	return cfg, nil
}
