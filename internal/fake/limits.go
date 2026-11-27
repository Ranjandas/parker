package fake

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Ranjandas/parker/internal/spec"
)

// maxHeaderCap is the most header bytes the fake's HTTP server reads, so
// that max_header_bytes, not Go's own 1 MB limit, decides. Limits above
// it are refused.
const maxHeaderCap = 4 << 20

// maxBodyRead bounds how much body is read to count form parameters
// when no body limit is set.
const maxBodyRead = 16 << 20

// Limits are request size limits, the way an app server refuses requests
// (0 is off), and the codes they answer.
type Limits struct {
	// Profile names the server whose defaults these started from.
	Profile         string `json:"profile,omitempty"`
	HeaderBytes     int64  `json:"max_header_bytes,omitempty"`
	HeaderLineBytes int64  `json:"max_header_line_bytes,omitempty"`
	HeaderCount     int    `json:"max_header_count,omitempty"`
	URLBytes        int64  `json:"max_url_bytes,omitempty"`
	BodyBytes       int64  `json:"max_body_bytes,omitempty"`
	FormParams      int    `json:"max_form_params,omitempty"`
	HeaderCode      int    `json:"header_code"`
	URLCode         int    `json:"url_code"`
	BodyCode        int    `json:"body_code"`
	FormCode        int    `json:"form_code"`
	// Concurrent requests at once, Queue waiting beyond that, and what
	// a refused one gets (WhenFull refuse or answer, with FullCode).
	Concurrent int    `json:"max_concurrent,omitempty"`
	Queue      int    `json:"queue,omitempty"`
	WhenFull   string `json:"when_full,omitempty"`
	FullCode   int    `json:"full_code"`
	// PerConn closes a keep-alive connection after this many requests.
	PerConn int `json:"max_requests_per_conn,omitempty"`
	// IdleTimeout, HeaderTimeout and H2Streams are the server's, set at
	// start.
	IdleTimeout   time.Duration `json:"idle_timeout,omitempty"`
	HeaderTimeout time.Duration `json:"header_timeout,omitempty"`
	H2Streams     int           `json:"h2_max_streams,omitempty"`
	// Underscores is allow (""), drop or reject for a header with an
	// underscore in its name; StrictCookies drops cookies outside RFC
	// 6265; StrictPath refuses unencoded " < > [ \ ] ^ ` { | } in the
	// request target.
	Underscores   string `json:"underscore_headers,omitempty"`
	StrictCookies bool   `json:"strict_cookies,omitempty"`
	StrictPath    bool   `json:"strict_path_chars,omitempty"`
}

// DefaultLimits is no limits, with the codes a limit answers once set.
func DefaultLimits() Limits {
	return Limits{HeaderCode: http.StatusRequestHeaderFieldsTooLarge, URLCode: http.StatusRequestURITooLong,
		BodyCode: http.StatusRequestEntityTooLarge, FormCode: http.StatusBadRequest, FullCode: http.StatusServiceUnavailable, WhenFull: "refuse"}
}

// On reports whether any limit is set.
func (l Limits) On() bool {
	return l.Profile != "" || l.sizeOn() || l.Concurrent > 0 || l.PerConn > 0 || l.IdleTimeout > 0 || l.HeaderTimeout > 0 || l.H2Streams > 0 || l.strictOn()
}

// strictOn reports whether any parsing quirk is on.
func (l Limits) strictOn() bool { return l.Underscores != "" || l.StrictCookies || l.StrictPath }

// sizeOn reports whether any request size limit is set.
func (l Limits) sizeOn() bool {
	return l.HeaderBytes > 0 || l.HeaderLineBytes > 0 || l.HeaderCount > 0 || l.URLBytes > 0 || l.BodyBytes > 0 || l.FormParams > 0
}

