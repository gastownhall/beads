package proxy

import (
	"testing"
	"time"
)

func TestIdleTimeoutFromEnv(t *testing.T) {
	const fallback = 30 * time.Second
	cases := []struct {
		name string
		set  bool
		val  string
		want time.Duration
	}{
		{"unset returns fallback", false, "", fallback},
		{"blank returns fallback", true, "   ", fallback},
		{"unparseable returns fallback", true, "soon", fallback},
		{"valid minutes", true, "10m", 10 * time.Minute},
		{"valid seconds", true, "90s", 90 * time.Second},
		{"zero disables (verbatim)", true, "0", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.set {
				t.Setenv(IdleTimeoutEnvVar, tc.val)
			} else {
				t.Setenv(IdleTimeoutEnvVar, "")
			}
			if got := IdleTimeoutFromEnv(fallback); got != tc.want {
				t.Fatalf("IdleTimeoutFromEnv(%q) = %v, want %v", tc.val, got, tc.want)
			}
		})
	}
}
