// Package fetch downloads pinned HashiCorp binaries into a local cache and
// verifies them against the release SHA256SUMS.
package fetch

import (
	"archive/zip"
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// CacheDir is $PARKER_CACHE, or the invoking user's cache dir (under sudo,
// SUDO_USER's) + /parker, so `sudo parker up` reuses downloads.
func CacheDir() string {
	if d := os.Getenv("PARKER_CACHE"); d != "" {
		return d
	}
	if u := os.Getenv("SUDO_USER"); u != "" && os.Geteuid() == 0 {
		return filepath.Join("/home", u, ".cache", "parker")
	}
	d, err := os.UserCacheDir()
	if err != nil {
		d = os.TempDir()
	}
	return filepath.Join(d, "parker")
}

// releasesURL is PARKER_RELEASES_URL (a mirror laid out like
// releases.hashicorp.com), else releases.hashicorp.com.
func releasesURL() string {
	if u := os.Getenv("PARKER_RELEASES_URL"); u != "" {
		return strings.TrimRight(u, "/")
	}
	return "https://releases.hashicorp.com"
}

// Cached returns the path of product@version if it is already there (or a
// path: build that exists), without downloading.
func Cached(product, version string) (string, bool) {
	p, ok := strings.CutPrefix(version, "path:")
	if !ok {
		p = filepath.Join(CacheDir(), product, version, product)
	}
	if _, err := os.Stat(p); err != nil {
		return "", false
	}
	return p, true
}

// Binary returns the path of product@version, downloading it if needed.
// version may be "path:/abs/binary" to use a local build.
func Binary(product, version string, log io.Writer) (string, error) {
	if p, ok := strings.CutPrefix(version, "path:"); ok {
		if _, err := os.Stat(p); err != nil {
			return "", err
		}
		return p, nil
	}
	dir := filepath.Join(CacheDir(), product, version)
	bin := filepath.Join(dir, product)
	if _, err := os.Stat(bin); err == nil {
		return bin, nil
	}
	fmt.Fprintf(log, "fetching %s %s ...\n", product, version)

	base := fmt.Sprintf("%s/%s/%s", releasesURL(), product, version)
	zipName := fmt.Sprintf("%s_%s_%s_%s.zip", product, version, runtime.GOOS, runtime.GOARCH)

	want, err := checksum(base+fmt.Sprintf("/%s_%s_SHA256SUMS", product, version), zipName)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, "dl-*.zip")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()

	resp, err := http.Get(base + "/" + zipName)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("download %s: %s", zipName, resp.Status)
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), resp.Body); err != nil {
		return "", err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return "", fmt.Errorf("%s: checksum mismatch (got %s, want %s)", zipName, got, want)
	}

	zr, err := zip.OpenReader(tmp.Name())
	if err != nil {
		return "", err
	}
	defer zr.Close()
	for _, f := range zr.File {
		if f.Name != product {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return "", err
		}
		defer rc.Close()
		part := bin + ".part"
		out, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			return "", err
		}
		if _, err := io.Copy(out, rc); err != nil {
			out.Close()
			return "", err
		}
		out.Close()
		chownToSudoUser(dir, part)
		return bin, os.Rename(part, bin)
	}
	return "", fmt.Errorf("%s: no %q in archive", zipName, product)
}

func checksum(url, file string) (string, error) {
	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("%s: %s (is the version right?)", url, resp.Status)
	}
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && f[1] == file {
			return f[0], nil
		}
	}
	return "", fmt.Errorf("%s: no checksum for %s", url, file)
}
