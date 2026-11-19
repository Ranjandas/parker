package netsetup

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
)

const netnsDir = "/run/netns"

func Label(lab string) string { return "parker:" + lab }

// NetnsName is parker-<lab>_<node>. Lab and node names can't contain '_',
// so the separator keeps lab "a" from claiming lab "a-b"'s namespaces.
func NetnsName(lab, node string) string { return "parker-" + lab + "_" + node }

func NetnsPath(lab, node string) string { return filepath.Join(netnsDir, NetnsName(lab, node)) }

func short(parts ...string) string {
	h := sha256.Sum256([]byte(strings.Join(parts, "/")))
	return hex.EncodeToString(h[:])[:8]
}

func BridgeName(lab, network string) string { return "pk-" + short(lab, network) }
