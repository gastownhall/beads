package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

const (
	codexHookSessionStart     = "SessionStart"
	codexHookPreCompact       = "PreCompact"
	codexHookPostCompact      = "PostCompact"
	codexHookUserPromptSubmit = "UserPromptSubmit"
)

var codexHookMarkerDirOverride string

var codexHookExecPrime = func(ctx context.Context, memoriesOnly bool) (string, error) {
	args := []string{"prime"}
	if memoriesOnly {
		args = append(args, "--memories-only")
	}
	cmd := exec.CommandContext(ctx, os.Args[0], args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("bd %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

type codexHookInput struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	CWD            string `json:"cwd"`
	HookEventName  string `json:"hook_event_name"`
	Model          string `json:"model"`
	Trigger        string `json:"trigger"`
}

type codexHookResponse struct {
	Continue           bool                    `json:"continue,omitempty"`
	SystemMessage      string                  `json:"systemMessage,omitempty"`
	HookSpecificOutput codexHookSpecificOutput `json:"hookSpecificOutput,omitempty"`
}

type codexHookSpecificOutput struct {
	HookEventName     string `json:"hookEventName,omitempty"`
	AdditionalContext string `json:"additionalContext,omitempty"`
}

var codexHookCmd = &cobra.Command{
	Use:    "codex-hook <event>",
	Hidden: true,
	Short:  "Run an internal Codex lifecycle hook",
	Args:   cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runCodexHook(cmd.Context(), args[0], os.Stdin, os.Stdout)
	},
}

func init() {
	rootCmd.AddCommand(codexHookCmd)
}

func runCodexHook(ctx context.Context, event string, stdin io.Reader, stdout io.Writer) error {
	var input codexHookInput
	if err := json.NewDecoder(stdin).Decode(&input); err != nil && err != io.EOF {
		return err
	}
	if input.HookEventName != "" {
		event = input.HookEventName
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	switch event {
	case codexHookSessionStart:
		return codexHookInjectPrime(ctx, stdout, codexHookSessionStart)
	case codexHookPreCompact:
		return codexHookPreCompactCheck(ctx, stdout)
	case codexHookPostCompact:
		return codexHookMarkNeedsRefresh(input)
	case codexHookUserPromptSubmit:
		return codexHookMaybeRefresh(ctx, input, stdout)
	default:
		return fmt.Errorf("unsupported Codex hook event %q", event)
	}
}

func codexHookInjectPrime(ctx context.Context, stdout io.Writer, event string) error {
	out, err := codexHookExecPrime(ctx, false)
	if err != nil || strings.TrimSpace(out) == "" {
		return nil
	}
	return writeCodexHookAdditionalContext(stdout, event, out)
}

func codexHookPreCompactCheck(ctx context.Context, stdout io.Writer) error {
	if _, err := codexHookExecPrime(ctx, true); err != nil {
		return writeCodexHookSystemMessage(stdout, fmt.Sprintf("Beads context check failed before compaction: %v", err))
	}
	return nil
}

func codexHookMarkNeedsRefresh(input codexHookInput) error {
	path := codexHookRefreshMarkerPath(input)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil
	}
	return os.WriteFile(path, []byte("1\n"), 0o600) // #nosec G306 -- user-private cache marker
}

func codexHookMaybeRefresh(ctx context.Context, input codexHookInput, stdout io.Writer) error {
	path := codexHookRefreshMarkerPath(input)
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	out, err := codexHookExecPrime(ctx, false)
	if err != nil {
		return writeCodexHookSystemMessage(stdout, fmt.Sprintf("Beads context refresh after compaction failed: %v", err))
	}
	_ = os.Remove(path)
	if strings.TrimSpace(out) == "" {
		return nil
	}
	return writeCodexHookAdditionalContext(stdout, codexHookUserPromptSubmit, out)
}

func codexHookRefreshMarkerPath(input codexHookInput) string {
	base := codexHookMarkerBaseDir()
	sessionID := input.SessionID
	if sessionID == "" {
		sessionID = "unknown-session"
	}
	workspace := input.CWD
	if workspace == "" {
		workspace = "unknown-workspace"
	}
	sum := sha256.Sum256([]byte(sessionID + "\x00" + filepath.Clean(workspace)))
	return filepath.Join(base, hex.EncodeToString(sum[:])+".refresh")
}

func codexHookMarkerBaseDir() string {
	if codexHookMarkerDirOverride != "" {
		return codexHookMarkerDirOverride
	}
	if dir, err := os.UserCacheDir(); err == nil && dir != "" {
		return filepath.Join(dir, "beads", "codex-hooks")
	}
	return filepath.Join(os.TempDir(), "beads-codex-hooks")
}

func writeCodexHookAdditionalContext(stdout io.Writer, event, context string) error {
	return json.NewEncoder(stdout).Encode(codexHookResponse{
		Continue: true,
		HookSpecificOutput: codexHookSpecificOutput{
			HookEventName:     event,
			AdditionalContext: context,
		},
	})
}

func writeCodexHookSystemMessage(stdout io.Writer, message string) error {
	return json.NewEncoder(stdout).Encode(codexHookResponse{
		Continue:      true,
		SystemMessage: message,
	})
}
