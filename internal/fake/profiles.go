package fake

import (
	"cmp"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/Ranjandas/parker/internal/spec"
)

// profile is a server's stock request limits, with the names of its own
// settings and where they were checked.
type profile struct {
	title    string
	limits   Limits
	settings map[string]string // limit key -> the server's setting
	source   string
}

// Profiles are the servers a fake can be limited like, by
// limits.profile. Each value is the server's default as its own docs
// give it (see source); the guide's table is checked against these.
var profiles = map[string]profile{
	"tomcat": {
		title:  "Apache Tomcat HTTP connector",
		limits: Limits{HeaderBytes: 8192, HeaderCount: 100, FormParams: 10000, HeaderCode: 400, URLCode: 400, BodyCode: 413, FormCode: 400, Concurrent: 200, Queue: 100, WhenFull: "refuse", FullCode: 503, PerConn: 100, IdleTimeout: 20 * time.Second, HeaderTimeout: 20 * time.Second, StrictCookies: true, StrictPath: true},
		settings: map[string]string{"max_header_bytes": "maxHttpHeaderSize", "max_header_count": "maxHeaderCount",
			"max_form_params": "maxParameterCount", "max_concurrent": "maxThreads", "max_requests_per_conn": "maxKeepAliveRequests", "idle_timeout": "keepAliveTimeout", "header_timeout": "connectionTimeout",
			"strict_cookies": "Rfc6265CookieProcessor", "strict_path_chars": "relaxedPathChars"},
		source: "https://tomcat.apache.org/tomcat-11.0-doc/config/http.html",
	},
	"nginx": {
		title:  "nginx",
		limits: Limits{HeaderLineBytes: 8192, HeaderBytes: 4 * 8192, URLBytes: 8192, BodyBytes: 1 << 20, HeaderCode: 400, URLCode: 414, BodyCode: 413, FormCode: 400, FullCode: 503, WhenFull: "refuse", PerConn: 1000, IdleTimeout: 75 * time.Second, HeaderTimeout: 60 * time.Second, Underscores: "drop"},
		settings: map[string]string{"max_header_line_bytes": "large_client_header_buffers", "max_header_bytes": "large_client_header_buffers",
			"max_url_bytes": "large_client_header_buffers", "max_body_bytes": "client_max_body_size", "max_requests_per_conn": "keepalive_requests", "idle_timeout": "keepalive_timeout", "header_timeout": "client_header_timeout",
			"underscore_headers": "underscores_in_headers"},
		source: "https://nginx.org/en/docs/http/ngx_http_core_module.html",
	},
	"apache": {
		title:  "Apache httpd",
		limits: Limits{HeaderLineBytes: 8190, HeaderCount: 100, URLBytes: 8190, BodyBytes: 1 << 30, HeaderCode: 400, URLCode: 414, BodyCode: 413, FormCode: 400, FullCode: 503, WhenFull: "refuse", PerConn: 100, IdleTimeout: 5 * time.Second},
		settings: map[string]string{"max_header_line_bytes": "LimitRequestFieldSize", "max_header_count": "LimitRequestFields",
			"max_url_bytes": "LimitRequestLine", "max_body_bytes": "LimitRequestBody", "max_requests_per_conn": "MaxKeepAliveRequests", "idle_timeout": "KeepAliveTimeout"},
		source: "https://httpd.apache.org/docs/2.4/mod/core.html",
	},
	"node": {
		title:    "Node.js http server",
		limits:   Limits{HeaderBytes: 16 << 10, HeaderCode: 431, URLCode: 414, BodyCode: 413, FormCode: 400, FullCode: 503, WhenFull: "refuse", IdleTimeout: 5 * time.Second, HeaderTimeout: 60 * time.Second},
		settings: map[string]string{"max_header_bytes": "maxHeaderSize", "idle_timeout": "keepAliveTimeout", "header_timeout": "headersTimeout"},
		source:   "https://nodejs.org/api/http.html#httpmaxheadersize",
	},
	"envoy": {
		title:    "Envoy",
		limits:   Limits{HeaderBytes: 60 << 10, HeaderCount: 100, HeaderCode: 431, URLCode: 414, BodyCode: 413, FormCode: 400, FullCode: 503, WhenFull: "refuse"},
		settings: map[string]string{"max_header_bytes": "max_request_headers_kb", "max_header_count": "max_headers_count"},
		source:   "https://www.envoyproxy.io/docs/envoy/latest/api-v3/config/core/v3/protocol.proto",
	},
	"go": {
		title:    "Go net/http",
		limits:   Limits{HeaderBytes: 1 << 20, HeaderCode: 431, URLCode: 414, BodyCode: 413, FormCode: 400, FullCode: 503, WhenFull: "refuse"},
		settings: map[string]string{"max_header_bytes": "MaxHeaderBytes"},
		source:   "https://pkg.go.dev/net/http#DefaultMaxHeaderBytes",
	},
	"alb": {
		title:  "AWS Application Load Balancer",
		limits: Limits{HeaderLineBytes: 16 << 10, HeaderBytes: 64 << 10, URLBytes: 16 << 10, HeaderCode: 400, URLCode: 414, BodyCode: 413, FormCode: 400, FullCode: 503, WhenFull: "refuse", IdleTimeout: 60 * time.Second},
		settings: map[string]string{"max_header_line_bytes": "single header quota", "max_header_bytes": "request header quota",
			"max_url_bytes": "request line quota", "idle_timeout": "idle timeout"},
		source: "https://docs.aws.amazon.com/elasticloadbalancing/latest/application/load-balancer-limits.html",
	},
	"kestrel": {
		title:  "ASP.NET Core Kestrel",
		limits: Limits{HeaderBytes: 32 << 10, HeaderCount: 100, URLBytes: 8 << 10, BodyBytes: 30_000_000, HeaderCode: 431, URLCode: 414, BodyCode: 413, FormCode: 400, FullCode: 503, WhenFull: "refuse", IdleTimeout: 2 * time.Minute, HeaderTimeout: 30 * time.Second},
		settings: map[string]string{"max_header_bytes": "MaxRequestHeadersTotalSize", "max_header_count": "MaxRequestHeaderCount",
			"max_url_bytes": "MaxRequestLineSize", "max_body_bytes": "MaxRequestBodySize", "idle_timeout": "KeepAliveTimeout", "header_timeout": "RequestHeadersTimeout"},
		source: "https://learn.microsoft.com/dotnet/api/microsoft.aspnetcore.server.kestrel.core.kestrelserverlimits",
	},
	"gunicorn": {
		title:  "Gunicorn",
		limits: Limits{HeaderLineBytes: 8190, HeaderCount: 100, URLBytes: 4094, HeaderCode: 400, URLCode: 400, BodyCode: 413, FormCode: 400, FullCode: 503, WhenFull: "refuse", IdleTimeout: 2 * time.Second},
		settings: map[string]string{"max_header_line_bytes": "limit_request_field_size", "max_header_count": "limit_request_fields",
			"max_url_bytes": "limit_request_line", "idle_timeout": "keep_alive"},
		source: "https://docs.gunicorn.org/en/stable/settings.html",
	},
}

