package git

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

func waitConfigPipeIO(t *testing.T, pipe, event windows.Handle, overlapped *windows.Overlapped, transferred *uint32, err error) {
	t.Helper()
	if err == nil || errors.Is(err, windows.ERROR_PIPE_CONNECTED) {
		return
	}
	require.ErrorIs(t, err, windows.ERROR_IO_PENDING)
	state, err := windows.WaitForSingleObject(event, 5000)
	require.NoError(t, err)
	require.Equal(t, uint32(windows.WAIT_OBJECT_0), state, "config pipe operation timed out")
	require.NoError(t, windows.GetOverlappedResult(pipe, overlapped, transferred, false))
}

func TestGetConfigInFlightCancellation(t *testing.T) {
	ordinary, err := exec.LookPath("git")
	require.NoError(t, err)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(strings.ToUpper(key), "GIT_") {
			t.Setenv(key, "")
			require.NoError(t, os.Unsetenv(key))
		}
	}
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_OPTIONAL_LOCKS", "0")
	cwd := t.TempDir()
	var direct string
	run := func(t *testing.T, cancelCase bool) string {
		name, err := windows.UTF16PtrFromString(`\\.\pipe\beads-config-` + rand.Text())
		require.NoError(t, err)
		pipe, err := windows.CreateNamedPipe(name, windows.PIPE_ACCESS_OUTBOUND|windows.FILE_FLAG_OVERLAPPED|windows.FILE_FLAG_FIRST_PIPE_INSTANCE,
			windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE|windows.PIPE_WAIT|windows.PIPE_REJECT_REMOTE_CLIENTS, 1, 4096, 4096, 0, nil)
		require.NoError(t, err)
		event, err := windows.CreateEvent(nil, 1, 0, nil)
		if err != nil {
			_ = windows.CloseHandle(pipe)
			t.Fatal(err)
		}
		t.Setenv("GIT_CONFIG", windows.UTF16PtrToString(name))
		ctx, cancel := context.WithCancel(t.Context())
		var client windows.Handle
		var pending *windows.Overlapped
		var writeData []byte
		closed, ownedClient := false, false
		closePipe := func() error {
			if closed {
				return nil
			}
			err := windows.CloseHandle(pipe)
			closed = err == nil
			return err
		}
		t.Cleanup(func() {
			cancel()
			if pending != nil {
				cancelErr := windows.CancelIoEx(pipe, pending)
				if !errors.Is(cancelErr, windows.ERROR_NOT_FOUND) {
					assert.NoError(t, cancelErr)
				}
				// Retire the operation before releasing its buffer or handles.
				var transferred uint32
				completionErr := windows.GetOverlappedResult(pipe, pending, &transferred, true)
				if !errors.Is(completionErr, windows.ERROR_OPERATION_ABORTED) {
					assert.NoError(t, completionErr)
				}
			}
			// EOF releases a launcher's reader even if cancel only killed its parent.
			assert.NoError(t, closePipe())
			if client != 0 {
				state, waitErr := windows.WaitForSingleObject(client, 2000)
				assert.NoError(t, waitErr)
				if state == uint32(windows.WAIT_TIMEOUT) && ownedClient {
					assert.NoError(t, windows.TerminateProcess(client, 91))
					state, waitErr = windows.WaitForSingleObject(client, 2000)
					assert.NoError(t, waitErr)
					t.Error("connected Git required forced fixture cleanup")
				}
				assert.Equal(t, uint32(windows.WAIT_OBJECT_0), state, "connected Git must terminate")
				assert.NoError(t, windows.CloseHandle(client))
			}
			assert.NoError(t, windows.CloseHandle(event))
			// The asynchronous write buffer must outlive its pipe operations.
			runtime.KeepAlive(writeData)
		})
		connect := &windows.Overlapped{HEvent: event}
		connectErr := windows.ConnectNamedPipe(pipe, connect)
		if errors.Is(connectErr, windows.ERROR_IO_PENDING) {
			pending = connect
		}
		type configResult struct {
			value string
			found bool
			err   error
		}
		result := make(chan configResult, 1)
		go func() {
			value, found, err := NewGitRepository(cwd).GetConfig(ctx, "beads.cancellation")
			result <- configResult{value, found, err}
		}()
		var transferred, pid uint32
		waitConfigPipeIO(t, pipe, event, connect, &transferred, connectErr)
		pending = nil
		require.NoError(t, windows.GetNamedPipeClientProcessId(pipe, &pid))
		client, err = windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE|windows.PROCESS_TERMINATE, false, pid)
		require.NoError(t, err)
		imageBuffer := make([]uint16, 32768)
		size := uint32(len(imageBuffer))
		require.NoError(t, windows.QueryFullProcessImageName(client, 0, &imageBuffer[0], &size))
		image := windows.UTF16ToString(imageBuffer[:size])
		require.True(t, filepath.IsAbs(image) && strings.EqualFold(filepath.Base(image), "git.exe"), "unexpected connected image %q", image)
		if cancelCase {
			require.True(t, strings.EqualFold(filepath.Clean(image), filepath.Clean(direct)), "connected image %q differs from selected native Git %q", image, direct)
		}
		// This retained handle identifies the Git client of this private input pipe.
		ownedClient = true
		t.Logf("ordinary Git %q; connected native Git %q (PID %d)", ordinary, image, pid)
		require.NoError(t, ctx.Err(), "context must still be live at connection")
		select {
		case got := <-result:
			t.Fatalf("GetConfig returned before release/cancel: %+v", got)
		default:
		}
		if cancelCase {
			cancel() // The input stays open and empty until GetConfig returns.
		} else {
			require.NoError(t, windows.ResetEvent(event))
			write := &windows.Overlapped{HEvent: event}
			writeData = []byte("[beads]\ncancellation = config-pipe-control\n")
			var written uint32
			writeErr := windows.WriteFile(pipe, writeData, &written, write)
			if errors.Is(writeErr, windows.ERROR_IO_PENDING) {
				pending = write
			}
			waitConfigPipeIO(t, pipe, event, write, &written, writeErr)
			pending = nil
			require.Equal(t, uint32(len(writeData)), written)
			require.NoError(t, closePipe())
		}
		var got configResult
		select {
		case got = <-result:
		case <-time.After(5 * time.Second):
			t.Fatal("GetConfig did not return within the connected-input bound")
		}
		state, waitErr := windows.WaitForSingleObject(client, 1000)
		require.NoError(t, waitErr)
		require.Equal(t, uint32(windows.WAIT_OBJECT_0), state)
		if cancelCase {
			require.False(t, closed, "cancellation must precede input EOF")
			require.Empty(t, got.value)
			require.False(t, got.found)
			require.ErrorIs(t, got.err, context.Canceled)
			var exitErr *exec.ExitError
			require.ErrorAs(t, got.err, &exitErr)
			require.Equal(t, 1, exitErr.ExitCode())
			require.Empty(t, strings.TrimSpace(string(exitErr.Stderr)))
			require.False(t, strings.HasSuffix(got.err.Error(), ": "), "empty diagnostic suffix: %q", got.err)
		} else {
			require.NoError(t, got.err)
			require.True(t, got.found)
			require.Equal(t, "config-pipe-control", got.value)
		}
		return image
	}
	if !t.Run("release_control", func(t *testing.T) { direct = run(t, false) }) {
		return
	}
	// The ordinary command may be a launcher. Select the native image that opened
	// the control pipe so cancellation targets the config reader directly.
	t.Setenv("PATH", filepath.Dir(direct)+string(os.PathListSeparator)+os.Getenv("PATH"))
	selected, err := exec.LookPath("git")
	require.NoError(t, err)
	require.True(t, strings.EqualFold(filepath.Clean(selected), filepath.Clean(direct)), "selected Git %q differs from discovered image %q", selected, direct)
	t.Run("connected_cancellation", func(t *testing.T) { run(t, true) })
}
