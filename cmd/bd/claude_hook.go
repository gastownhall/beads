package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"
)

const claudeHookUserPromptSubmit = "UserPromptSubmit"

type claudeHookInput struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	CWD            string `json:"cwd"`
	HookEventName  string `json:"hook_event_name"`
	Prompt         string `json:"prompt"`
}

var claudeHookCmd = &cobra.Command{
	Use:    "claude-hook <event>",
	Hidden: true,
	Short:  "Run an internal Claude Code lifecycle hook",
	Args:   cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runClaudeHook(cmd.Context(), args[0], os.Stdin, os.Stdout)
	},
}

func init() {
	rootCmd.AddCommand(claudeHookCmd)
}

func runClaudeHook(ctx context.Context, event string, stdin io.Reader, stdout io.Writer) error {
	var input claudeHookInput
	if err := json.NewDecoder(stdin).Decode(&input); err != nil && err != io.EOF {
		return err
	}
	if input.HookEventName != "" {
		event = input.HookEventName
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	switch event {
	case claudeHookUserPromptSubmit:
		return claudeHookHandleUserPromptSubmit(ctx, input, stdout)
	default:
		return fmt.Errorf("unsupported Claude hook event %q", event)
	}
}

func claudeHookHandleUserPromptSubmit(ctx context.Context, input claudeHookInput, stdout io.Writer) error {
	_ = recordSteeringOnOwningBead(ctx, steeringReceipt{
		Provider: "claude",
		Event:    claudeHookUserPromptSubmit,
		ThreadID: input.SessionID,
	}, input.Prompt)
	return json.NewEncoder(stdout).Encode(map[string]any{})
}
