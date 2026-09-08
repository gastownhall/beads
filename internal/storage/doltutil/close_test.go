package doltutil

import (
	"errors"
	"strings"
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
	err := CloseWithTimeout("slow-db", func() error {
		time.Sleep(CloseTimeout + time.Second)
		return nil
	})
	if err == nil {
		t.Fatal("CloseWithTimeout() should return error on timeout")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("error should mention timeout, got: %v", err)
	}
	if !strings.Contains(err.Error(), "slow-db") {
		t.Errorf("error should mention resource name, got: %v", err)
	}
}

func TestCloseTimeout_Value(t *testing.T) {
	if CloseTimeout != 5*time.Second {
		t.Errorf("CloseTimeout = %v, want 5s", CloseTimeout)
	}
}
