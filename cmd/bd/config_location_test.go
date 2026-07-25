package main

import (
	"testing"

	"github.com/steveyegge/beads/internal/config"
)

func TestConfigLocationLabel(t *testing.T) {
	cases := []struct {
		src  config.ConfigSource
		want string
	}{
		{config.SourceDefault, "default"},
		{config.SourceConfigFile, "config.yaml"},
		{config.SourceEnvVar, "env"},
		{config.SourceFlag, "flag"},
	}
	for _, tc := range cases {
		if got := configLocationLabel(tc.src); got != tc.want {
			t.Errorf("configLocationLabel(%q)=%q, want %q", tc.src, got, tc.want)
		}
	}
}
