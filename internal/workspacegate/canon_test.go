package workspacegate

import "testing"

// TestFinalPathToGateParent pins the string conversion from a Windows
// GetFinalPathNameByHandle result to the spelling a gate path uses: the
// extended-length prefix is dropped for drive-letter and UNC volumes, a
// volume-GUID spelling is kept verbatim (a letterless volume root keeps
// its trailing separator so a guarded directory directly at that root
// still resolves), and every unrecognized shape is an error — the caller
// falls back to another resolver rather than gate on a guessed path.
// The function is pure string handling, so the table runs on Linux.
func TestFinalPathToGateParent(t *testing.T) {
	const guid = `\\?\Volume{0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0}`
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"drive path", `\\?\C:\x\y`, `C:\x\y`, false},
		{"drive root", `\\?\C:\`, `C:\`, false},
		{"unc path", `\\?\UNC\srv\share\x`, `\\srv\share\x`, false},
		{"volume guid path", guid + `\x`, guid + `\x`, false},
		{"volume guid root keeps separator", guid + `\`, guid + `\`, false},
		{"empty", ``, ``, true},
		{"no extended-length prefix", `C:\x\y`, ``, true},
		{"nt device form", `\Device\HarddiskVolume3\x`, ``, true},
		{"unc without share", `\\?\UNC\srv`, ``, true},
		{"bare prefix", `\\?\`, ``, true},
		{"unc share empty", `\\?\UNC\srv\`, ``, true},
		{"drive path without root", `\\?\C:`, ``, true},
		{"lowercase drive", `\\?\c:\x`, ``, true},
		{"volume guid unclosed", `\\?\Volume{0f1e2d3c`, ``, true},
		{"volume guid without separator", `\\?\Volume{0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0}`, ``, true},
		{"not a volume spelling", `\\?\con`, ``, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := finalPathToGateParent(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("finalPathToGateParent(%q) = %q, want an error", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("finalPathToGateParent(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("finalPathToGateParent(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