// limitKeys are the limits a profile can name a setting for, in the
// guide's order.
var limitKeys = []string{"max_header_bytes", "max_header_line_bytes", "max_header_count", "max_url_bytes", "max_body_bytes", "max_form_params",
	"max_concurrent", "max_requests_per_conn", "idle_timeout", "header_timeout", "underscore_headers", "strict_cookies", "strict_path_chars"}

// ProfileNames lists the profiles, sorted.
func ProfileNames() []string { return slices.Sorted(maps.Keys(profiles)) }

// profileRow is the profile's row in the guide's profiles table, which a
// test keeps in step with the values here.
func profileRow(name string) string {
	p := profiles[name]
	l := p.limits
	var sets []string
	for _, k := range limitKeys {
		if s := p.settings[k]; s != "" && !slices.Contains(sets, "`"+s+"`") {
			sets = append(sets, "`"+s+"`")
		}
	}
	return "| `" + name + "` | [" + p.title + "](" + p.source + ") | " + l.summary() + " | " + strings.Join(sets, ", ") + " |"
}

// ProfileInfo is a profile as the lab builder shows it: the server, a
// one-line summary, and its values as a fake's limits: keys, written as
// a lab file writes them ("8KB", "20s").
type ProfileInfo struct {
	Title   string         `json:"title"`
	Summary string         `json:"summary"`
	Limits  map[string]any `json:"limits"`
}

// Profiles lists every profile for the lab builder, plus "" for no
// profile: the codes a limit answers with when none sets them.
func Profiles() map[string]ProfileInfo {
	out := map[string]ProfileInfo{"": {Limits: limitKeysOf(DefaultLimits())}}
	for n, p := range profiles {
		out[n] = ProfileInfo{Title: p.title, Summary: p.limits.summary(), Limits: limitKeysOf(p.limits)}
	}
	return out
}

// limitKeysOf writes l as limits: keys, leaving out the limits it
// doesn't set.
func limitKeysOf(l Limits) map[string]any {
	m := map[string]any{}
	size := func(k string, v int64) {
		if v > 0 {
			m[k] = spec.ByteSize(v).String()
		}
	}
	num := func(k string, v int) {
		if v > 0 {
			m[k] = v
		}
	}
	dur := func(k string, v time.Duration) {
		if v > 0 {
			m[k] = shortDur(v)
		}
	}
	size("max_header_bytes", l.HeaderBytes)
	size("max_header_line_bytes", l.HeaderLineBytes)
	num("max_header_count", l.HeaderCount)
	size("max_url_bytes", l.URLBytes)
	size("max_body_bytes", l.BodyBytes)
	num("max_form_params", l.FormParams)
	num("max_concurrent", l.Concurrent)
	num("queue", l.Queue)
	num("max_requests_per_conn", l.PerConn)
	dur("idle_timeout", l.IdleTimeout)
	dur("header_timeout", l.HeaderTimeout)
	num("h2_max_streams", l.H2Streams)
	m["when_full"] = cmp.Or(l.WhenFull, "refuse")
	m["underscore_headers"] = cmp.Or(l.Underscores, "allow")
	m["strict_cookies"] = l.StrictCookies
	m["strict_path_chars"] = l.StrictPath
	m["codes"] = map[string]int{"header": l.HeaderCode, "url": l.URLCode, "body": l.BodyCode, "form": l.FormCode, "full": l.FullCode}
	return m
}
