package setup

import (
	"io"
	"os"
)

// ProjectInstallOutput is where the project installers bd init runs
// (InstallClaudeProjectTo, InstallCodexProjectTo, InstallCursorProjectTo)
// report: progress on Stdout, failures on Stderr. A nil writer means the
// process's own stream.
type ProjectInstallOutput struct {
	Stdout io.Writer
	Stderr io.Writer
}

// QuietProjectInstallOutput discards the installers' progress and keeps
// their error lines on stderr, as bd init --quiet does.
func QuietProjectInstallOutput() ProjectInstallOutput {
	return ProjectInstallOutput{Stdout: io.Discard, Stderr: os.Stderr}
}

func (o ProjectInstallOutput) stdout() io.Writer {
	if o.Stdout == nil {
		return os.Stdout
	}
	return o.Stdout
}

func (o ProjectInstallOutput) stderr() io.Writer {
	if o.Stderr == nil {
		return os.Stderr
	}
	return o.Stderr
}
