package workspacegate

import (
	"errors"
	"io/fs"
	"testing"
)

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

// resolveParent's order and laziness are the only way to exercise the
// volume-GUID branch without an admin-created volume, so they are pinned
// here with counting resolvers.
func TestResolveParent(t *testing.T) {
	type fake struct {
		path string
		err  error
	}
	ok := func(p string) fake { return fake{path: p} }
	fail := func(err error) fake { return fake{err: err} }
	errDOS := errors.New("no DOS name")
	errShape := errors.New("unsupported shape")
	errGUID := errors.New("no volume GUID")

	for _, tc := range []struct {
		name            string
		dos, guid, eval fake
		want            string
		wantCalls       [3]int // dos, guid, eval
		wantErrIs       []error
	}{
		{
			name: "DOS accepted", dos: ok(`C:\x`), guid: ok(`\\?\Volume{g}\x`), eval: ok(`C:\eval`),
			want: `C:\x`, wantCalls: [3]int{1, 0, 0},
		},
		{
			name: "DOS fails, GUID accepted", dos: fail(errDOS), guid: ok(`\\?\Volume{g}\x`), eval: ok(`C:\eval`),
			want: `\\?\Volume{g}\x`, wantCalls: [3]int{1, 1, 0},
		},
		{
			name: "DOS shape rejected, GUID accepted", dos: fail(errShape), guid: ok(`\\?\Volume{g}\x`), eval: ok(`C:\eval`),
			want: `\\?\Volume{g}\x`, wantCalls: [3]int{1, 1, 0},
		},
		{
			name: "DOS empty, GUID accepted", dos: ok(""), guid: ok(`\\?\Volume{g}\x`), eval: ok(`C:\eval`),
			want: `\\?\Volume{g}\x`, wantCalls: [3]int{1, 1, 0},
		},
		{
			name: "both queries fail, EvalSymlinks accepted", dos: fail(errDOS), guid: fail(errGUID), eval: ok(`C:\eval`),
			want: `C:\eval`, wantCalls: [3]int{1, 1, 1},
		},
		{
			name: "all fail, not-exist preserved", dos: fail(errDOS), guid: fail(errGUID),
			eval:      fail(&fs.PathError{Op: "lstat", Path: `C:\gone`, Err: fs.ErrNotExist}),
			wantCalls: [3]int{1, 1, 1}, wantErrIs: []error{errDOS, errGUID, fs.ErrNotExist},
		},
		{
			name: "all fail, not-exist from a query preserved", dos: fail(fs.ErrNotExist), guid: fail(errGUID), eval: fail(errShape),
			wantCalls: [3]int{1, 1, 1}, wantErrIs: []error{fs.ErrNotExist, errGUID, errShape},
		},
		{
			name: "all empty", dos: ok(""), guid: ok(""), eval: ok(""),
			wantCalls: [3]int{1, 1, 1},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls [3]int
			counting := func(i int, f fake) func() (string, error) {
				return func() (string, error) {
					calls[i]++
					return f.path, f.err
				}
			}
			got, err := resolveParent(counting(0, tc.dos), counting(1, tc.guid), counting(2, tc.eval))
			if calls != tc.wantCalls {
				t.Errorf("calls (dos, guid, eval) = %v, want %v", calls, tc.wantCalls)
			}
			if tc.want != "" {
				if err != nil || got != tc.want {
					t.Fatalf("resolveParent = %q, %v; want %q, nil", got, err, tc.want)
				}
				return
			}
			if err == nil {
				t.Fatalf("resolveParent = %q, nil; want an error", got)
			}
			if got != "" {
				t.Errorf("resolveParent returned path %q alongside error %v; the unresolved path must never be returned", got, err)
			}
			for _, target := range tc.wantErrIs {
				if !errors.Is(err, target) {
					t.Errorf("errors.Is(%v, %v) = false; every cause must be preserved", err, target)
				}
			}
		})
	}
}
