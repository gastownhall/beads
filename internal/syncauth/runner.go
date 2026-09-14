package syncauth

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"time"
)

// commandRunner shells out to a program. It is swappable in tests.
type commandRunner func(ctx context.Context, name string, args ...string) (stdout, stderr []byte, err error)

// execCommandRunner is the production runner.
func execCommandRunner(ctx context.Context, name string, args ...string) (stdout, stderr []byte, err error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...) // #nosec G204 -- validated program names and args, no shell
	var out, errbuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errbuf
	err = cmd.Run()
	return out.Bytes(), errbuf.Bytes(), err
}

// run is the package-level runner; tests replace it.
var run commandRunner = execCommandRunner

// runner is embedded in providers so tests can override per instance.
type runner struct{ run commandRunner }

func (r runner) exec(ctx context.Context, name string, args ...string) (stdout, stderr []byte, err error) {
	f := r.run
	if f == nil {
		f = run
	}
	return f(ctx, name, args...)
}

func (r runner) execFirstLine(ctx context.Context, name string, args ...string) (string, error) {
	out, _, err := r.exec(ctx, name, args...)
	if err != nil {
		return "", err
	}
	line := bytes.TrimSpace(out)
	if len(line) == 0 {
		return "", fmt.Errorf("%s returned no output", name)
	}
	return string(line), nil
}
