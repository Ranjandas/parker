// Command sitegen builds the website: it copies site/ and renders the user
// guide (docs/guide/*.md, in the order of docs/guide/nav.yaml) into
// <out>/docs/, in the site's look, with a sidebar, an "On this page" list,
// prev/next links and a search index. The lab file reference is generated
// from internal/spec/parker.schema.json, so it can't drift from the code.
//
//	go run ./hack/sitegen -out _site      # then serve _site/
package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/goccy/go-yaml"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer/html"
)

//go:embed page.html
var pageHTML string

// Nav is docs/guide/nav.yaml: sections of page slugs, in reading order.
type Nav struct {
	Sections []struct {
		Title string   `yaml:"title"`
		Pages []string `yaml:"pages"`
	} `yaml:"sections"`
}

// Page is one rendered guide page.
type Page struct {
	Slug, Title, Description, Section string
	Body                              template.HTML
	TOC                               []Heading
	Prev, Next                        *Page
	Source                            string // path in the repo, for "Edit this page"
}

// Heading is an h2 or h3 of a page.
type Heading struct {
	Level    int
	ID, Text string
}

// Doc is a guide Markdown file: front matter, then Markdown.
type Doc struct {
	Title, Description string
	Markdown           string
}

func main() {
	site := flag.String("site", "site", "static site to copy")
	guide := flag.String("guide", "docs/guide", "guide Markdown")
	schema := flag.String("schema", "internal/spec/parker.schema.json", "JSON Schema for the lab file reference")
	out := flag.String("out", "_site", "output directory")
	flag.Parse()
	if err := build(*site, *guide, *schema, *out); err != nil {
		fmt.Fprintln(os.Stderr, "sitegen:", err)
		os.Exit(1)
	}
}

func build(site, guide, schemaPath, out string) error {
	if err := os.RemoveAll(out); err != nil {
		return err
	}
	if err := copyTree(site, out); err != nil {
		return err
	}
	pages, err := Load(guide, schemaPath)
	if err != nil {
		return err
	}
	tmpl, err := template.New("page").Parse(pageHTML)
	if err != nil {
		return err
	}
	nav, err := readNav(guide)
	if err != nil {
		return err
	}
	bySlug := map[string]*Page{}
	for _, p := range pages {
		bySlug[p.Slug] = p
	}
	type navSection struct {
		Title string
		Pages []*Page
	}
	var sections []navSection
	for _, s := range nav.Sections {
		ns := navSection{Title: s.Title}
		for _, slug := range s.Pages {
			ns.Pages = append(ns.Pages, bySlug[slug])
		}
		sections = append(sections, ns)
	}
	dir := filepath.Join(out, "docs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	type entry struct {
		URL, Title, Section, Heading, Text string
	}
	var index []entry
	for _, p := range pages {
		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, map[string]any{"Page": p, "Nav": sections}); err != nil {
			return fmt.Errorf("%s: %w", p.Slug, err)
		}
		name := p.Slug + ".html"
		if p.Slug == "index" {
			name = "index.html"
		}
		if err := os.WriteFile(filepath.Join(dir, name), buf.Bytes(), 0o644); err != nil {
			return err
		}
		for _, s := range searchSections(p) {
			index = append(index, entry{URL: pageURL(p.Slug) + s.anchor, Title: p.Title, Section: p.Section, Heading: s.heading, Text: s.text})
		}
	}
	js, err := json.Marshal(index)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "search.json"), js, 0o644)
}

func pageURL(slug string) string {
	if slug == "index" {
		return "./"
	}
	return slug + ".html"
}

func readNav(guide string) (*Nav, error) {
	b, err := os.ReadFile(filepath.Join(guide, "nav.yaml"))
	if err != nil {
		return nil, err
	}
	var n Nav
	if err := yaml.UnmarshalWithOptions(b, &n, yaml.Strict()); err != nil {
		return nil, fmt.Errorf("nav.yaml: %w", err)
	}
	return &n, nil
}

