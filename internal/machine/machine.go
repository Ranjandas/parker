// Package machine runs a small Linux VM on macOS (Apple's Virtualization
// framework) with the Parker daemon inside, so `parker` on the Mac works
// without the user setting up a Linux host. Everything except booting the VM
// is plain Go and builds everywhere; the VM itself lives in vm_darwin.go.
package machine

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"
)

// Config is what `parker machine init` chose; saved as config.json.
type Config struct {
	CPUs      uint   `json:"cpus"`
	MemoryMiB uint64 `json:"memory_mib"`
	DiskGiB   uint64 `json:"disk_gib"`
	MAC       string `json:"mac"`
	Image     string `json:"image"`
	// Mounts are host directories shared into the VM at the same paths, for
	// labs that point at files on the Mac (certificates, local builds).
	// None by default: the VM sees only its own share.
	Mounts []string `json:"mounts,omitempty"`
}

// Ready is what the VM writes to the share once its daemon serves remote
// clients; `start` waits for it.
type Ready struct {
	IP      string `json:"ip"`
	BootID  string `json:"boot_id"`
	Version string `json:"version"`
	Time    string `json:"time"`
}

// Connection is how the host CLI reaches the machine's daemon.
type Connection struct {
	Host   string // https://IP:7443
	Token  string
	CAFile string
}

// DaemonPort is where the VM's daemon listens for the host.
const DaemonPort = 7443

// Machine is one VM's directory. Only one, "default", exists for now.
type Machine struct{ Dir string }

// Default is ~/.parker/machine/default (PARKER_MACHINE_DIR overrides).
func Default() (Machine, error) {
	if d := os.Getenv("PARKER_MACHINE_DIR"); d != "" {
		return Machine{Dir: d}, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return Machine{}, err
	}
	return Machine{Dir: filepath.Join(home, ".parker", "machine", "default")}, nil
}

func (m Machine) path(p ...string) string { return filepath.Join(append([]string{m.Dir}, p...)...) }

func (m Machine) ConfigPath() string  { return m.path("config.json") }
func (m Machine) DiskPath() string    { return m.path("disk.img") }
func (m Machine) SeedPath() string    { return m.path("seed.iso") }
func (m Machine) EFIVarsPath() string { return m.path("efi-vars.fd") }
func (m Machine) MachineIDPath() string {
	return m.path("machine-id")
}
func (m Machine) KeyPath() string     { return m.path("id_ed25519") }
func (m Machine) ConsolePath() string { return m.path("console.log") }
func (m Machine) LogPath() string     { return m.path("machine.log") }
func (m Machine) PIDPath() string     { return m.path("vm.pid") }

// ShareDir is shared into the VM (tag "parker", at /mnt/parker-machine): the
// host puts a parker binary there, the VM puts its address, token and CA.
func (m Machine) ShareDir() string   { return m.path("share") }
func (m Machine) ReadyPath() string  { return m.path("share", "ready.json") }
func (m Machine) CAPath() string     { return m.path("share", "ca.pem") }
func (m Machine) TokenPath() string  { return m.path("share", "token") }
func (m Machine) BinaryPath() string { return m.path("share", "bin", "parker") }
func (m Machine) Initialized() bool  { _, err := os.Stat(m.ConfigPath()); return err == nil }
func (m Machine) LoadConfig() (Config, error) {
	var c Config
	b, err := os.ReadFile(m.ConfigPath())
	if errors.Is(err, os.ErrNotExist) {
		return c, errors.New("no parker machine yet: run `parker machine init`")
	}
	if err != nil {
		return c, err
	}
	return c, json.Unmarshal(b, &c)
}

func (m Machine) SaveConfig(c Config) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(m.ConfigPath(), append(b, '\n'), 0o644)
}

// PID returns the process that owns the running VM, or 0.
func (m Machine) PID() int {
	b, err := os.ReadFile(m.PIDPath())
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return 0
	}
	p, err := os.FindProcess(pid)
	if err != nil || p.Signal(syscall.Signal(0)) != nil {
		return 0
	}
	return pid
}

func (m Machine) Running() bool { return m.PID() != 0 }

// ReadReady returns what the VM last reported, if anything.
func (m Machine) ReadReady() (Ready, bool) {
	var r Ready
	b, err := os.ReadFile(m.ReadyPath())
	if err != nil || json.Unmarshal(b, &r) != nil || net.ParseIP(r.IP) == nil {
		return r, false
	}
	return r, true
}

// Connection returns how to reach the daemon of a running machine.
func (m Machine) Connection() (Connection, bool) {
	if !m.Running() {
		return Connection{}, false
	}
	r, ok := m.ReadReady()
	if !ok {
		return Connection{}, false
	}
	tok, err := os.ReadFile(m.TokenPath())
	if err != nil {
		return Connection{}, false
	}
	return Connection{
		Host:   fmt.Sprintf("https://%s:%d", r.IP, DaemonPort),
		Token:  strings.TrimSpace(string(tok)),
		CAFile: m.CAPath(),
	}, true
}

// RandomMAC is a locally administered unicast address. A fixed MAC keeps
// the VM's DHCP lease, and so its IP and the daemon's certificate, stable.
func RandomMAC() (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[0] = (b[0] | 0x02) &^ 0x01
	return net.HardwareAddr(b).String(), nil
}

// NewKey writes an ed25519 SSH key pair and returns the public key line.
func (m Machine) NewKey() (string, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	blk, err := ssh.MarshalPrivateKey(priv, "parker-machine")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(m.KeyPath(), pem.EncodeToMemory(blk), 0o600); err != nil {
		return "", err
	}
	sp, err := ssh.NewPublicKey(pub)
	if err != nil {
		return "", err
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sp))) + " parker-machine"
	return line, os.WriteFile(m.KeyPath()+".pub", []byte(line+"\n"), 0o644)
}

// SSHArgs are the ssh(1) arguments for logging in as the VM's user.
func (m Machine) SSHArgs(ip string) []string {
	return []string{
		"-i", m.KeyPath(),
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		"-o", "IdentitiesOnly=yes",
		User + "@" + ip,
	}
}

// User is the login user cloud-init creates in the VM.
const User = "parker"

// WaitReady waits for a ready.json written after since, while alive says
// the VM process is still up.
func (m Machine) WaitReady(since time.Time, timeout time.Duration, alive func() bool, tick func()) (Ready, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fi, err := os.Stat(m.ReadyPath()); err == nil && fi.ModTime().After(since.Add(-time.Second)) {
			if r, ok := m.ReadReady(); ok {
				if _, err := os.Stat(m.TokenPath()); err == nil {
					return r, nil
				}
			}
		}
		if !alive() {
			return Ready{}, errors.New("the VM stopped while booting")
		}
		if tick != nil {
			tick()
		}
		time.Sleep(time.Second)
	}
	return Ready{}, fmt.Errorf("the VM's daemon was not ready within %s", timeout)
}
