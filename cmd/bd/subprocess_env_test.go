//go:build cgo

package main

import "testing"

func TestHermeticSubprocessEnvironmentsPreserveTestMode(t *testing.T) {
	dir := t.TempDir()

	for _, tc := range []struct {
		name string
		env  func() []string
	}{
		{name: "bootstrap backend guard", env: func() []string { return bootstrapBackendGuardEnv(dir, dir) }},
		{name: "context binding", env: func() []string { return filteredEnvForContextBinding() }},
		{name: "create dependencies", env: func() []string { return createDepsTestEnv(dir) }},
		{name: "auto export", env: func() []string { return autoExportDataLossTestEnv(dir) }},
		{name: "embedded init", env: func() []string { return bdEnv(dir) }},
		{name: "init safety", env: func() []string { return hermeticInitEnv(dir) }},
		{name: "init backend", env: func() []string { return initBackendTestEnv(dir) }},
		{name: "proxied integration", env: func() []string { return bdProxiedEnv(dir) }},
		{name: "removed backend", env: func() []string { return removedBackendTestEnv(dir) }},
		{name: "multi-id update", env: func() []string { return multiIDUpdateEnv(dir) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, entry := range tc.env() {
				if entry == "BEADS_TEST_MODE=1" {
					return
				}
			}
			t.Fatal("subprocess environment does not preserve BEADS_TEST_MODE=1")
		})
	}
}
