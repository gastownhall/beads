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
	// the key entirely: `omitempty` never fires on a struct value, and Codex
	// rejects a hookSpecificOutput object without hookEventName (gh#6039).
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
		return writeCodexHookSystemMessage(stdout, fmt.Sprintf("Beads context check failed before compaction: %s", codexHookPrimeFailureReason(ctx, err)))
	}
	return nil
}

// codexHookPrimeFailureReason renders a prime failure for a hook warning. An
// expired context means prime was cut off by the hook deadline (a parent
// deadline can be shorter than the hook's own 30 seconds, so no exact elapsed
// time is claimed); anything else is summarized by primeErrorSummary so
// captured subprocess output cannot flood the same UI path the warning is
// shown in.
func codexHookPrimeFailureReason(ctx context.Context, err error) string {
	if ctxErr := ctx.Err(); errors.Is(ctxErr, context.DeadlineExceeded) {
		return "bd prime exceeded the hook deadline"
	}
	return primeErrorSummary(err)
}

func codexHookMarkNeedsRefresh(input codexHookInput) error {
	return writeAgentHookMarker(codexHookRefreshMarkerPath(input))
}

func codexHookMaybeRefresh(ctx context.Context, input codexHookInput, stdout io.Writer) error {
	path := codexHookRefreshMarkerPath(input)
	// Consume the marker before attempting the refresh: a prime that fails or
	// is killed by the hook deadline must not leave every later prompt
	// re-running it (gh#6039), and removing up front cannot delete a marker a
	// concurrent PostCompact writes while prime runs. A marker that cannot be
	// removed never starts prime; the next PostCompact re-arms the attempt.
	if err := os.Remove(path); err != nil {
		return nil
	}
	out, err := codexHookExecPrime(ctx, false)
	if err != nil {
		// The marker was already consumed, so this one warning is the only
		// automatic attempt; name the manual recovery and the re-arm event.
		return writeCodexHookSystemMessage(stdout, fmt.Sprintf("Beads context refresh after compaction failed: %s. Run bd prime to reload context; automatic refresh will retry after the next compaction.", codexHookPrimeFailureReason(ctx, err)))
	}
	if strings.TrimSpace(out) == "" {
		return nil
	}
	return writeCodexHookAdditionalContext(stdout, codexHookUserPromptSubmit, out)
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
