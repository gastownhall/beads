//go:build !linux

package main

import (
	"context"
	"errors"
)

func enableHistoricalProcessContainment() error {
	return errors.New("historical process containment requires Linux")
}

func runHistoricalCommand(context.Context, string, ...string) ([]byte, error) {
	return nil, errors.New("historical process containment requires Linux")
}

func runHistoricalCommandIn(context.Context, string, string, []string, ...string) ([]byte, error) {
	return nil, errors.New("historical process containment requires Linux")
}