func (l *Limits) apply(sl *spec.FakeLimits) error {
	switch p, ok := profiles[sl.Profile]; {
	case sl.Profile == "":
	case sl.Profile == "none":
		*l = DefaultLimits()
	case !ok:
		return fmt.Errorf("limits.profile: want one of %s or none, got %q", strings.Join(ProfileNames(), ", "), sl.Profile)
	default:
		*l = p.limits
		l.Profile = sl.Profile
	}
	for _, s := range []struct {
		name string
		v    *spec.ByteSize
		dst  *int64
		cap  int64
	}{
		{"max_header_bytes", sl.MaxHeaderBytes, &l.HeaderBytes, maxHeaderCap},
		{"max_header_line_bytes", sl.MaxHeaderLineBytes, &l.HeaderLineBytes, maxHeaderCap},
		{"max_url_bytes", sl.MaxURLBytes, &l.URLBytes, maxHeaderCap},
		{"max_body_bytes", sl.MaxBodyBytes, &l.BodyBytes, 0},
	} {
		if s.v == nil {
			continue
		}
		if *s.v < 0 {
			return fmt.Errorf("limits.%s: want 0 or more, got %d", s.name, *s.v)
		}
		if s.cap > 0 && int64(*s.v) > s.cap {
			return fmt.Errorf("limits.%s: want at most %s, got %s", s.name, spec.ByteSize(s.cap), *s.v)
		}
		*s.dst = int64(*s.v)
	}
	for _, c := range []struct {
		name string
		v    *int
		dst  *int
	}{{"max_header_count", sl.MaxHeaderCount, &l.HeaderCount}, {"max_form_params", sl.MaxFormParams, &l.FormParams},
		{"max_concurrent", sl.MaxConcurrent, &l.Concurrent}, {"queue", sl.Queue, &l.Queue},
		{"max_requests_per_conn", sl.MaxRequestsPerConn, &l.PerConn}, {"h2_max_streams", sl.H2MaxStreams, &l.H2Streams}} {
		if c.v == nil {
			continue
		}
		if *c.v < 0 {
			return fmt.Errorf("limits.%s: want 0 or more, got %d", c.name, *c.v)
		}
		*c.dst = *c.v
	}
	switch sl.WhenFull {
	case "":
	case "refuse", "answer":
		l.WhenFull = sl.WhenFull
	default:
		return fmt.Errorf("limits.when_full: want refuse or answer, got %q", sl.WhenFull)
	}
	switch sl.UnderscoreHeaders {
	case "":
	case "allow":
		l.Underscores = ""
	case "drop", "reject":
		l.Underscores = sl.UnderscoreHeaders
	default:
		return fmt.Errorf("limits.underscore_headers: want allow, drop or reject, got %q", sl.UnderscoreHeaders)
	}
	if sl.StrictCookies != nil {
		l.StrictCookies = *sl.StrictCookies
	}
	if sl.StrictPathChars != nil {
		l.StrictPath = *sl.StrictPathChars
	}
	for _, d := range []struct {
		name, v string
		dst     *time.Duration
	}{{"idle_timeout", sl.IdleTimeout, &l.IdleTimeout}, {"header_timeout", sl.HeaderTimeout, &l.HeaderTimeout}} {
		if d.v == "" {
			continue
		}
		v, err := parseDur("limits."+d.name, d.v)
		if err != nil {
			return err
		}
		*d.dst = v
	}
	if c := sl.Codes; c != nil {
		for _, k := range []struct {
			name string
			v    int
			dst  *int
		}{{"header", c.Header, &l.HeaderCode}, {"url", c.URL, &l.URLCode}, {"body", c.Body, &l.BodyCode}, {"form", c.Form, &l.FormCode}, {"full", c.Full, &l.FullCode}} {
			if err := checkCode("limits.codes."+k.name, k.v); err != nil {
				return err
			}
			if k.v != 0 {
				*k.dst = k.v
			}
		}
	}
	return nil
}

// summary is the limits on one line: "header 8KB (400), url 8KB (414)".
func (l Limits) summary() string {
	var parts []string
	add := func(on bool, s string, code int) {
		if on {
			parts = append(parts, fmt.Sprintf("%s (%d)", s, code))
		}
	}
	add(l.HeaderBytes > 0, "headers "+spec.ByteSize(l.HeaderBytes).String(), l.HeaderCode)
	add(l.HeaderLineBytes > 0, "header line "+spec.ByteSize(l.HeaderLineBytes).String(), l.HeaderCode)
	add(l.HeaderCount > 0, fmt.Sprintf("%d headers", l.HeaderCount), l.HeaderCode)
	add(l.URLBytes > 0, "url "+spec.ByteSize(l.URLBytes).String(), l.URLCode)
	add(l.BodyBytes > 0, "body "+spec.ByteSize(l.BodyBytes).String(), l.BodyCode)
	add(l.FormParams > 0, fmt.Sprintf("%d params", l.FormParams), l.FormCode)
	if l.Concurrent > 0 {
		full := "refused"
		if l.WhenFull == "answer" {
			full = fmt.Sprint(l.FullCode)
		}
		parts = append(parts, fmt.Sprintf("%d at once + %d queued (%s)", l.Concurrent, l.Queue, full))
	}
	if l.PerConn > 0 {
		parts = append(parts, fmt.Sprintf("%d requests per connection", l.PerConn))
	}
	if l.IdleTimeout > 0 {
		parts = append(parts, "idle "+shortDur(l.IdleTimeout))
	}
	if l.HeaderTimeout > 0 {
		parts = append(parts, "header timeout "+shortDur(l.HeaderTimeout))
	}
	if l.H2Streams > 0 {
		parts = append(parts, fmt.Sprintf("%d h2 streams", l.H2Streams))
	}
	switch l.Underscores {
	case "drop":
		parts = append(parts, "underscore headers dropped")
	case "reject":
		parts = append(parts, "underscore headers refused (400)")
	}
	if l.StrictCookies {
		parts = append(parts, "strict cookies")
	}
	if l.StrictPath {
		parts = append(parts, "strict path chars (400)")
	}
	if l.Profile != "" {
		return l.Profile + ": " + strings.Join(parts, ", ")
	}
	return strings.Join(parts, ", ")
}

// refusal is a request a limit refused: the code, the limit's key and
// what was over it.
type refusal struct {
	code  int
	limit string
	msg   string
}