// Load reads and renders every page the nav lists, in order, and checks
// that every .md in the guide is listed.
func Load(guide, schemaPath string) ([]*Page, error) {
	nav, err := readNav(guide)
	if err != nil {
		return nil, err
	}
	md := goldmark.New(
		goldmark.WithExtensions(extension.GFM),
		goldmark.WithParserOptions(parser.WithAutoHeadingID(), parser.WithAttribute()),
		goldmark.WithRendererOptions(html.WithUnsafe()),
	)
	listed := map[string]bool{}
	var pages []*Page
	for _, s := range nav.Sections {
		for _, slug := range s.Pages {
			if listed[slug] {
				return nil, fmt.Errorf("nav.yaml lists %s twice", slug)
			}
			listed[slug] = true
			src := filepath.Join(guide, slug+".md")
			doc, err := ReadDoc(src)
			if err != nil {
				return nil, err
			}
			text := doc.Markdown
			if strings.Contains(text, schemaMarker) {
				ref, err := SchemaReference(schemaPath)
				if err != nil {
					return nil, err
				}
				text = strings.Replace(text, schemaMarker, ref, 1)
			}
			var buf bytes.Buffer
			if err := md.Convert([]byte(text), &buf); err != nil {
				return nil, fmt.Errorf("%s: %w", src, err)
			}
			body := postProcess(buf.String())
			pages = append(pages, &Page{
				Slug: slug, Title: doc.Title, Description: doc.Description, Section: s.Title,
				Body: template.HTML(body), TOC: headings(body), Source: filepath.ToSlash(src),
			})
		}
	}
	for i, p := range pages {
		if i > 0 {
			p.Prev = pages[i-1]
		}
		if i < len(pages)-1 {
			p.Next = pages[i+1]
		}
	}
	files, err := filepath.Glob(filepath.Join(guide, "*.md"))
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		if slug := strings.TrimSuffix(filepath.Base(f), ".md"); !listed[slug] {
			return nil, fmt.Errorf("%s isn't listed in nav.yaml", f)
		}
	}
	if err := checkLinks(pages); err != nil {
		return nil, err
	}
	return pages, nil
}

var (
	localLink = regexp.MustCompile(`href="((?:[a-z0-9-]+\.html|\./)?)(#[^"]*)?"`)
	idRe      = regexp.MustCompile(` id="([^"]+)"`)
)

// checkLinks fails on a link between guide pages whose page or heading
// doesn't exist, so renaming a heading can't silently break a link.
func checkLinks(pages []*Page) error {
	ids := map[string]map[string]bool{}
	for _, p := range pages {
		m := map[string]bool{}
		for _, sm := range idRe.FindAllStringSubmatch(string(p.Body), -1) {
			m[sm[1]] = true
		}
		ids[pageURL(p.Slug)] = m
	}
	var bad []string
	for _, p := range pages {
		for _, sm := range localLink.FindAllStringSubmatch(string(p.Body), -1) {
			target, frag := sm[1], strings.TrimPrefix(sm[2], "#")
			if target == "" {
				target = pageURL(p.Slug)
			}
			m, ok := ids[target]
			switch {
			case !ok:
				bad = append(bad, fmt.Sprintf("%s.md: link to missing page %s", p.Slug, target))
			case frag != "" && !m[frag]:
				bad = append(bad, fmt.Sprintf("%s.md: link to missing heading %s#%s", p.Slug, target, frag))
			}
		}
	}
	if len(bad) > 0 {
		return errors.New("broken links:\n  " + strings.Join(bad, "\n  "))
	}
	return nil
}

// ReadDoc reads a guide page: a front matter block with title and
// description, then Markdown.
func ReadDoc(path string) (*Doc, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	s := string(b)
	if !strings.HasPrefix(s, "---\n") {
		return nil, fmt.Errorf("%s: no front matter", path)
	}
	fm, rest, ok := strings.Cut(s[4:], "\n---\n")
	if !ok {
		return nil, fmt.Errorf("%s: front matter isn't closed", path)
	}
	var meta struct {
		Title       string `yaml:"title"`
		Description string `yaml:"description"`
	}
	if err := yaml.UnmarshalWithOptions([]byte(fm), &meta, yaml.Strict()); err != nil {
		return nil, fmt.Errorf("%s: front matter: %w", path, err)
	}
	if meta.Title == "" || meta.Description == "" {
		return nil, fmt.Errorf("%s: front matter needs title and description", path)
	}
	return &Doc{Title: meta.Title, Description: meta.Description, Markdown: rest}, nil
}

