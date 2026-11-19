package doltserver

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestNewServerCommandBuildsTheDetachedServer(t *testing.T) {
	dir := t.TempDir()
	logFile, err := os.Create(filepath.Join(dir, "server.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()

	args := []string{"sql-server", "--config", "fixture.yaml"}
	cmd := newServerCommand("dolt-fixture-never-started", dir, args, logFile)

	if cmd.Process != nil {
		t.Fatal("newServerCommand started the process")
	}
	if got, want := cmd.Args, append([]string{"dolt-fixture-never-started"}, args...); !reflect.DeepEqual(got, want) {
		t.Errorf("Args = %q, want %q", got, want)
	}
	if cmd.Dir != dir {
		t.Errorf("Dir = %q, want %q", cmd.Dir, dir)
	}
	if cmd.Stdout != logFile || cmd.Stderr != logFile {
		t.Errorf("Stdout/Stderr = %v/%v, want the log file", cmd.Stdout, cmd.Stderr)
	}
	if cmd.Stdin != nil {
		t.Errorf("Stdin = %v, want nil", cmd.Stdin)
	}
	if cmd.SysProcAttr == nil {
		t.Error("SysProcAttr is nil, want the detached server attrs")
	}
	if got, want := cmd.Env, ServerSpawnEnv(); !reflect.DeepEqual(got, want) {
		t.Errorf("Env differs from ServerSpawnEnv()")
	}
}
