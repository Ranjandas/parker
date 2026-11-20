package webui

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/Ranjandas/parker/internal/labfile"
)

// The builder's file endpoints, under /ui/build/. They run where the UI
// is served (the `parker ui` process, or the daemon with --listen), so a
// path is a path on that machine, and relative imports and file: refs
// resolve there, as they would for `parker up FILE` run on it.
//
//	check  {yaml, path}       the file checked, with merged and expanded forms,
//	                          its trees, and where each path sits
//	patch  {yaml, ops}        the file with ops spliced in (labfile.Patch)
//	open   {path}             a .yaml file's text
//	save   {path, yaml}       writes a .yaml file
//	up     {yaml, path}       brings the lab up; progress streams as text
const buildPrefix = "/ui/build/"

type buildReq struct {
	YAML string       `json:"yaml"`
	Path string       `json:"path"`
	Ops  []labfile.Op `json:"ops"`
}

func (h *Handler) build(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !sameOrigin(r) {
		http.Error(w, "cross-origin request refused", http.StatusForbidden)
		return
	}
	if !h.valid(r) {
		http.Error(w, "sign in again", http.StatusUnauthorized)
		return
	}
	var req buildReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2*labfile.MaxSize)).Decode(&req); err != nil {
		http.Error(w, "want JSON {yaml, path, ops}", http.StatusBadRequest)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	switch strings.TrimPrefix(r.URL.Path, buildPrefix) {
	case "check":
		path, err := labPath(req.Path, false)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		res := labfile.Check(req.YAML, path)
		out := struct {
			labfile.Result
			Tree   json.RawMessage   `json:"tree,omitempty"`   // the merged file, keys in order
			Source json.RawMessage   `json:"source,omitempty"` // the file itself, keys in order
			Spans  map[string][2]int `json:"spans"`            // where each path sits in the file
		}{Result: res, Spans: labfile.Spans(req.YAML)}
		if res.OK {
			out.Tree, _ = labfile.TreeJSON(res.Merged)
		}
		out.Source, _ = labfile.TreeJSON(req.YAML)
		writeJSON(w, out)
	case "patch":
		text, err := labfile.Patch(req.YAML, req.Ops)
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		writeJSON(w, map[string]string{"yaml": text})
	case "open":
		path, err := labPath(req.Path, true)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		b, err := readLabFile(path)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		writeJSON(w, map[string]string{"yaml": string(b), "path": path})
	case "save":
		path, err := labPath(req.Path, true)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := writeLabFile(path, []byte(req.YAML)); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]string{"path": path})
	case "up":
		h.up(w, r, req)
	default:
		http.NotFound(w, r)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// labPath is a topology file's path on this machine. A bare name (a file
// the browser uploaded) sits in no real directory; need says a real,
// absolute path is wanted.
func labPath(p string, need bool) (string, error) {
	p = strings.TrimSpace(p)
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		p = filepath.Join(home, rest)
	}
	if !filepath.IsAbs(p) {
		if need {
			return "", fmt.Errorf("%q: want an absolute path (or ~/...) on %s", p, hostLabel())
		}
		name := filepath.Base(p)
		if p == "" || name == "." || name == "/" {
			name = "lab.yaml"
		}
		return filepath.Join(os.TempDir(), "parker-builder", name), nil
	}
	p = filepath.Clean(p)
	if ext := strings.ToLower(filepath.Ext(p)); ext != ".yaml" && ext != ".yml" {
		return "", fmt.Errorf("%s: the builder opens and saves .yaml files only", p)
	}
	return p, nil
}

func hostLabel() string {
	if h, err := os.Hostname(); err == nil {
		return h
	}
	return "this machine"
}

func readLabFile(path string) ([]byte, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a file", path)
	}
	if st.Size() > labfile.MaxSize {
		return nil, fmt.Errorf("%s is over 1 MiB", path)
	}
	return os.ReadFile(path)
}

// writeLabFile replaces the file in one step, keeping its mode.
func writeLabFile(path string, b []byte) error {
	if len(b) > labfile.MaxSize {
		return errors.New("the file is over 1 MiB")
	}
	mode := os.FileMode(0o644)
	if st, err := os.Stat(path); err == nil {
		if !st.Mode().IsRegular() {
			return fmt.Errorf("%s is not a file", path)
		}
		mode = st.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".parker-builder-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// up checks the file and brings its lab up, streaming progress lines as
// plain text. The last line is "parker: up <lab>" or "parker: error: ...".
func (h *Handler) up(w http.ResponseWriter, r *http.Request, req buildReq) {
	if h.o.Up == nil {
		http.Error(w, "this UI can't start labs; run parker up", http.StatusNotImplemented)
		return
	}
	path, err := labPath(req.Path, false)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	res := labfile.Check(req.YAML, path)
	if !res.OK {
		msgs := make([]string, len(res.Problems))
		for i, p := range res.Problems {
			msgs[i] = p.Message
		}
		http.Error(w, "the file has problems:\n"+strings.Join(msgs, "\n"), http.StatusUnprocessableEntity)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	fw := &flushWriter{w: w}
	if f, ok := w.(http.Flusher); ok {
		fw.f = f
	}
	for _, warn := range res.Warnings {
		fmt.Fprintf(fw, "warning: %s\n", warn)
	}
	if err := h.o.Up(r.Context(), res.Low, fw); err != nil {
		fmt.Fprintf(fw, "\nparker: error: %v\n", err)
		return
	}
	fmt.Fprintf(fw, "\nparker: up %s\n", res.Low.Name)
}

type flushWriter struct {
	w io.Writer
	f http.Flusher
}

func (f *flushWriter) Write(p []byte) (int, error) {
	n, err := f.w.Write(p)
	if f.f != nil {
		f.f.Flush()
	}
	return n, err
}
