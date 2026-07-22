package main

import "testing"

func TestSelectCreateIDPrefix(t *testing.T) {
	tests := []struct {
		name        string
		global      bool
		yamlPrefix  string
		storePrefix string
		want        string
	}{
		{
			name:        "global ignores YAML",
			global:      true,
			yamlPrefix:  "proj0",
			storePrefix: "global",
			want:        "global",
		},
		{
			name:        "ordinary prefers YAML",
			yamlPrefix:  "proj0",
			storePrefix: "store",
			want:        "proj0",
		},
		{
			name:        "ordinary falls back to store",
			storePrefix: "store",
			want:        "store",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := selectCreateIDPrefix(tt.global, tt.yamlPrefix, tt.storePrefix); got != tt.want {
				t.Fatalf("selectCreateIDPrefix(%t, %q, %q) = %q, want %q",
					tt.global, tt.yamlPrefix, tt.storePrefix, got, tt.want)
			}
		})
	}
}
