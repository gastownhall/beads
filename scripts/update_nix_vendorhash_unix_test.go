//go:build !windows

package scripts_test

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// TestUpdateNixVendorHashInterruptRestoresOriginal signals the script's whole
// process group mid-build, as a terminal Ctrl-C or a cancelled job does. Until
// a build has verified the file it leaves behind, an interrupted run must put
// the original default.nix back: during the probe build default.nix holds the
// placeholder hash, and during the verification build an unverified candidate;
// neither may outlive the run once its private backup is gone. SIGTERM rather
// than SIGINT because a process started with SIGINT ignored (any background
// job of a non-interactive shell) passes that on to the script, and the EXIT
// trap under test runs the same way for both.
func TestUpdateNixVendorHashInterruptRestoresOriginal(t *testing.T) {
	tests := []struct {
		name         string
		plan         string
		blockedBuild int
		wantBuilds   []string
	}{
		{
			name:         "during the probe build",
			plan:         "block",
			blockedBuild: 1,
			wantBuilds:   []string{nixFixturePlaceholder},
		},
		{
			name:         "during the verification build",
			plan:         "mismatch block",
			blockedBuild: 2,
			wantBuilds:   []string{nixFixturePlaceholder, nixFixtureUpdatedHash},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fx := newNixVendorHashFixture(t, test.plan)
			cmd := fx.command()
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			cmd.WaitDelay = 10 * time.Second
			var output bytes.Buffer
			cmd.Stdout = &output
			cmd.Stderr = &output
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			group := -cmd.Process.Pid
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			// stop kills the group unless the script has already been reaped
			// (its group id may be reused after that), then returns the output,
			// which is only safe to read once Wait has returned.
			reaped := false
			stop := func() string {
				if !reaped {
					_ = syscall.Kill(group, syscall.SIGKILL)
					<-done
					reaped = true
				}
				return output.String()
			}
			t.Cleanup(func() { stop() })

			marker := filepath.Join(fx.state, "blocked-"+strconv.Itoa(test.blockedBuild))
			deadline := time.Now().Add(30 * time.Second)
			for {
				if _, err := os.Stat(marker); err == nil {
					break
				}
				select {
				case err := <-done:
					reaped = true
					t.Fatalf("script exited (%v) before nix build %d started\n%s", err, test.blockedBuild, stop())
				default:
				}
				if time.Now().After(deadline) {
					t.Fatalf("nix build %d never started\n%s", test.blockedBuild, stop())
				}
				time.Sleep(10 * time.Millisecond)
			}
			if err := syscall.Kill(group, syscall.SIGTERM); err != nil {
				t.Fatalf("signal the script's process group: %v\n%s", err, stop())
			}

			var err error
			select {
			case err = <-done:
				reaped = true
			case <-time.After(30 * time.Second):
				t.Fatalf("script still running 30s after SIGTERM\n%s", stop())
			}
			// bash runs its EXIT trap, then dies of the signal it caught (or,
			// with a TERM trap of its own, exits 128+15).
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) {
				t.Fatalf("script exited %v after SIGTERM, want it to fail\n%s", err, output.String())
			}
			status, ok := exitErr.Sys().(syscall.WaitStatus)
			killed := ok && status.Signaled() && status.Signal() == syscall.SIGTERM
			if !killed && (!ok || !status.Exited() || status.ExitStatus() != 128+int(syscall.SIGTERM)) {
				t.Errorf("script ended with %v after SIGTERM, want termination by SIGTERM\n%s", exitErr, output.String())
			}
			if got := fx.builds(t); !slices.Equal(got, test.wantBuilds) {
				t.Errorf("nix built vendorHash %q, want %q\n%s", got, test.wantBuilds, output.String())
			}
			fx.assertCheckout(t, nixFixtureOriginalHash)
			for _, name := range fx.tmpEntries(t) {
				if info, err := os.Stat(filepath.Join(fx.tmp, name)); err == nil && info.IsDir() {
					t.Errorf("interrupted run left its private directory %s behind", name)
				}
			}
		})
	}
}