// name is how a refusal names a limit: its key, or with a profile the
// server's own setting ("tomcat maxHttpHeaderSize").
func (l Limits) name(key string) string {
	if p, ok := profiles[l.Profile]; ok {
		if set := p.settings[key]; set != "" {
			return l.Profile + " " + set
		}
	}
	return key
}

// check measures r against the limits. It reads the body only when a
// body or form limit needs it: a form body is put back for whoever reads
// it next, any other is consumed.
func (l Limits) check(r *http.Request) *refusal {
	if !l.sizeOn() {
		return nil
	}
	uri := r.RequestURI
	if uri == "" {
		uri = r.URL.RequestURI()
	}
	if l.URLBytes > 0 && int64(len(uri)) > l.URLBytes {
		return &refusal{l.URLCode, "max_url_bytes", fmt.Sprintf("request URI too long: %d bytes > %s %s", len(uri), l.name("max_url_bytes"), spec.ByteSize(l.URLBytes))}
	}
	h := measureHead(r, uri)
	if l.HeaderLineBytes > 0 && h.longestLen > l.HeaderLineBytes {
		return &refusal{l.HeaderCode, "max_header_line_bytes", fmt.Sprintf("request header %s too large: %d bytes > %s %s", h.longest, h.longestLen, l.name("max_header_line_bytes"), spec.ByteSize(l.HeaderLineBytes))}
	}
	if l.HeaderCount > 0 && h.count > l.HeaderCount {
		return &refusal{l.HeaderCode, "max_header_count", fmt.Sprintf("too many request headers: %d > %s %d", h.count, l.name("max_header_count"), l.HeaderCount)}
	}
	if l.HeaderBytes > 0 && h.total > l.HeaderBytes {
		return &refusal{l.HeaderCode, "max_header_bytes", fmt.Sprintf("request headers too large: %d bytes > %s %s", h.total, l.name("max_header_bytes"), spec.ByteSize(l.HeaderBytes))}
	}
	tooBig := func(n string) *refusal {
		return &refusal{l.BodyCode, "max_body_bytes", fmt.Sprintf("request body too large: %s > %s %s", n, l.name("max_body_bytes"), spec.ByteSize(l.BodyBytes))}
	}
	if l.BodyBytes > 0 && r.ContentLength > l.BodyBytes {
		return tooBig(fmt.Sprintf("%d bytes", r.ContentLength))
	}
	hasBody := r.Body != nil && r.Body != http.NoBody
	form := l.FormParams > 0 && strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded")
	var body []byte
	switch {
	case hasBody && form:
		max := int64(maxBodyRead)
		if l.BodyBytes > 0 {
			max = min(max, l.BodyBytes)
		}
		body, _ = io.ReadAll(io.LimitReader(r.Body, max+1))
		r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(body), r.Body))
		if l.BodyBytes > 0 && int64(len(body)) > l.BodyBytes {
			return tooBig("over")
		}
	case hasBody && l.BodyBytes > 0 && r.ContentLength < 0:
		// No length given (chunked): count it without keeping it.
		n, _ := io.Copy(io.Discard, io.LimitReader(r.Body, l.BodyBytes+1))
		r.Body = http.NoBody
		if n > l.BodyBytes {
			return tooBig("over")
		}
	}
	if l.FormParams > 0 {
		n := params(r.URL.RawQuery)
		if form {
			n += params(string(body))
		}
		if n > l.FormParams {
			return &refusal{l.FormCode, "max_form_params", fmt.Sprintf("too many parameters: %d > %s %d", n, l.name("max_form_params"), l.FormParams)}
		}
	}
	return nil
}

// head is a request's head as it is on the wire.
type head struct {
	total, longestLen int64
	count             int
	longest           string
}

// measureHead sizes the request's head HTTP/1.1 style: the request line,
// then "Name: value" lines (Host first), each ending in CRLF. HTTP/2
// requests are sized the same way, so a limit means the same on both.
func measureHead(r *http.Request, uri string) head {
	h := head{total: int64(len(r.Method) + 1 + len(uri) + 1 + len(r.Proto) + 2)}
	line := func(name, value string) {
		n := int64(len(name) + 2 + len(value))
		h.total += n + 2
		h.count++
		if n > h.longestLen {
			h.longest, h.longestLen = name, n
		}
	}
	if r.Host != "" {
		line("Host", r.Host)
	}
	for name, vs := range r.Header {
		for _, v := range vs {
			line(name, v)
		}
	}
	return h
}

// params counts the a=1&b=2 pairs in a query or form body.
func params(s string) int {
	n := 0
	for _, p := range strings.Split(s, "&") {
		if p != "" {
			n++
		}
	}
	return n
}

// shortDur writes 75s as 75s and 2m as 2m, not 1m15s or 2m0s.
func shortDur(d time.Duration) string {
	switch {
	case d%time.Minute == 0:
		return fmt.Sprintf("%dm", d/time.Minute)
	case d%time.Second == 0:
		return fmt.Sprintf("%ds", d/time.Second)
	}
	return d.String()
}