var (
	mdLink   = regexp.MustCompile(`href="([a-z0-9-]+)\.md(#[^"]*)?"`)
	codeRe   = regexp.MustCompile(`(?s)<pre><code class="language-([a-z]+)">(.*?)</code></pre>`)
	plainRe  = regexp.MustCompile(`(?s)<pre><code>(.*?)</code></pre>`)
	quoteRe  = regexp.MustCompile(`(?s)<blockquote>\s*<p><strong>(Note|Tip|Warning|Enterprise):</strong>`)
	headRe   = regexp.MustCompile(`<h([23]) id="([^"]+)">(.*?)</h[23]>`)
	tagRe    = regexp.MustCompile(`<[^>]+>`)
	tableRe  = regexp.MustCompile(`(?s)<table>(.*?)</table>`)
	extLink  = regexp.MustCompile(`<a href="(https?://[^"]+)"`)
	anchorRe = regexp.MustCompile(`<h([23]) id="([^"]+)">`)
)

// postProcess turns links to other pages into .html links, highlights code,
// styles callouts, wraps tables so they scroll, and adds anchor links.
func postProcess(body string) string {
	body = mdLink.ReplaceAllStringFunc(body, func(m string) string {
		sm := mdLink.FindStringSubmatch(m)
		return `href="` + pageURL(sm[1]) + sm[2] + `"`
	})
	body = codeRe.ReplaceAllStringFunc(body, func(m string) string {
		sm := codeRe.FindStringSubmatch(m)
		lang, code := sm[1], unescape(sm[2])
		return codeBlock(lang, code)
	})
	body = plainRe.ReplaceAllStringFunc(body, func(m string) string {
		return codeBlock("text", unescape(plainRe.FindStringSubmatch(m)[1]))
	})
	body = quoteRe.ReplaceAllStringFunc(body, func(m string) string {
		kind := quoteRe.FindStringSubmatch(m)[1]
		return `<blockquote class="callout callout-` + strings.ToLower(kind) + `"><p><strong>` + kind + `:</strong>`
	})
	body = tableRe.ReplaceAllString(body, `<div class="table"><table>$1</table></div>`)
	body = extLink.ReplaceAllString(body, `<a href="$1" rel="noopener"`)
	body = anchorRe.ReplaceAllString(body, `<h$1 id="$2"><a class="anchor" href="#$2" aria-hidden="true">#</a>`)
	return body
}

func unescape(s string) string {
	r := strings.NewReplacer("&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#39;", "'", "&amp;", "&")
	return r.Replace(s)
}

func headings(body string) []Heading {
	var hs []Heading
	for _, m := range headRe.FindAllStringSubmatch(body, -1) {
		text := strings.TrimPrefix(tagRe.ReplaceAllString(m[3], ""), "#")
		hs = append(hs, Heading{Level: int(m[1][0] - '0'), ID: m[2], Text: unescape(text)})
	}
	return hs
}

type searchSection struct{ anchor, heading, text string }

// searchSections splits a page at its h2s: one search entry per section.
func searchSections(p *Page) []searchSection {
	body := string(p.Body)
	locs := regexp.MustCompile(`<h2 id="([^"]+)">`).FindAllStringSubmatchIndex(body, -1)
	var out []searchSection
	add := func(anchor, heading, html string) {
		text := strings.Join(strings.Fields(unescape(tagRe.ReplaceAllString(html, " "))), " ")
		text = strings.TrimPrefix(text, "# ")
		if len(text) > 600 {
			text = text[:600]
		}
		out = append(out, searchSection{anchor, heading, text})
	}
	start := len(body)
	if len(locs) > 0 {
		start = locs[0][0]
	}
	add("", p.Title, body[:start])
	for i, l := range locs {
		end := len(body)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		id := body[l[2]:l[3]]
		h := ""
		for _, t := range p.TOC {
			if t.ID == id {
				h = t.Text
			}
		}
		add("#"+id, h, body[l[1]:end])
	}
	return out
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return errors.New(path + ": not a regular file")
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	})
}
