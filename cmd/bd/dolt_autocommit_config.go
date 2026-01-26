package main

import (
	"fmt"
	"strings"
)

type doltAutoCommitMode string

const (
	doltAutoCommitOff doltAutoCommitMode = "off"
	doltAutoCommitOn  doltAutoCommitMode = "on"
)

func getDoltAutoCommitMode() (doltAutoCommitMode, error) {
	mode := strings.TrimSpace(strings.ToLower(doltAutoCommit))
	if mode == "" {
		mode = string(doltAutoCommitOn)
	}
	switch doltAutoCommitMode(mode) {
	case doltAutoCommitOff:
		return doltAutoCommitOff, nil
	case doltAutoCommitOn:
		return doltAutoCommitOn, nil
	default:
		return "", fmt.Errorf("invalid --dolt-auto-commit=%q (valid: off, on)", doltAutoCommit)
	}
}
