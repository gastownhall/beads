package storage

import "testing"

func TestJSONMetadataPath(t *testing.T) {
	tests := []struct {
		key  string
		want string
	}{
		{"status", `$."status"`},
		{"my_field", `$."my_field"`},
		{"gc.routed_to", `$."gc.routed_to"`},
		{"gc.scope.ref", `$."gc.scope.ref"`},
		{"jira/sprint", `$."jira/sprint"`},
		{`ke"y`, `$."ke\"y"`},
		{`ke\y`, `$."ke\\y"`},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			if got := JSONMetadataPath(tt.key); got != tt.want {
				t.Errorf("JSONMetadataPath(%q) = %q, want %q", tt.key, got, tt.want)
			}
		})
	}
}
