package doltutil

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestCloseWithTimeout_Success(t *testing.T) {
	err := CloseWithTimeout("test", func() error {
		return nil
	})
	if err != nil {
		t.Errorf("CloseWithTimeout() returned error for successful close: %v", err)
	}
}

func TestCloseWithTimeout_Error(t *testing.T) {
	want := errors.New("close failed")
	err := CloseWithTimeout("test", func() error {
		return want
	})
	if !errors.Is(err, want) {
		t.Errorf("CloseWithTimeout() = %v, want %v", err, want)
	}
}

func TestCloseWithTimeout_Timeout(t *testing.T) {
	deadline := make(chan time.Time)
	releaseClose := make(chan struct{})
	closeExited := make(chan struct{})
	closeStarted := make(chan struct{})
	var closeCalls atomic.Int32

	result := make(chan error, 1)
	go func() {
		result <- closeWithDeadline("slow-db", func() error {
			closeCalls.Add(1)
			close(closeStarted)
			<-releaseClose
			close(closeExited)
			return nil
		}, deadline)
	}()

	<-closeStarted
	t.Cleanup(func() {
		close(releaseClose)
		<-closeExited
	})
	deadline <- time.Now()
	err := <-result
	if err == nil {
		t.Fatal("CloseWithTimeout() should return error on timeout")
	}
	if got, want := err.Error(), "slow-db close timed out after 5s"; got != want {
		t.Errorf("CloseWithTimeout() error = %q, want %q", got, want)
	}
	if got, want := closeCalls.Load(), int32(1); got != want {
		t.Errorf("close function calls = %d, want %d", got, want)
	}
}

func TestCloseTimeout_Value(t *testing.T) {
	if CloseTimeout != 5*time.Second {
		t.Errorf("CloseTimeout = %v, want 5s", CloseTimeout)
	}
}
