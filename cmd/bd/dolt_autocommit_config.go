package main

import (
	"fmt"
	"strings"
)

type doltAutoCommitMode string

const (
	doltAutoCommitOff   doltAutoCommitMode = "off"
	doltAutoCommitOn    doltAutoCommitMode = "on"
	doltAutoCommitBatch doltAutoCommitMode = "batch"
)

func getDoltAutoCommitMode() (doltAutoCommitMode, error) {
	mode := strings.TrimSpace(strings.ToLower(doltAutoCommit))
	if mode == "" {
		// Default resolved at store-creation time in main.go based on server mode.
		// If still empty here, fall back to off (safe default).
		mode = string(doltAutoCommitOff)
	}
	switch doltAutoCommitMode(mode) {
	case doltAutoCommitOff:
		return doltAutoCommitOff, nil
	case doltAutoCommitOn:
		return doltAutoCommitOn, nil
	case doltAutoCommitBatch:
		return doltAutoCommitBatch, nil
	default:
		return "", fmt.Errorf("invalid --dolt-auto-commit=%q (valid: off, on, batch)", doltAutoCommit)
	}
}
