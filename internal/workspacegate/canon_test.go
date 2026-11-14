package workspacegate

import "testing"

// finalPathToGateParent is pure string handling, so its whole contract is
// pinned here on every platform, including the Linux lane that cannot run
// the native Windows tests.
func TestFinalPathToGateParent(t *testing.T) {
	const guidRoot = `\\?\Volume{0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0}\`
	for _, tc := range []struct {
		in, want string
	}{
		{`\\?\C:\x\y`, `C:\x\y`},
		{`\\?\C:\`, `C:\`},
		{`\\?\d:\work`, `d:\work`},
		{`\\?\UNC\srv\share\x`, `\\srv\share\x`},
		{`\\?\UNC\srv\share`, `\\srv\share`},
		{guidRoot + `x`, guidRoot + `x`},
		{guidRoot, guidRoot},
	} {
		got, err := finalPathToGateParent(tc.in)
		if err != nil {
			t.Errorf("finalPathToGateParent(%q): unexpected error %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("finalPathToGateParent(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	for _, in := range []string{
		``,
		`C:\x`,
		`\Device\HarddiskVolume3\x`,
		`\\?\UNC\srv`,
		`\\?\UNC\srv\`,
		`\\?\UNC\\share`,
		`\\?\C:`,
		`\\?\C:x`,
		`\\?\`,
		`\\?\Volume{0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0}`,
		`\\?\Volume{0f1e2d3c`,
		`\\?\GLOBALROOT\Device\HarddiskVolume3\x`,
		`\\?\1:\x`,
	} {
		if got, err := finalPathToGateParent(in); err == nil {
			t.Errorf("finalPathToGateParent(%q) = %q, want an error", in, got)
		}
	}
}
