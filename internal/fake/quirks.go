package fake

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
)

// pathChars are the characters Tomcat refuses unencoded in the request
// target unless relaxedPathChars or relaxedQueryChars allow them.
const pathChars = "\"<>[\\]^`{|}"

// checkTarget refuses a request target with a character strict_path_chars
// doesn't allow, as a server does while reading the request line.
func (l Limits) checkTarget(r *http.Request) *refusal {
	if !l.StrictPath {
		return nil
	}
	uri := r.RequestURI
	if uri == "" {
		uri = r.URL.RequestURI()
	}
	if i := strings.IndexAny(uri, pathChars); i >= 0 {
		return &refusal{http.StatusBadRequest, "strict_path_chars", fmt.Sprintf("invalid character %q in the request target (%s)", uri[i], l.name("strict_path_chars"))}
	}
	return nil
}

// clean applies the header quirks once the head is read: it refuses or
// drops headers with an underscore in their name, and drops cookies
// outside RFC 6265, returning what it dropped (names in lower case).
func (l Limits) clean(r *http.Request) (rf *refusal, headers, cookies []string) {
	if l.Underscores != "" {
		for _, name := range slices.Sorted(func(yield func(string) bool) {
			for k := range r.Header {
				if strings.Contains(k, "_") && !yield(k) {
					return
				}
			}
		}) {
			if l.Underscores == "reject" {
				return &refusal{http.StatusBadRequest, "underscore_headers", fmt.Sprintf("request header %s has an underscore in its name (%s)", strings.ToLower(name), l.name("underscore_headers"))}, nil, nil
			}
			r.Header.Del(name)
			headers = append(headers, strings.ToLower(name))
		}
	}
	if l.StrictCookies && len(r.Header["Cookie"]) > 0 {
		var keep []string
		for _, line := range r.Header["Cookie"] {
			for _, c := range strings.Split(line, ";") {
				c = strings.TrimSpace(c)
				if c == "" {
					continue
				}
				if validCookie(c) {
					keep = append(keep, c)
				} else {
					name, _, _ := strings.Cut(c, "=")
					cookies = append(cookies, name)
				}
			}
		}
		if len(cookies) > 0 {
			if len(keep) == 0 {
				r.Header.Del("Cookie")
			} else {
				r.Header["Cookie"] = []string{strings.Join(keep, "; ")}
			}
		}
	}
	return nil, headers, cookies
}

// validCookie reports whether name=value is a cookie-pair as RFC 6265
// section 4.1.1 defines it: a token name and a value of cookie-octets,
// optionally in double quotes.
func validCookie(pair string) bool {
	name, value, ok := strings.Cut(pair, "=")
	if !ok || name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		if !isTokenChar(name[i]) {
			return false
		}
	}
	if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
		value = value[1 : len(value)-1]
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		if c < 0x21 || c > 0x7e || c == '"' || c == ',' || c == ';' || c == '\\' {
			return false
		}
	}
	return true
}

// isTokenChar reports whether c may be in an HTTP token (RFC 9110).
func isTokenChar(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	}
	return strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0
}
