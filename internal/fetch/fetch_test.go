package fetch

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

// fakeReleases serves product@version like releases.hashicorp.com.
func fakeReleases(t *testing.T, product, version string, content []byte, badSum bool) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var zbuf bytes.Buffer
	zw := zip.NewWriter(&zbuf)
	w, _ := zw.Create(product)
	w.Write(content)
	zw.Close()
	sum := sha256.Sum256(zbuf.Bytes())
	hexsum := hex.EncodeToString(sum[:])
	if badSum {
		hexsum = strings.Repeat("0", 64)
	}
	zipName := fmt.Sprintf("%s_%s_%s_%s.zip", product, version, runtime.GOOS, runtime.GOARCH)
	var downloads atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc(fmt.Sprintf("/%s/%s/%s_%s_SHA256SUMS", product, version, product, version), func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s  other.zip\n%s  %s\n", strings.Repeat("1", 64), hexsum, zipName)
	})
	mux.HandleFunc(fmt.Sprintf("/%s/%s/%s", product, version, zipName), func(w http.ResponseWriter, r *http.Request) {
		downloads.Add(1)
		w.Write(zbuf.Bytes())
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Setenv("PARKER_RELEASES_URL", srv.URL+"/")
	t.Setenv("PARKER_CACHE", t.TempDir())
	return srv, &downloads
}

func TestBinaryDownloadsVerifiesAndCaches(t *testing.T) {
	_, downloads := fakeReleases(t, "consul", "1.22.7", []byte("#!/bin/sh\necho consul\n"), false)
	var log bytes.Buffer
	bin, err := Binary("consul", "1.22.7", &log)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(CacheDir(), "consul", "1.22.7", "consul"); bin != want {
		t.Errorf("path %q, want %q", bin, want)
	}
	st, err := os.Stat(bin)
	if err != nil || st.Mode()&0o111 == 0 {
		t.Fatalf("binary not executable: %v %v", st, err)
	}
	if !strings.Contains(log.String(), "fetching consul 1.22.7") {
		t.Errorf("log %q", log.String())
	}
	// Second call: cache hit, no download, no log.
	if _, err := Binary("consul", "1.22.7", io.Discard); err != nil {
		t.Fatal(err)
	}
	if downloads.Load() != 1 {
		t.Errorf("downloads = %d, want 1", downloads.Load())
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(bin), "*")); len(left) != 1 {
		t.Errorf("temp files left behind: %v", left)
	}
}

func TestBinaryChecksumMismatch(t *testing.T) {
	fakeReleases(t, "consul", "1.22.7", []byte("x"), true)
	_, err := Binary("consul", "1.22.7", io.Discard)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(CacheDir(), "consul", "1.22.7", "consul")); err == nil {
		t.Error("bad download was cached")
	}
}

func TestBinaryUnknownVersion(t *testing.T) {
	fakeReleases(t, "consul", "1.22.7", []byte("x"), false)
	_, err := Binary("consul", "9.9.9", io.Discard)
	if err == nil || !strings.Contains(err.Error(), "is the version right?") {
		t.Fatalf("err = %v", err)
	}
}

func TestBinaryLocalPath(t *testing.T) {
	f := filepath.Join(t.TempDir(), "consul")
	os.WriteFile(f, nil, 0o755)
	if got, err := Binary("consul", "path:"+f, io.Discard); err != nil || got != f {
		t.Errorf("path: = %q, %v", got, err)
	}
	if _, err := Binary("consul", "path:/nonexistent/consul", io.Discard); err == nil {
		t.Error("missing local binary accepted")
	}
}

func TestCacheDir(t *testing.T) {
	t.Setenv("PARKER_CACHE", "/x/cache")
	if CacheDir() != "/x/cache" {
		t.Error("PARKER_CACHE ignored")
	}
	t.Setenv("PARKER_CACHE", "")
	t.Setenv("XDG_CACHE_HOME", "/y")
	if os.Geteuid() != 0 || os.Getenv("SUDO_USER") == "" {
		if got := CacheDir(); got != "/y/parker" {
			t.Errorf("CacheDir = %q", got)
		}
	}
}
