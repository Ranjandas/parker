package machine

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	qcow2reader "github.com/lima-vm/go-qcow2reader"
	"github.com/lima-vm/go-qcow2reader/convert"
)

// Image is a pinned cloud image (qcow2; converted to the raw disk Apple's
// Virtualization framework boots). Its SHA-256 is pinned (SHA256), or read
// from the distro's CHECKSUM file at download time (ChecksumURL).
type Image struct {
	Name        string // shown to the user
	URL         string
	SHA256      string
	ChecksumURL string
}

// DefaultImage is what `init` uses without --image.
const DefaultImage = "ubuntu"

// images by --image name, then CPU (runtime.GOARCH).
//
// ubuntu: Ubuntu 24.04 LTS minimal, release 20261105 (about 220 MB).
// fedora: Fedora Cloud Base Generic 43-1.6 (about 520 MB); Fedora's mirror
// network may be closer than Ubuntu's.
var images = map[string]map[string]Image{
	"ubuntu": {
		"arm64": {
			Name:   "Ubuntu 24.04 minimal (20261105, arm64)",
			URL:    "https://cloud-images.ubuntu.com/minimal/releases/noble/release-20261105/ubuntu-24.04-minimal-cloudimg-arm64.img",
			SHA256: "3dc8370d923978d4b610fa7964d1b36ba040eefb43198809ca5bc8ac51777b18",
		},
		"amd64": {
			Name:   "Ubuntu 24.04 minimal (20261105, amd64)",
			URL:    "https://cloud-images.ubuntu.com/minimal/releases/noble/release-20261105/ubuntu-24.04-minimal-cloudimg-amd64.img",
			SHA256: "406630c08b5e545fcc7209854b129d47e3af822db128c9f19336593b5a2d3fe0",
		},
	},
	"fedora": {
		"arm64": {
			Name:        "Fedora Cloud 43 (1.6, aarch64)",
			URL:         "https://download.fedoraproject.org/pub/fedora/linux/releases/43/Cloud/aarch64/images/Fedora-Cloud-Base-Generic-43-1.6.aarch64.qcow2",
			ChecksumURL: "https://download.fedoraproject.org/pub/fedora/linux/releases/43/Cloud/aarch64/images/Fedora-Cloud-43-1.6-aarch64-CHECKSUM",
		},
		"amd64": {
			Name:        "Fedora Cloud 43 (1.6, x86_64)",
			URL:         "https://download.fedoraproject.org/pub/fedora/linux/releases/43/Cloud/x86_64/images/Fedora-Cloud-Base-Generic-43-1.6.x86_64.qcow2",
			ChecksumURL: "https://download.fedoraproject.org/pub/fedora/linux/releases/43/Cloud/x86_64/images/Fedora-Cloud-43-1.6-x86_64-CHECKSUM",
		},
	},
}

// ImageNames are the --image choices.
func ImageNames() []string { return slices.Sorted(maps.Keys(images)) }

// ImageFor returns the named image for a CPU (runtime.GOARCH).
func ImageFor(name, arch string) (Image, error) {
	byArch, ok := images[name]
	if !ok {
		return Image{}, fmt.Errorf("no machine image %q (want one of %s)", name, strings.Join(ImageNames(), ", "))
	}
	img, ok := byArch[arch]
	if !ok {
		return Image{}, fmt.Errorf("no %s machine image for %s", name, arch)
	}
	return img, nil
}

// HostImage is ImageFor this CPU.
func HostImage(name string) (Image, error) { return ImageFor(name, runtime.GOARCH) }

// checksumFrom finds file's SHA-256 in a checksum list: Fedora's
// "SHA256 (file) = hex" lines or sha256sum's "hex  file" / "hex *file".
func checksumFrom(list, file string) (string, bool) {
	for _, l := range strings.Split(list, "\n") {
		f := strings.Fields(l)
		switch {
		case len(f) == 4 && f[0] == "SHA256" && f[1] == "("+file+")" && f[2] == "=":
			return f[3], len(f[3]) == 64
		case len(f) == 2 && strings.TrimPrefix(f[1], "*") == file && len(f[0]) == 64:
			return f[0], true
		}
	}
	return "", false
}

// wantSHA256 is img's checksum: pinned, or read from its checksum list.
func wantSHA256(img Image) (string, error) {
	if img.SHA256 != "" {
		return img.SHA256, nil
	}
	resp, err := http.Get(img.ChecksumURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s: %s", img.ChecksumURL, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	sum, ok := checksumFrom(string(b), path.Base(img.URL))
	if !ok {
		return "", fmt.Errorf("%s does not list %s", img.ChecksumURL, path.Base(img.URL))
	}
	return sum, nil
}

// CacheDir holds downloaded images across machines (PARKER_CACHE, else
// the user cache dir).
func CacheDir() (string, error) {
	if d := os.Getenv("PARKER_CACHE"); d != "" {
		return filepath.Join(d, "images"), nil
	}
	d, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "parker", "images"), nil
}

// Download fetches img into the cache, once, and checks its SHA-256.
func Download(img Image, out io.Writer) (string, error) {
	dir, err := CacheDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	// Cached by file name: the pinned names never change content.
	dst := filepath.Join(dir, path.Base(img.URL))
	if _, err := os.Stat(dst); err == nil {
		return dst, nil
	}
	want, err := wantSHA256(img)
	if err != nil {
		return "", err
	}
	resp, err := http.Get(img.URL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s: %s", img.URL, resp.Status)
	}
	tmp, err := os.CreateTemp(dir, "download-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	pw := &progress{out: out, total: resp.ContentLength, label: "downloading " + img.Name}
	if _, err := io.Copy(io.MultiWriter(tmp, h, pw), resp.Body); err != nil {
		tmp.Close()
		return "", err
	}
	pw.done()
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return "", fmt.Errorf("%s: checksum mismatch (want %s, got %s)", img.URL, want, got)
	}
	return dst, os.Rename(tmp.Name(), dst)
}

// ConvertDisk writes the image at src to dst as a sparse raw disk, then
// grows it to sizeGiB (cloud-init grows the root partition on first boot).
func ConvertDisk(src, dst string, sizeGiB uint64) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	img, err := qcow2reader.Open(f)
	if err != nil {
		return fmt.Errorf("%s: %w", src, err)
	}
	defer img.Close()
	if err := img.Readable(); err != nil {
		return fmt.Errorf("%s: %w", src, err)
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	if err := convert.Convert(out, img, convert.Options{}); err != nil {
		out.Close()
		return err
	}
	size := max(int64(sizeGiB)<<30, img.Size())
	if err := out.Truncate(size); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

type progress struct {
	out   io.Writer
	total int64
	n     int64
	label string
	last  time.Time
}

func (p *progress) Write(b []byte) (int, error) {
	p.n += int64(len(b))
	if p.out != nil && time.Since(p.last) > 500*time.Millisecond {
		p.last = time.Now()
		if p.total > 0 {
			fmt.Fprintf(p.out, "\r\033[K%s: %d%% (%d of %d MB)", p.label, p.n*100/p.total, p.n>>20, p.total>>20)
		} else {
			fmt.Fprintf(p.out, "\r\033[K%s: %d MB", p.label, p.n>>20)
		}
	}
	return len(b), nil
}

func (p *progress) done() {
	if p.out != nil {
		fmt.Fprintf(p.out, "\r\033[K%s: done (%d MB)\n", p.label, p.n>>20)
	}
}
