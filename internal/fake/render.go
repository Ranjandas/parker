package fake

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

// Tree draws an answer as a tree, a hop per line: name, where it ran,
// code and time, and any fault, error or cycle. fact adds each hop's fact.
func Tree(r *Response, fact bool) []string {
	var out []string
	var walk func(r *Response, prefix, branch string)
	walk = func(r *Response, prefix, branch string) {
		out = append(out, prefix+branch+hopLine(r))
		next := prefix
		switch branch {
		case "├─ ":
			next += "│  "
		case "└─ ":
			next += "   "
		}
		if fact && r.Fact != "" {
			bar := "   "
			if len(r.UpstreamCalls) > 0 {
				bar = "│  "
			}
			out = append(out, next+bar+"✦ "+r.Fact)
		}
		keys := slices.Sorted(maps.Keys(r.UpstreamCalls))
		for i, k := range keys {
			b := "├─ "
			if i == len(keys)-1 {
				b = "└─ "
			}
			walk(r.UpstreamCalls[k], next, b)
		}
	}
	walk(r, "", "")
	return out
}

func hopLine(r *Response) string {
	name := r.Name
	if name == "" {
		name = r.URI
	}
	parts := []string{name}
	var where []string
	for _, v := range []string{r.Node, r.DC, r.Version} {
		if v != "" {
			where = append(where, v)
		}
	}
	if len(where) > 0 {
		parts = append(parts, strings.Join(where, " · "))
	}
	if r.Type != "" && r.Type != "HTTP" {
		parts = append(parts, r.Type)
	}
	code := fmt.Sprint(r.Code)
	if r.Code < 0 {
		code = "failed"
	}
	parts = append(parts, code)
	if r.Duration != "" {
		parts = append(parts, r.Duration)
	}
	if r.Injected != "" {
		parts = append(parts, "injected "+r.Injected)
	}
	if r.Cycle {
		parts = append(parts, "cycle: stopped here")
	}
	if r.Error != "" && r.Injected == "" {
		parts = append(parts, r.Error)
	}
	return strings.Join(parts, "  ")
}

// Hops lists every hop of an answer, the root first, depth first.
func Hops(r *Response) []*Response {
	out := []*Response{r}
	for _, k := range slices.Sorted(maps.Keys(r.UpstreamCalls)) {
		out = append(out, Hops(r.UpstreamCalls[k])...)
	}
	return out
}

// Oneline sums up an answer for a traffic loop: the code and time, then
// each hop as name@dc/version.
func Oneline(r *Response) string {
	var hops []string
	for _, h := range Hops(r) {
		s := h.Name
		if s == "" {
			s = h.URI
		}
		if h.DC != "" {
			s += "@" + h.DC
		}
		if h.Version != "" {
			s += "/" + h.Version
		}
		if h.Code < 0 || h.Code >= 400 {
			s += fmt.Sprintf("(%d)", h.Code)
		}
		hops = append(hops, s)
	}
	return fmt.Sprintf("%d  %-9s %s", r.Code, r.Duration, strings.Join(hops, " → "))
}

// Split counts which dc and version answered for each service, over many
// answers: the traffic split a loop shows.
type Split struct {
	Total int
	// failed counts answers with a code of 400 or more.
	failed int
	// By service, then "dc1 v1" style labels.
	counts map[string]map[string]int
}

// Add counts one answer's hops (the root's too).
func (s *Split) Add(r *Response) {
	if s.counts == nil {
		s.counts = map[string]map[string]int{}
	}
	s.Total++
	if r.Code >= 400 {
		s.failed++
	}
	seen := map[string]bool{}
	for _, h := range Hops(r) {
		if h.Name == "" {
			continue
		}
		label := strings.TrimSpace(h.DC + " " + h.Version)
		if label == "" {
			label = h.Node
		}
		if seen[h.Name+"\x00"+label] {
			continue
		}
		seen[h.Name+"\x00"+label] = true
		if s.counts[h.Name] == nil {
			s.counts[h.Name] = map[string]int{}
		}
		s.counts[h.Name][label]++
	}
}

// Failed is how many answers failed (code 400 or more).
func (s *Split) Failed() int { return s.failed }

// Lines reports services answered by more than one dc or version, as
// "api: dc1 v1 70% · dc2 v1 30%", or nothing when there's no split.
func (s *Split) Lines() []string {
	var out []string
	for _, svc := range slices.Sorted(maps.Keys(s.counts)) {
		c := s.counts[svc]
		if len(c) < 2 {
			continue
		}
		total := 0
		for _, n := range c {
			total += n
		}
		labels := slices.SortedFunc(maps.Keys(c), func(a, b string) int {
			if c[a] != c[b] {
				return c[b] - c[a]
			}
			return strings.Compare(a, b)
		})
		var parts []string
		for _, l := range labels {
			parts = append(parts, fmt.Sprintf("%s %d%%", l, (c[l]*100+total/2)/total))
		}
		out = append(out, svc+": "+strings.Join(parts, " · "))
	}
	return out
}
