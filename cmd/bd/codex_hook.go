package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
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

// codexHookRefreshRecovery ends a failed post-compaction refresh warning: the
// marker is consumed before the attempt, so nothing retries until the next
// compaction re-arms it.
const codexHookRefreshRecovery = "Run bd prime to reload context; automatic refresh will retry after the next compaction."

var codexHookMarkerDirOverride string

var codexHookExecPrime = func(ctx context.Context, memoriesOnly bool) (string, error) {
	if memoriesOnly {
		return runBdPrime(ctx, "--memories-only")
	}
	return runBdPrime(ctx)
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
	Continue      bool   `json:"continue,omitempty"`
	SystemMessage string `json:"systemMessage,omitempty"`
	// HookSpecificOutput is a pointer so system-message-only responses omit
	// it: Codex rejects an event-specific object without hookEventName, and
	// omitempty never drops a value struct.
	HookSpecificOutput *codexHookSpecificOutput `json:"hookSpecificOutput,omitempty"`
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
		return writeCodexHookSystemMessage(stdout, "Beads context check failed before compaction: "+codexHookFailureReason(ctx, err))
	}
	return nil
}

func codexHookMarkNeedsRefresh(input codexHookInput) error {
	return writeAgentHookMarker(codexHookRefreshMarkerPath(input))
}

func codexHookMaybeRefresh(ctx context.Context, input codexHookInput, stdout io.Writer) error {
	// Consume the marker before the attempt: a failed or killed prime must
	// not leave every later prompt retrying it, and a marker a PostCompact
	// writes while prime runs must survive for the next prompt. A marker that
	// is absent or cannot be removed starts no refresh.
	if err := os.Remove(codexHookRefreshMarkerPath(input)); err != nil {
		return nil
	}
	out, err := codexHookExecPrime(ctx, false)
	if err != nil {
		return writeCodexHookSystemMessage(stdout, fmt.Sprintf("Beads context refresh after compaction failed: %s. %s", codexHookFailureReason(ctx, err), codexHookRefreshRecovery))
	}
	if strings.TrimSpace(out) == "" {
		return nil
	}
	return writeCodexHookAdditionalContext(stdout, codexHookUserPromptSubmit, out)
}

// codexHookFailureReason is the one-line reason a warning quotes. A prime
// killed at the hook deadline gets a plain explanation; any other error is
// reduced to its first line, since runBdPrime appends the child's combined
// output. The deadline wording names no duration: a parent context can expire
// before the hook's own timeout.
func codexHookFailureReason(ctx context.Context, err error) string {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "bd prime exceeded the hook deadline"
	}
	return primeErrorSummary(err)
}

func codexHookRefreshMarkerPath(input codexHookInput) string {
	return agentHookMarkerPath(codexHookMarkerBaseDir(), input.SessionID, input.CWD)
}

func codexHookMarkerBaseDir() string {
	return agentHookMarkerBaseDir("codex-hooks", codexHookMarkerDirOverride)
}

func writeCodexHookAdditionalContext(stdout io.Writer, event, context string) error {
	return json.NewEncoder(stdout).Encode(codexHookResponse{
		Continue: true,
		HookSpecificOutput: &codexHookSpecificOutput{
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
