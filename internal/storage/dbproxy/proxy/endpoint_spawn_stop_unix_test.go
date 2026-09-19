//go:build unix

package proxy

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestShutdownWaitsForStartDelayedBeforeExec(t *testing.T) {
	root := t.TempDir()
	childPath := filepath.Join(root, "exit-child.sh")
	require.NoError(t, os.WriteFile(childPath, []byte("#!/bin/sh\nexit 0\n"), 0o700))

	previousResolve := ResolveExecutable
	previousHook := beforeProxyChildStart
	ResolveExecutable = func() (string, error) { return childPath, nil }
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	beforeProxyChildStart = func() {
		once.Do(func() {
			close(entered)
			<-release
		})
	}
	t.Cleanup(func() {
		ResolveExecutable = previousResolve
		beforeProxyChildStart = previousHook
	})

	startDone := make(chan error, 1)
	go func() {
		_, err := GetCreateDatabaseProxyServerEndpoint(root, externalOpenOpts(root))
		startDone <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("start did not reach injected pre-exec delay")
	}

	stopDone := make(chan error, 1)
	go func() { stopDone <- Shutdown(root) }()
	select {
	case err := <-stopDone:
		t.Fatalf("Shutdown returned during delayed start: %v", err)
	case <-time.After(150 * time.Millisecond):
		// The injected hook, rather than scheduler timing, holds the exact
		// historical release-lock-before-Start window open.
	}

	close(release)
	select {
	case err := <-startDone:
		require.Error(t, err)
		assert.True(t, errors.Is(err, errStartInterrupted), "start error = %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("start did not terminate after concurrent shutdown")
	}
	select {
	case err := <-stopDone:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown did not finish after delayed start resolved")
	}

	assert.NoFileExists(t, filepath.Join(root, spawnMarkerFileName))
}
