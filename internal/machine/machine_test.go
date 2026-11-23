package machine

import (
	"bytes"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/goccy/go-yaml"
	"github.com/kdomanski/iso9660"
	"golang.org/x/crypto/ssh"
)

func TestUserDataIsCloudConfig(t *testing.T) {
	ud := UserData("ssh-ed25519 AAAA parker-machine", []string{"/Users/me/labs"})
	if !strings.HasPrefix(ud, "#cloud-config\n") {
		t.Fatal("user-data must start with #cloud-config")
	}
	var doc struct {
		Users []struct {
			Name string   `yaml:"name"`
			Keys []string `yaml:"ssh_authorized_keys"`
		} `yaml:"users"`
		Mounts     [][]string `yaml:"mounts"`
		WriteFiles []struct {
			Path    string `yaml:"path"`
			Content string `yaml:"content"`
		} `yaml:"write_files"`
		RunCmd [][]string `yaml:"runcmd"`
	}
	if err := yaml.Unmarshal([]byte(ud), &doc); err != nil {
		t.Fatalf("user-data is not YAML: %v\n%s", err, ud)
	}
	if len(doc.Users) != 1 || doc.Users[0].Name != User || doc.Users[0].Keys[0] != "ssh-ed25519 AAAA parker-machine" {
		t.Errorf("users: %+v", doc.Users)
	}
	if len(doc.Mounts) != 2 || doc.Mounts[0][0] != shareTag || doc.Mounts[1][0] != "mount0" || doc.Mounts[1][1] != "/Users/me/labs" || doc.Mounts[1][2] != "virtiofs" {
		t.Errorf("mounts: %v", doc.Mounts)
	}
	files := map[string]string{}
	for _, f := range doc.WriteFiles {
		files[f.Path] = f.Content
	}
	if files["/etc/systemd/system/parker.service"] != daemonUnit {
		t.Errorf("daemon unit mangled:\n%s", files["/etc/systemd/system/parker.service"])
	}
	if files["/usr/local/libexec/parker-machine-ready"] != readyScript {
		t.Errorf("ready script mangled")
	}
	if !strings.Contains(daemonUnit, "--listen :"+strconv.Itoa(DaemonPort)) {
		t.Error("daemon must listen on DaemonPort")
	}
	if len(doc.RunCmd) != 3 {
		t.Errorf("runcmd: %v", doc.RunCmd)
	}

	if ud := UserData("k", nil); strings.Contains(ud, "mount0") {
		t.Error("no --mount: only the machine's own share")
	}
}

func TestSeedISO(t *testing.T) {
	p := filepath.Join(t.TempDir(), "seed.iso")
	if err := WriteSeed(p, "#cloud-config\n", MetaData("parker-1")); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := iso9660.OpenImage(f)
	if err != nil {
		t.Fatal(err)
	}
	if l, _ := img.Label(); l != "cidata" {
		t.Errorf("label %q, want cidata (NoCloud looks for it)", l)
	}
	root, err := img.RootDir()
	if err != nil {
		t.Fatal(err)
	}
	kids, err := root.GetChildren()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, k := range kids {
		b, _ := io.ReadAll(k.Reader())
		got[strings.ToLower(k.Name())] = string(b)
	}
	if got["meta-data"] != "instance-id: parker-1\nlocal-hostname: parker-machine\n" || got["user-data"] != "#cloud-config\n" {
		t.Errorf("iso files: %v", got)
	}
}

func TestConvertDisk(t *testing.T) {
	dir := t.TempDir()
	disk := make([]byte, 3<<20)
	copy(disk, "boot")
	copy(disk[2<<20:], "root")
	src := filepath.Join(dir, "raw.img") // raw input exercises the same path as qcow2
	if err := os.WriteFile(src, disk, 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "disk.img")
	if err := ConvertDisk(src, dst, 1); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != 1<<30 {
		t.Fatalf("size %d, want 1 GiB", len(b))
	}
	if !bytes.Equal(b[:len(disk)], disk) {
		t.Error("disk contents differ")
	}
}

func TestPinnedImages(t *testing.T) {
	for _, arch := range []string{"arm64", "amd64"} {
		img, err := ImageFor(arch)
		if err != nil {
			t.Fatal(err)
		}
		if len(img.SHA256) != 64 || !strings.HasPrefix(img.URL, "https://") || !strings.Contains(img.URL, arch) {
			t.Errorf("%s: %+v", arch, img)
		}
	}
	if _, err := ImageFor("riscv64"); err == nil {
		t.Error("riscv64: want an error")
	}
}

