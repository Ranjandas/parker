package machine

import (
	"bytes"
	"fmt"
	"os"
	"strings"

	"github.com/kdomanski/iso9660"
)

// Guest paths.
const (
	guestShare = "/mnt/parker-machine"
	shareTag   = "parker"
)

// installScript puts parker in the VM before each daemon start: the host's
// dev binary from the share if there is one, else a release.
const installScript = `#!/bin/sh
set -eu
src=` + guestShare + `/bin/parker
dst=/usr/local/bin/parker
# Transparent proxy needs iptables; some images (Ubuntu minimal) lack it.
if ! command -v iptables >/dev/null 2>&1; then
  if command -v apt-get >/dev/null 2>&1; then
    apt-get update -q && DEBIAN_FRONTEND=noninteractive apt-get install -yq --no-install-recommends iptables
  elif command -v dnf >/dev/null 2>&1; then
    dnf install -y -q iptables-nft
  fi
fi
mark=/var/lib/parker/machine-installed
mkdir -p /var/lib/parker
if [ -x "$src" ]; then
  cmp -s "$src" "$dst" || install -m 0755 "$src" "$dst"
  echo binary > "$mark"
  exit 0
fi
want=$(cat ` + guestShare + `/version 2>/dev/null || true)
have=$(cat "$mark" 2>/dev/null || true)
# Nothing to do if this release is in; with no version known (the host was
# offline), keep any release already installed.
if [ -x "$dst" ] && [ "$have" != binary ] && { [ -z "$want" ] || [ "$want" = "$have" ]; }; then
  exit 0
fi
curl -fsSL https://ranjandas.github.io/parker/install.sh |
  PARKER_INSTALL_DIR=/usr/local/bin PARKER_VERSION="$want" sh
echo "$want" > "$mark"
`

// readyScript hands the host what it needs to reach the daemon, through the
// share, once the daemon serves TLS.
const readyScript = `#!/bin/sh
set -eu
share=` + guestShare + `
export PARKER_STATE_DIR=/var/lib/parker
i=0
until [ -s "$PARKER_STATE_DIR/tls/cert.pem" ]; do
  i=$((i + 1)); [ "$i" -gt 120 ] && { echo "no daemon certificate after 120s" >&2; exit 1; }
  sleep 1
done
parker daemon credentials >/dev/null
install -m 0600 "$PARKER_STATE_DIR/token" "$share/token"
install -m 0644 "$PARKER_STATE_DIR/tls/cert.pem" "$share/ca.pem"
ip=$(ip -4 -o addr show scope global | awk '{print $4}' | cut -d/ -f1 | head -n1)
ver=$(parker --version 2>/dev/null | head -n1 || true)
printf '{"ip":"%s","boot_id":"%s","version":"%s","time":"%s"}\n' \
  "$ip" "$(cat /proc/sys/kernel/random/boot_id)" "$ver" "$(date -u +%FT%TZ)" > "$share/ready.json.tmp"
mv "$share/ready.json.tmp" "$share/ready.json"
`

const daemonUnit = `[Unit]
Description=Parker lab daemon (parker machine)
After=network-online.target
Wants=network-online.target
RequiresMountsFor=` + guestShare + `

[Service]
Type=simple
Environment=PARKER_CACHE=/var/cache/parker
Environment=PARKER_STATE_DIR=/var/lib/parker
ExecStartPre=/usr/local/libexec/parker-machine-install
ExecStart=/usr/local/bin/parker daemon --listen :7443
KillMode=process
Restart=on-failure
RestartSec=2
TimeoutStartSec=300
CacheDirectory=parker
StateDirectory=parker

[Install]
WantedBy=multi-user.target
`

const readyUnit = `[Unit]
Description=Tell the host the Parker daemon is up (parker machine)
After=parker.service
Requires=parker.service
RequiresMountsFor=` + guestShare + `

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/usr/local/libexec/parker-machine-ready

[Install]
WantedBy=multi-user.target
`

// MountTag is the virtiofs tag of the i-th --mount.
func MountTag(i int) string { return fmt.Sprintf("mount%d", i) }

// UserData is the cloud-init config: a login user with the machine's key,
// the virtiofs shares, and the daemon as a systemd service.
func UserData(pubKey string, mounts []string) string {
	var b strings.Builder
	b.WriteString("#cloud-config\n")
	b.WriteString("hostname: parker-machine\n")
	b.WriteString("users:\n")
	fmt.Fprintf(&b, "  - name: %s\n", User)
	b.WriteString("    shell: /bin/bash\n")
	b.WriteString("    sudo: ALL=(ALL) NOPASSWD:ALL\n")
	b.WriteString("    lock_passwd: true\n")
	b.WriteString("    ssh_authorized_keys:\n")
	fmt.Fprintf(&b, "      - %q\n", pubKey)
	b.WriteString("ssh_pwauth: false\n")
	b.WriteString("mounts:\n")
	fmt.Fprintf(&b, "  - [%s, %s, virtiofs, \"rw,nofail\", \"0\", \"0\"]\n", shareTag, guestShare)
	for i, dir := range mounts {
		fmt.Fprintf(&b, "  - [%s, %q, virtiofs, \"rw,nofail\", \"0\", \"0\"]\n", MountTag(i), dir)
	}
	b.WriteString("write_files:\n")
	file := func(path, perm, content string) {
		fmt.Fprintf(&b, "  - path: %s\n    permissions: %q\n    content: |\n", path, perm)
		for _, l := range strings.Split(strings.TrimRight(content, "\n"), "\n") {
			b.WriteString("      " + l + "\n")
		}
	}
	file("/usr/local/libexec/parker-machine-install", "0755", installScript)
	file("/usr/local/libexec/parker-machine-ready", "0755", readyScript)
	file("/etc/systemd/system/parker.service", "0644", daemonUnit)
	file("/etc/systemd/system/parker-machine-ready.service", "0644", readyUnit)
	b.WriteString("runcmd:\n")
	b.WriteString("  - [systemctl, daemon-reload]\n")
	b.WriteString("  - [systemctl, enable, --now, parker.service]\n")
	b.WriteString("  - [systemctl, enable, --now, parker-machine-ready.service]\n")
	return b.String()
}

// MetaData names the instance; a new id makes cloud-init run again.
func MetaData(instanceID string) string {
	return fmt.Sprintf("instance-id: %s\nlocal-hostname: parker-machine\n", instanceID)
}

// WriteSeed writes a NoCloud seed ISO (volume label "cidata").
func WriteSeed(path, userData, metaData string) error {
	w, err := iso9660.NewWriter()
	if err != nil {
		return err
	}
	defer w.Cleanup()
	for name, body := range map[string]string{"user-data": userData, "meta-data": metaData} {
		if err := w.AddFile(bytes.NewReader([]byte(body)), name); err != nil {
			return err
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := w.WriteTo(f, "cidata"); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
