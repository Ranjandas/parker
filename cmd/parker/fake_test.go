package main

import (
	"testing"
)

func TestFakeFlags(t *testing.T) {
	c, f := newFakeCmd()
	if err := c.Flags().Parse([]string{"--health", "503", "--error-rate", "0", "--latency", "p50=200ms,p99=2s", "--rate-limit", "10", "--service", "api"}); err != nil {
		t.Fatal(err)
	}
	st, err := f.step(c, "dc1/api")
	if err != nil {
		t.Fatal(err)
	}
	fs := st.Fake
	if fs.On != "dc1/api" || fs.Service != "api" || fs.Health.Code != 503 || fs.Errors.Rate == nil || *fs.Errors.Rate != 0 ||
		fs.Latency.P50 != "200ms" || fs.Latency.P99 != "2s" || *fs.RateLimit.RPS != 10 || fs.Reset {
		t.Errorf("step = %+v", fs)
	}

	c, f = newFakeCmd()
	_ = c.Flags().Parse([]string{"--error-code", "502"})
	st, _ = f.step(c, "dc1/api")
	if st.Fake.Errors.Rate != nil || st.Fake.Errors.Code != 502 || st.Fake.Health != nil || st.Fake.Latency != nil {
		t.Errorf("only --error-code: %+v", st.Fake)
	}
	c, f = newFakeCmd()
	if st, _ := f.step(c, "dc1/api"); !st.Fake.Empty() {
		t.Errorf("no flags should show: %+v", st.Fake)
	}
	c, f = newFakeCmd()
	_ = c.Flags().Parse([]string{"--latency", "p75=1s"})
	if _, err := f.step(c, "dc1/api"); err == nil {
		t.Error("p75 accepted")
	}
}
