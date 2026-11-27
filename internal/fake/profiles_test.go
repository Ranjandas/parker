package fake

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/Ranjandas/parker/internal/spec"
)

func TestProfiles(t *testing.T) {
	for _, n := range ProfileNames() {
		l := limitsOf(t, &spec.FakeLimits{Profile: n})
		if !l.On() || l.Profile != n || !strings.HasPrefix(l.summary(), n+": ") {
			t.Errorf("%s: %+v", n, l)
		}
		p := profiles[n]
		if p.title == "" || !strings.HasPrefix(p.source, "https://") || len(p.settings) == 0 {
			t.Errorf("%s: needs a title, a source and setting names", n)
		}
		for k := range p.settings {
			if !slices.Contains(limitKeys, k) {
				t.Errorf("%s: unknown limit %q", n, k)
			}
		}
	}
	// Keys beside a profile win; the profile's codes stay.
	l := limitsOf(t, &spec.FakeLimits{Profile: "tomcat", MaxHeaderBytes: size(16 << 10)})
	if l.HeaderBytes != 16<<10 || l.HeaderCode != 400 || l.HeaderCount != 100 {
		t.Errorf("tomcat + max_header_bytes: %+v", l)
	}
	// none clears a profile; an unknown one is an error.
	f := DefaultFaults()
	_ = f.Apply(&spec.Fake{Limits: &spec.FakeLimits{Profile: "nginx"}})
	if err := f.Apply(&spec.Fake{Limits: &spec.FakeLimits{Profile: "none"}}); err != nil || f.Limits.On() {
		t.Errorf("none: %v %+v", err, f.Limits)
	}
	if err := Validate(&spec.Fake{Limits: &spec.FakeLimits{Profile: "iis"}}); err == nil || !strings.Contains(err.Error(), "tomcat") {
		t.Errorf("unknown profile: %v", err)
	}
}

// A refusal names the server's own setting, so the reader knows what to
// change in production.
func TestProfileRefusalNamesSetting(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Cookie", strings.Repeat("c", 9000))
	for name, want := range map[string]string{
		"tomcat":   "400 request headers too large: 9045 bytes > tomcat maxHttpHeaderSize 8KB",
		"nginx":    "400 request header Cookie too large: 9008 bytes > nginx large_client_header_buffers 8KB",
		"gunicorn": "400 request header Cookie too large: 9008 bytes > gunicorn limit_request_field_size 8190B",
	} {
		rf := limitsOf(t, &spec.FakeLimits{Profile: name}).check(r)
		if rf == nil {
			t.Errorf("%s: passed", name)
			continue
		}
		if got := fmt.Sprintf("%d %s", rf.code, rf.msg); got != want {
			t.Errorf("%s:\n got %s\nwant %s", name, got, want)
		}
	}
	if rf := limitsOf(t, &spec.FakeLimits{Profile: "node"}).check(r); rf != nil {
		t.Errorf("node allows 16 KB: %s", rf.msg)
	}
}

// The guide's profiles table shows the values the code uses.
func TestGuideProfilesTable(t *testing.T) {
	b, err := os.ReadFile("../../docs/guide/fake-service.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range ProfileNames() {
		if row := profileRow(n); !strings.Contains(string(b), row+"\n") {
			t.Errorf("fake-service.md lacks the row:\n%s", row)
		}
	}
}

// The schema's enum lists every profile.
func TestSchemaListsProfiles(t *testing.T) {
	f, _ := reflect.TypeFor[spec.FakeLimits]().FieldByName("Profile")
	tag := f.Tag.Get("jsonschema")
	for _, n := range append(ProfileNames(), "none") {
		if !strings.Contains(tag, "enum="+n) {
			t.Errorf("FakeLimits.Profile's schema enum lacks %s", n)
		}
	}
}
