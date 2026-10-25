//go:build !windows

package testbash

import "os/exec"

func resolve() (string, error) {
	return exec.LookPath("bash")
}
