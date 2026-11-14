package embeddeddolt

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"syscall"
	"testing"
)

// TestClassifyOpenErrorWriteAccess pins the classification GH#7445 asks for:
// an embedded engine open refused because the process may not write the
// store's files (permission denied, or a read-only filesystem) must come back
// as ErrStoreRequiresWriteAccess with guidance that retrying cannot help,
// while keeping the engine's own error reachable; every other error, and nil,
// passes through untouched so no unrelated failure gains that diagnosis.
func TestClassifyOpenErrorWriteAccess(t *testing.T) {
	const dir = "/work/.beads/embeddeddolt"
	journalDenied := &fs.PathError{Op: "open", Path: dir + "/beads/.dolt/noms/vvvv", Err: syscall.EACCES}

	positives := []struct {
		name string
		err  error
	}{
		{"bare EACCES", syscall.EACCES},
		{"bare EPERM", syscall.EPERM},
		{"bare EROFS", syscall.EROFS},
		{"fs.ErrPermission", fs.ErrPermission},
		{"path error", journalDenied},
		{"wrapped path error", fmt.Errorf("open db: %w", journalDenied)},
		{"read-only filesystem path error", &fs.PathError{Op: "open", Path: dir, Err: syscall.EROFS}},
		// errors.Join puts a line break between its causes, which the
		// message escapes like any other control character in a leaf
		// error, so only the denied path error's own text is asserted.
		{"joined with an unrelated error", errors.Join(errors.New("cleanup failed"), journalDenied)},
	}
	for _, tc := range positives {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyOpenError(dir, tc.err)
			if !errors.Is(got, ErrStoreRequiresWriteAccess) {
				t.Fatalf("classifyOpenError(%v) = %v, want an error matching ErrStoreRequiresWriteAccess", tc.err, got)
			}
			if !errors.Is(got, tc.err) {
				t.Fatalf("classifyOpenError(%v) = %v, lost the original error", tc.err, got)
			}
			var errno syscall.Errno
			if !errors.As(tc.err, &errno) && !errors.Is(tc.err, fs.ErrPermission) {
				t.Fatalf("fixture %v carries no errno", tc.err)
			}
			if errors.As(tc.err, &errno) && !errors.Is(got, errno) {
				t.Fatalf("classifyOpenError(%v) = %v, lost errno %v", tc.err, got, errno)
			}
			var pathErr *fs.PathError
			if errors.As(tc.err, &pathErr) {
				var gotPathErr *fs.PathError
				if !errors.As(got, &gotPathErr) || gotPathErr != pathErr {
					t.Fatalf("classifyOpenError(%v) = %v, errors.As no longer reaches the *fs.PathError", tc.err, got)
				}
			}
			msg := got.Error()
			for _, want := range []string{
				dir,
				"require write access even for --readonly",
				"retrying will not help until access changes",
				journalDeniedOrSelf(tc.err, journalDenied).Error(),
			} {
				if !strings.Contains(msg, want) {
					t.Errorf("classifyOpenError(%v) message %q does not contain %q", tc.err, msg, want)
				}
			}
		})
	}

	negatives := []struct {
		name string
		err  error
	}{
		{"nil", nil},
		{"not exist", &fs.PathError{Op: "open", Path: dir, Err: syscall.ENOENT}},
		{"canceled", context.Canceled},
		{"deadline", fmt.Errorf("open: %w", context.DeadlineExceeded)},
		{"lock contention stand-in", errors.New("database \"beads\" is locked by another dolt process")},
		{"permission words in the text only", errors.New("open journal: permission denied")},
	}
	for _, tc := range negatives {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyOpenError(dir, tc.err)
			if got != tc.err {
				t.Fatalf("classifyOpenError(%v) = %v, want the same error back unchanged", tc.err, got)
			}
		})
	}

	t.Run("escapes terminal control characters", func(t *testing.T) {
		hostileDir := "/work/evil\n\x1b[2Jdir"
		cause := &fs.PathError{Op: "open", Path: hostileDir + "/journal\x1b]0;x\a", Err: syscall.EACCES}
		got := classifyOpenError(hostileDir, cause)
		if !errors.Is(got, ErrStoreRequiresWriteAccess) || !errors.Is(got, cause) {
			t.Fatalf("classifyOpenError = %v, want the sentinel and the cause", got)
		}
		if msg := got.Error(); strings.ContainsAny(msg, "\n\x1b\a") {
			t.Fatalf("classifyOpenError message %q carries raw control characters", msg)
		}
	})
}

// journalDeniedOrSelf is the error whose text a classified message must
// repeat verbatim: denied itself when err joins it with another cause, and
// err otherwise.
func journalDeniedOrSelf(err, denied error) error {
	if _, joined := err.(interface{ Unwrap() []error }); joined {
		return denied
	}
	return err
}
