package main

import (
	"context"
	"testing"
)

func TestGetRootContext_NilFallback(t *testing.T) {
	// Save original state
	oldRootCtx := rootCtx
	oldCmdCtx := cmdCtx
	defer func() {
		rootCtx = oldRootCtx
		cmdCtx = oldCmdCtx
	}()

	t.Run("returns background when rootCtx is nil", func(t *testing.T) {
		rootCtx = nil
		cmdCtx = &CommandContext{}

		ctx := getRootContext()
		if ctx == nil {
			t.Fatal("getRootContext() returned nil, expected context.Background()")
		}
	})

	t.Run("returns rootCtx when set", func(t *testing.T) {
		expected := context.WithValue(context.Background(), "test", "value")
		rootCtx = expected
		cmdCtx = &CommandContext{}

		ctx := getRootContext()
		if ctx != expected {
			t.Errorf("getRootContext() = %v, want %v", ctx, expected)
		}
	})

	t.Run("returns cmdCtx.RootCtx when globals disabled", func(t *testing.T) {
		expected := context.WithValue(context.Background(), "cmd", "ctx")
		rootCtx = nil
		cmdCtx = &CommandContext{RootCtx: expected}

		ctx := getRootContext()
		if ctx == nil {
			t.Fatal("getRootContext() returned nil")
		}
	})
}
