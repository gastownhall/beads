package embeddeddolt

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"syscall"
	"testing"

	"github.com/steveyegge/beads/internal/remedy"
)

// TestClassifyOpenErrorWriteAccess pins classifyOpenError's contract
// (GH#7445): an engine-open error whose errno chain carries a permission
// denial or a read-only filesystem is wrapped with both
// ErrStoreRequiresWriteAccess and the original error, and names the store
// directory and the retry guidance; anything else — including an error whose
// text merely says "permission denied" — passes through unchanged, the same
// error value, so classification never fires on a hunch from a message.
func TestClassifyOpenErrorWriteAccess(t *testing.T) {
	const plainDir = "/work/demo/.beads/embeddeddolt"
	pathErr := &fs.PathError{Op: "open", Path: plainDir + "/demo/.dolt/noms/LOCK", Err: syscall.EACCES}

	cases := []struct {
		name     string
		err      error
		positive bool
	}{
		{"bare EACCES", syscall.EACCES, true},
		{"bare EPERM", syscall.EPERM, true},
		// A read-only filesystem is its own errno, not a permission denial:
		// it must be matched on its own, not required to masquerade as one.
		{"bare EROFS", syscall.EROFS, true},
		{"fs.PathError EACCES", pathErr, true},
		{"wrapped fs.PathError", fmt.Errorf("failed to load database %q: %w", "demo", pathErr), true},
		{"joined causes", errors.Join(errors.New("closing engine"), syscall.EACCES), true},
		{"nil error", nil, false},
		{"ENOENT is not access denial", &fs.PathError{Op: "stat", Path: plainDir, Err: syscall.ENOENT}, false},
		{"context canceled", context.Canceled, false},
		{"deadline exceeded", context.DeadlineExceeded, false},
		{"lock contention stand-in", errors.New("the database is locked (file locked by another dolt process)"), false},
		// Classification is by errno, never by message text: this error is
		// unrelated no matter what its text claims.
		{"text-only permission denied", errors.New("open /etc/master.key: permission denied"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyOpenError(plainDir, tc.err)
			if !tc.positive {
				if got != tc.err {
					t.Errorf("classifyOpenError(%v) = %v; want the same error back unchanged", tc.err, got)
				}
				if got != nil && strings.Contains(got.Error(), "write access") {
					t.Errorf("unrelated error gained the write-access diagnostic: %v", got)
				}
				return
			}
			if got == nil || got == tc.err {
				t.Fatalf("error not classified: %v", got)
			}
			if !errors.Is(got, ErrStoreRequiresWriteAccess) {
				t.Errorf("result does not match ErrStoreRequiresWriteAccess: %v", got)
			}
			if !errors.Is(got, tc.err) {
				t.Errorf("result lost the original error %v: %v", tc.err, got)
			}
			msg := got.Error()
			for _, phrase := range []string{
				"require write access even for --readonly",
				"retrying will not help",
				plainDir,
			} {
				if !strings.Contains(msg, phrase) {
					t.Errorf("message lacks %q: %s", phrase, msg)
				}
			}
		})
	}

	// A store directory and an engine error are values from the filesystem
	// and the engine: a newline or an escape in either must not reach the
	// terminal raw, where it could redraw or split the guidance, while
	// errors.Is still reaches the cause.
	t.Run("escapes control characters in directory and cause", func(t *testing.T) {
		dir := "/tmp/bad\u001bdir\nname"
		cause := &fs.PathError{Op: "open", Path: "/tmp/bad\u001bpath\nx", Err: syscall.EACCES}
		got := classifyOpenError(dir, cause)
		if got == nil || got == cause {
			t.Fatalf("error not classified: %v", got)
		}
		if !errors.Is(got, ErrStoreRequiresWriteAccess) {
			t.Errorf("result does not match ErrStoreRequiresWriteAccess: %v", got)
		}
		if !errors.Is(got, syscall.EACCES) {
			t.Errorf("result lost the errno: %v", got)
		}
		msg := got.Error()
		if strings.ContainsAny(msg, "\n\x1b") {
			t.Errorf("message carries a raw control character: %q", msg)
		}
		if !strings.Contains(msg, remedy.PrintedValue(dir)) {
			t.Errorf("message does not name the (escaped) directory %s: %s", remedy.PrintedValue(dir), msg)
		}
	})
}
