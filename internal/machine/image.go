package machine

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	qcow2reader "github.com/lima-vm/go-qcow2reader"
	"github.com/lima-vm/go-qcow2reader/convert"
)

// Image is a pinned cloud image (qcow2; converted to the raw disk Apple's
// Virtualization framework boots).
type Image struct {
	Name   string // shown to the user
	URL    string
	SHA256 string
}

// Ubuntu 24.04 LTS, release 20261017: EFI-bootable, its kernel has
// virtiofs, and cloud-init reads the NoCloud seed.
var images = map[string]Image{
	"arm64": {
		Name:   "Ubuntu 24.04 (20261017, arm64)",
		URL:    "https://cloud-images.ubuntu.com/releases/noble/release-20261017/ubuntu-24.04-server-cloudimg-arm64.img",
		SHA256: "50e24e1fb79e89116e939d90aa7738f7af48c442fa1bc2510131e0b39c50348e",
	},
	"amd64": {
		Name:   "Ubuntu 24.04 (20261017, amd64)",
		URL:    "https://cloud-images.ubuntu.com/releases/noble/release-20261017/ubuntu-24.04-server-cloudimg-amd64.img",
		SHA256: "04e8f2461a16a44e78a370284471f29616cdd9baf1893df25807b71f985b6c94",
	},
}

// ImageFor returns the pinned image for a CPU (runtime.GOARCH).
func ImageFor(arch string) (Image, error) {
	img, ok := images[arch]
	if !ok {
		return Image{}, fmt.Errorf("no machine image for %s", arch)
	}
	return img, nil
}

// HostImage is ImageFor this CPU.
func HostImage() (Image, error) { return ImageFor(runtime.GOARCH) }

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
	dst := filepath.Join(dir, img.SHA256+".qcow2")
	if _, err := os.Stat(dst); err == nil {
		return dst, nil
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
	if got := hex.EncodeToString(h.Sum(nil)); got != img.SHA256 {
		return "", fmt.Errorf("%s: checksum mismatch (want %s, got %s)", img.URL, img.SHA256, got)
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
			fmt.Fprintf(p.out, "\r%s: %d%% (%d of %d MB)", p.label, p.n*100/p.total, p.n>>20, p.total>>20)
		} else {
			fmt.Fprintf(p.out, "\r%s: %d MB", p.label, p.n>>20)
		}
	}
	return len(b), nil
}

func (p *progress) done() {
	if p.out != nil {
		fmt.Fprintf(p.out, "\r%s: done (%d MB)\n", p.label, p.n>>20)
	}
}
