package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// BeforeWrite synchronously admits one mutation. A missing pre_write hook is a
// no-op; every configured-hook problem denies the write before storage runs.
func (r *Runner) BeforeWrite(ctx context.Context, operation string) error {
	hookPath, configured, err := r.preWriteHookPath()
	if err != nil {
		return r.preWriteError(operation, "configuration", "", err)
	}
	if !configured {
		return nil
	}

	repository, err := r.preWriteRepository()
	if err != nil {
		return r.preWriteError(operation, "configuration", "", err)
	}
	payload, err := json.Marshal(PreWriteRequest{
		Version:    PreWriteProtocolVersion,
		Operation:  operation,
		Repository: repository,
	})
	if err != nil {
		return r.preWriteError(operation, "encode", "", err)
	}

	runCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, hookPath) // #nosec G204 -- validated workspace hook path
	cmd.Dir = repository.Root
	cmd.Env = preWriteEnvironment()
	cmd.Stdin = bytes.NewReader(payload)
	stdout := &boundedBuffer{limit: maxPreWriteOutputBytes}
	stderr := &boundedBuffer{limit: maxPreWriteOutputBytes}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err = runPreWriteCommand(runCtx, cmd)
	if stdout.exceeded || stderr.exceeded {
		return r.preWriteError(operation, "output_limit", "", errors.New("hook output exceeds 32 KiB"))
	}
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		return r.preWriteError(operation, "timeout", "", runCtx.Err())
	}
	if err != nil {
		return r.preWriteError(operation, "execution", boundedText(stderr.String()), err)
	}

	var response PreWriteResponse
	decoder := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&response); err != nil {
		return r.preWriteError(operation, "malformed_response", boundedText(stdout.String()), err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("response contains multiple JSON values")
		}
		return r.preWriteError(operation, "malformed_response", boundedText(stdout.String()), err)
	}
	if response.Allow == nil {
		return r.preWriteError(operation, "malformed_response", "", errors.New(`response is missing "allow"`))
	}
	if !*response.Allow {
		return r.preWriteError(operation, "denied", boundedText(response.Reason), nil)
	}
	return nil
}

func (r *Runner) preWriteError(operation, kind, reason string, err error) error {
	return &PreWriteError{
		Operation: operation,
		Kind:      kind,
		Reason:    reason,
		Err:       err,
	}
}

func (r *Runner) preWriteRepository() (PreWriteRepositoryID, error) {
	beadsDir, err := canonicalPath(filepath.Dir(r.hooksDir))
	if err != nil {
		return PreWriteRepositoryID{}, fmt.Errorf("resolve Beads directory: %w", err)
	}
	root := r.workspaceRoot
	if root == "" {
		root = filepath.Dir(beadsDir)
	}
	root, err = canonicalPath(root)
	if err != nil {
		return PreWriteRepositoryID{}, fmt.Errorf("resolve workspace root: %w", err)
	}
	return PreWriteRepositoryID{Root: root, BeadsDir: beadsDir}, nil
}

func (r *Runner) preWriteHookPath() (string, bool, error) {
	names := []string{HookPreWrite}
	if runtime.GOOS == "windows" {
		names = []string{HookPreWrite + ".exe", HookPreWrite + ".cmd", HookPreWrite + ".bat"}
	}

	var found []string
	for _, name := range names {
		path := filepath.Join(r.hooksDir, name)
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", false, err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return "", false, fmt.Errorf("%s must be a regular file, not a symlink or directory", path)
		}
		if runtime.GOOS != "windows" && info.Mode()&0111 == 0 {
			return "", false, fmt.Errorf("%s is not executable", path)
		}
		found = append(found, path)
	}
	if len(found) == 0 {
		return "", false, nil
	}
	if len(found) != 1 {
		return "", false, fmt.Errorf("multiple pre-write hooks configured: %s", strings.Join(found, ", "))
	}

	hooksDir, err := canonicalPath(r.hooksDir)
	if err != nil {
		return "", false, err
	}
	hookPath, err := canonicalPath(found[0])
	if err != nil {
		return "", false, err
	}
	rel, err := filepath.Rel(hooksDir, hookPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", false, fmt.Errorf("pre-write hook resolves outside %s", hooksDir)
	}
	return hookPath, true, nil
}

func canonicalPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	return filepath.Clean(resolved), nil
}

func preWriteEnvironment() []string {
	env := []string{"PATH=" + os.Getenv("PATH")}
	if runtime.GOOS == "windows" {
		for _, key := range []string{"SystemRoot", "ComSpec"} {
			if value := os.Getenv(key); value != "" {
				env = append(env, key+"="+value)
			}
		}
	}
	return env
}

func boundedText(value string) string {
	return strings.TrimSpace(value)
}

type boundedBuffer struct {
	limit    int
	buffer   bytes.Buffer
	exceeded bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	remaining := b.limit - b.buffer.Len()
	if remaining <= 0 {
		b.exceeded = true
		return len(p), nil
	}
	if len(p) > remaining {
		_, _ = b.buffer.Write(p[:remaining])
		b.exceeded = true
		return len(p), nil
	}
	return b.buffer.Write(p)
}

func (b *boundedBuffer) Bytes() []byte { return b.buffer.Bytes() }

func (b *boundedBuffer) String() string { return b.buffer.String() }
