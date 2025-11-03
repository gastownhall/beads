//go:build js && wasm

package main

import (
	"fmt"
	"os"
	"os/exec"
)

// WASM doesn't support signals or process management
var daemonSignals = []os.Signal{}

func configureDaemonProcess(cmd *exec.Cmd) {
	// No-op in WASM
}

func sendStopSignal(process *os.Process) error {
	return fmt.Errorf("daemon operations not supported in WASM")
}

func isReloadSignal(sig os.Signal) bool {
	return false
}

func isProcessRunning(pid int) bool {
	return false
}
