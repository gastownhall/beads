package main

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/stretchr/testify/require"
)

func TestInitHookConfigWriteCancellation(t *testing.T) {
	binDir := t.TempDir()
	tool := filepath.Join(binDir, "git")
	if runtime.GOOS == "windows" {
		tool += ".exe"
	}
	buildCtx, stopBuild := context.WithTimeout(t.Context(), time.Minute)
	defer stopBuild()
	build := exec.CommandContext(buildCtx, "go", "build", "-o", tool, "./testdata/hook-config-helper")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	out, err := build.CombinedOutput()
	require.NoError(t, err, "build controlled Git process: %s", out)

	for _, cancelWrite := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancel=%t", cancelWrite), func(t *testing.T) {
			selected, _, storage, _ := newInitHooksFixture(t)
			hooks, err := resolveInitHooksContext(selected, storage)
			require.NoError(t, err)
			listener, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
			require.NoError(t, err)
			defer listener.Close()
			require.NoError(t, listener.SetDeadline(time.Now().Add(10*time.Second)))
			marker := filepath.Join(t.TempDir(), "config-write")
			hooks.env = append(hooks.env, "BEADS_HOOK_CONFIG_ADDR="+listener.Addr().String(),
				"BEADS_HOOK_CONFIG_WRITE="+marker)
			t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				// An empty hook selection isolates the reported config-write boundary;
				// adjacent tests cover real Git and installed hook contents.
				done <- (initHooksFileSystem{hooks: hooks}).InstallGitHooks(ctx,
					domain.HooksInstallParams{BeadsHooks: true})
			}()
			var conn net.Conn
			finished := false
			t.Cleanup(func() {
				cancel()
				if conn != nil {
					_, _ = conn.Write([]byte{1}) // Release even a broken uncancelled implementation.
					_ = conn.Close()
				}
				if !finished {
					select {
					case <-done:
					case <-time.After(10 * time.Second):
						t.Error("hook config process did not finish during cleanup")
					}
				}
			})
			conn, err = listener.Accept()
			require.NoError(t, err)
			require.NoError(t, conn.SetDeadline(time.Now().Add(10*time.Second)))
			ready, err := bufio.NewReader(conn).ReadString('\n')
			require.NoError(t, err)
			require.Equal(t, "config ready\n", ready)
			if cancelWrite {
				cancel() // The process has started and is blocked before its write.
			} else {
				_, err = conn.Write([]byte{1})
				require.NoError(t, err)
			}
			select {
			case err = <-done:
				finished = true
			case <-time.After(5 * time.Second):
				t.Fatal("cancellation did not stop the in-flight config process")
			}
			if cancelWrite {
				var exitErr *exec.ExitError
				require.ErrorAs(t, err, &exitErr) // CombinedOutput waited for the killed child.
				_, _ = conn.Write([]byte{1})
				_, statErr := os.Stat(marker)
				require.ErrorIs(t, statErr, os.ErrNotExist, "cancelled process wrote after release")
			} else {
				require.NoError(t, err)
				data, err := os.ReadFile(marker)
				require.NoError(t, err)
				require.Equal(t, "configured", string(data))
			}
		})
	}
}
