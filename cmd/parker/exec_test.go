package main

import "testing"

func TestShellQuote(t *testing.T) {
	for in, want := range map[string]string{
		"https://10.1.0.10:8501":       "https://10.1.0.10:8501",
		"/var/lib/parker/run/x/ca.pem": "/var/lib/parker/run/x/ca.pem",
		"/home/me/my labs/ca.pem":      "'/home/me/my labs/ca.pem'",
		"it's":                         `'it'\''s'`,
		"":                             "''",
	} {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %s, want %s", in, got, want)
		}
	}
}
