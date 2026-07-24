package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// Cursor agent hook event names. These match the keys used in
// .cursor/hooks.json and the "hook_event_name" field Cursor sends on stdin.
// See https://cursor.com/docs/hooks for the full lifecycle.
const (
	cursorHookSessionStart = "sessionStart"
	cursorHookPreCompact   = "preCompact"
	cursorHookPostToolUse  = "postToolUse"
)

// cursorHookMarkerDirOverride lets tests redirect the refresh-marker location.
var cursorHookMarkerDirOverride string

// cursorHookExecPrime runs `bd prime` and returns its output. It is a package
// variable so tests can stub it; it delegates to the shared runBdPrime used by
// codex-hook.
var cursorHookExecPrime = func(ctx context.Context) (string, error) {
	return runBdPrime(ctx)
}

// cursorHookInput captures the subset of Cursor's hook payload we use. Cursor
// sends a common base (conversation_id, hook_event_name, workspace_roots, ...)
// plus event-specific fields. Unknown fields are ignored by the decoder.
type cursorHookInput struct {
	ConversationID string   `json:"conversation_id"`
	SessionID      string   `json:"session_id"`
	HookEventName  string   `json:"hook_event_name"`
	WorkspaceRoots []string `json:"workspace_roots"`
	CWD            string   `json:"cwd"`
	Trigger        string   `json:"trigger"`
	ToolName       string   `json:"tool_name"`
}

// cursorHookResponse is the JSON we write to stdout. All fields are optional;
// an empty struct encodes to "{}", which is the correct no-op response for a
// command hook (e.g. postToolUse when no refresh is pending).
type cursorHookResponse struct {
	Continue          *bool  `json:"continue,omitempty"`
	AdditionalContext string `json:"additional_context,omitempty"`
	UserMessage       string `json:"user_message,omitempty"`
}

var cursorHookCmd = &cobra.Command{
	Use:    "cursor-hook <event>",
	Hidden: true,
	Short:  "Run an internal Cursor agent lifecycle hook",
	Args:   cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runCursorHook(cmd.Context(), args[0], os.Stdin, os.Stdout)
	},
}

func init() {
	rootCmd.AddCommand(cursorHookCmd)
}

func runCursorHook(ctx context.Context, event string, stdin io.Reader, stdout io.Writer) error {
	var input cursorHookInput
	if err := json.NewDecoder(stdin).Decode(&input); err != nil && err != io.EOF {
		return err
	}
	// Cursor sends the canonical event name on stdin; prefer it over the arg so
	// a single misconfigured hooks.json entry can't mislabel the event.
	if input.HookEventName != "" {
		event = input.HookEventName
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	switch event {
	case cursorHookSessionStart:
		return cursorHookHandleSessionStart(ctx, input, stdout)
	case cursorHookPreCompact:
		return cursorHookHandlePreCompact(input, stdout)
	case cursorHookPostToolUse:
		return cursorHookHandlePostToolUse(ctx, input, stdout)
	default:
		return fmt.Errorf("unsupported Cursor hook event %q", event)
	}
}

// cursorHookHandleSessionStart injects full `bd prime` output into the new
// session and clears any stale post-compaction refresh marker.
func cursorHookHandleSessionStart(ctx context.Context, input cursorHookInput, stdout io.Writer) error {
	_ = os.Remove(cursorHookRefreshMarkerPath(input)) // best-effort: drop stale marker

	cont := true
	resp := cursorHookResponse{Continue: &cont}

	out, err := cursorHookExecPrime(ctx)
	if err == nil {
		resp.AdditionalContext = strings.TrimRight(out, "\n")
	}
	return json.NewEncoder(stdout).Encode(resp)
}

// cursorHookHandlePreCompact arms a one-shot marker so the next postToolUse
// re-injects context, and surfaces a short note to the user. Cursor's
// preCompact cannot inject model context directly, so the marker + postToolUse
// pair is how we recover after compaction.
func cursorHookHandlePreCompact(input cursorHookInput, stdout io.Writer) error {
	_ = writeAgentHookMarker(cursorHookRefreshMarkerPath(input)) // best-effort: arm recovery
	return json.NewEncoder(stdout).Encode(cursorHookResponse{
		UserMessage: "Beads: context compacting — bd workflow context will be re-injected on the next tool call (or run `bd prime`).",
	})
}

// cursorHookHandlePostToolUse is a fast no-op on every tool call unless a
// compaction just happened (marker present), in which case it re-injects
// `bd prime` exactly once and clears the marker.
func cursorHookHandlePostToolUse(ctx context.Context, input cursorHookInput, stdout io.Writer) error {
	path := cursorHookRefreshMarkerPath(input)
	if _, err := os.Stat(path); err != nil {
		return json.NewEncoder(stdout).Encode(cursorHookResponse{})
	}

	out, err := cursorHookExecPrime(ctx)
	if err != nil || strings.TrimSpace(out) == "" {
		// Leave the marker in place so a later tool call can retry the refresh.
		return json.NewEncoder(stdout).Encode(cursorHookResponse{})
	}
	_ = os.Remove(path)

	// Set continue=true alongside the injected context, mirroring sessionStart's
	// context-injection response. (The no-op paths above stay as a bare "{}",
	// the documented no-op for a command hook.)
	cont := true
	restored := "[Beads] Context was compacted. Restored bd workflow context below.\n\n" + strings.TrimRight(out, "\n")
	return json.NewEncoder(stdout).Encode(cursorHookResponse{Continue: &cont, AdditionalContext: restored})
}

// cursorHookRefreshMarkerPath derives a per-conversation, per-workspace marker
// path so concurrent Cursor sessions don't clobber each other's state. Cursor
// keys on conversation_id (falling back to session_id) and the first workspace
// root (falling back to cwd); the empty→placeholder fallbacks live in
// agentHookMarkerPath.
func cursorHookRefreshMarkerPath(input cursorHookInput) string {
	conv := input.ConversationID
	if conv == "" {
		conv = input.SessionID
	}

	workspace := ""
	if len(input.WorkspaceRoots) > 0 {
		workspace = input.WorkspaceRoots[0]
	}
	if workspace == "" {
		workspace = input.CWD
	}

	return agentHookMarkerPath(cursorHookMarkerBaseDir(), conv, workspace)
}

func cursorHookMarkerBaseDir() string {
	return agentHookMarkerBaseDir("cursor-hooks", cursorHookMarkerDirOverride)
}