func TestMACAndKey(t *testing.T) {
	mac, err := RandomMAC()
	if err != nil {
		t.Fatal(err)
	}
	hw, err := net.ParseMAC(mac)
	if err != nil || hw[0]&0x02 == 0 || hw[0]&0x01 != 0 {
		t.Errorf("%s: want a locally administered unicast MAC", mac)
	}
	m := Machine{Dir: t.TempDir()}
	pub, err := m.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := ssh.ParseAuthorizedKey([]byte(pub)); err != nil {
		t.Errorf("public key %q: %v", pub, err)
	}
	b, _ := os.ReadFile(m.KeyPath())
	if _, err := ssh.ParsePrivateKey(b); err != nil {
		t.Errorf("private key: %v", err)
	}
	if fi, _ := os.Stat(m.KeyPath()); fi.Mode().Perm() != 0o600 {
		t.Errorf("key mode %v", fi.Mode().Perm())
	}
}

func TestConnectionNeedsRunningAndReady(t *testing.T) {
	m := Machine{Dir: t.TempDir()}
	if err := os.MkdirAll(m.ShareDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Connection(); ok {
		t.Fatal("nothing running: no connection")
	}
	_ = os.WriteFile(m.PIDPath(), []byte(strconv.Itoa(os.Getpid())), 0o644)
	if _, ok := m.Connection(); ok {
		t.Fatal("running but not ready: no connection")
	}
	_ = os.WriteFile(m.ReadyPath(), []byte(`{"ip":"192.168.64.5"}`), 0o644)
	_ = os.WriteFile(m.TokenPath(), []byte("tok\n"), 0o600)
	c, ok := m.Connection()
	if !ok || c.Host != "https://192.168.64.5:7443" || c.Token != "tok" || c.CAFile != m.CAPath() {
		t.Fatalf("connection %+v %v", c, ok)
	}
	_ = os.WriteFile(m.PIDPath(), []byte("999999999"), 0o644)
	if _, ok := m.Connection(); ok {
		t.Error("stale pid: no connection")
	}
}

func TestWaitReady(t *testing.T) {
	m := Machine{Dir: t.TempDir()}
	_ = os.MkdirAll(m.ShareDir(), 0o700)
	since := time.Now()
	go func() {
		time.Sleep(200 * time.Millisecond)
		_ = os.WriteFile(m.TokenPath(), []byte("t"), 0o600)
		_ = os.WriteFile(m.ReadyPath(), []byte(`{"ip":"10.0.0.2","version":"v1"}`), 0o644)
	}()
	r, err := m.WaitReady(since, 5*time.Second, func() bool { return true }, nil)
	if err != nil || r.IP != "10.0.0.2" {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := (Machine{Dir: t.TempDir()}).WaitReady(time.Now(), 5*time.Second, func() bool { return false }, nil); err == nil {
		t.Error("VM gone: want an error")
	}
}

func TestSetDaemon(t *testing.T) {
	m := Machine{Dir: t.TempDir()}
	_ = os.MkdirAll(m.ShareDir(), 0o700)
	dir := t.TempDir()
	elf := filepath.Join(dir, "parker")
	_ = os.WriteFile(elf, []byte("\x7fELFrest"), 0o755)
	if err := m.SetDaemon("", elf); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(m.BinaryPath()); string(b) != "\x7fELFrest" {
		t.Error("binary not copied")
	}
	macho := filepath.Join(dir, "mac")
	_ = os.WriteFile(macho, []byte("\xcf\xfa\xed\xfe"), 0o755)
	if err := m.SetDaemon("", macho); err == nil {
		t.Error("a macOS binary: want an error")
	}
	// A release replaces the dev binary.
	if err := m.SetDaemon("v0.9.0", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(m.BinaryPath()); err == nil {
		t.Error("dev binary left behind")
	}
	if b, _ := os.ReadFile(m.VersionPath()); string(b) != "0.9.0\n" {
		t.Errorf("version file %q", b)
	}
}

func TestConfigRoundTrip(t *testing.T) {
	m := Machine{Dir: t.TempDir()}
	if _, err := m.LoadConfig(); err == nil || !strings.Contains(err.Error(), "parker machine init") {
		t.Errorf("no config: %v", err)
	}
	c := Config{CPUs: 2, MemoryMiB: 2048, DiskGiB: 20, MAC: "02:00:00:00:00:01", Mounts: []string{"/Users/me/labs"}}
	if err := m.SaveConfig(c); err != nil {
		t.Fatal(err)
	}
	got, err := m.LoadConfig()
	if err != nil || !reflect.DeepEqual(got, c) {
		t.Errorf("%+v %v", got, err)
	}
	if !m.Initialized() {
		t.Error("Initialized after SaveConfig")
	}
}
