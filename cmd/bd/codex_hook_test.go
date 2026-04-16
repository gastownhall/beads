package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFindPendingCodexCompaction(t *testing.T) {
	tests := []struct {
		name       string
		lines      []string
		wantFound  bool
		wantMarker string
	}{
		{
			name: "compaction after last user message",
			lines: []string{
				`{"timestamp":"2026-04-15T21:40:35.145Z","type":"event_msg","payload":{"type":"user_message","message":"hi"}}`,
				`{"timestamp":"2026-04-15T21:40:49.634Z","type":"event_msg","payload":{"type":"context_compacted"}}`,
				`{"timestamp":"2026-04-15T21:40:49.700Z","type":"event_msg","payload":{"type":"task_started"}}`,
			},
			wantFound:  true,
			wantMarker: "2026-04-15T21:40:49.634Z",
		},
		{
			name: "user message after compaction suppresses recovery",
			lines: []string{
				`{"timestamp":"2026-04-15T21:40:35.145Z","type":"event_msg","payload":{"type":"user_message","message":"hi"}}`,
				`{"timestamp":"2026-04-15T21:40:49.634Z","type":"event_msg","payload":{"type":"context_compacted"}}`,
				`{"timestamp":"2026-04-15T21:40:50.675Z","type":"event_msg","payload":{"type":"user_message","message":"yo"}}`,
			},
			wantFound: false,
		},
		{
			name: "prime context after compaction suppresses recovery",
			lines: []string{
				`{"timestamp":"2026-04-15T21:40:35.145Z","type":"event_msg","payload":{"type":"user_message","message":"hi"}}`,
				`{"timestamp":"2026-04-15T21:40:49.634Z","type":"event_msg","payload":{"type":"context_compacted"}}`,
				`{"timestamp":"2026-04-15T21:40:50.000Z","type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"` + beadsPrimeContextHeading + `\n\nalready restored"}]}}`,
			},
			wantFound: false,
		},
		{
			name: "stop hook prompt after compaction suppresses recovery",
			lines: []string{
				`{"timestamp":"2026-04-15T21:40:35.145Z","type":"event_msg","payload":{"type":"user_message","message":"hi"}}`,
				`{"timestamp":"2026-04-15T21:40:49.634Z","type":"event_msg","payload":{"type":"context_compacted"}}`,
				`{"timestamp":"2026-04-15T21:40:50.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"<hook_prompt hook_run_id=\"stop:1\">` + beadsPrimeContextHeading + `</hook_prompt>"}]}}`,
			},
			wantFound: false,
		},
		{
			name: "large later line still scans backward by record",
			lines: []string{
				`{"timestamp":"2026-04-15T21:40:35.145Z","type":"event_msg","payload":{"type":"user_message","message":"hi"}}`,
				`{"timestamp":"2026-04-15T21:40:49.634Z","type":"event_msg","payload":{"type":"context_compacted"}}`,
				`{"timestamp":"2026-04-15T21:40:49.700Z","type":"response_item","payload":{"type":"reasoning","encrypted_content":"` + strings.Repeat("x", codexTranscriptReadChunk+128) + `"}}`,
			},
			wantFound:  true,
			wantMarker: "2026-04-15T21:40:49.634Z",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeCodexTranscript(t, tt.lines)
			marker, found, err := findPendingCodexCompaction(path)
			if err != nil {
				t.Fatalf("findPendingCodexCompaction returned error: %v", err)
			}
			if found != tt.wantFound {
				t.Fatalf("found = %v, want %v", found, tt.wantFound)
			}
			if marker.Timestamp != tt.wantMarker {
				t.Fatalf("timestamp = %q, want %q", marker.Timestamp, tt.wantMarker)
			}
		})
	}
}

func TestRunCodexHookEmitsAdditionalContextOnce(t *testing.T) {
	projectDir := t.TempDir()
	transcriptPath := writeCodexTranscript(t, []string{
		`{"timestamp":"2026-04-15T21:40:35.145Z","type":"event_msg","payload":{"type":"user_message","message":"hi"}}`,
		`{"timestamp":"2026-04-15T21:40:49.634Z","type":"event_msg","payload":{"type":"context_compacted"}}`,
		`{"timestamp":"2026-04-15T21:40:49.700Z","type":"event_msg","payload":{"type":"task_started"}}`,
	})
	restore := stubCodexHookPrimeContext(t, func(opts codexHookOptions) (string, error) {
		if !opts.Stealth {
			t.Fatal("expected stealth option to be forwarded")
		}
		return "# Beads context\n", nil
	})
	defer restore()

	payload := codexHookPayload(projectDir, transcriptPath)
	var out bytes.Buffer
	if err := runCodexHook(strings.NewReader(payload), &out, codexHookOptions{Stealth: true}); err != nil {
		t.Fatalf("runCodexHook returned error: %v", err)
	}

	var hookOutput codexHookOutput
	if err := json.Unmarshal(out.Bytes(), &hookOutput); err != nil {
		t.Fatalf("parse hook output: %v\n%s", err, out.String())
	}
	if hookOutput.HookSpecificOutput == nil {
		t.Fatalf("expected hookSpecificOutput, got %#v", hookOutput)
	}
	if hookOutput.HookSpecificOutput.HookEventName != "UserPromptSubmit" {
		t.Fatalf("hookEventName = %q", hookOutput.HookSpecificOutput.HookEventName)
	}
	if hookOutput.HookSpecificOutput.AdditionalContext != "# Beads context\n" {
		t.Fatalf("additionalContext = %q", hookOutput.HookSpecificOutput.AdditionalContext)
	}

	out.Reset()
	if err := runCodexHook(strings.NewReader(payload), &out, codexHookOptions{Stealth: true}); err != nil {
		t.Fatalf("second runCodexHook returned error: %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("expected duplicate compaction to be quiet, got %q", out.String())
	}
}

func TestRunCodexHookQuietWhenLatestMarkerIsUserInput(t *testing.T) {
	projectDir := t.TempDir()
	transcriptPath := writeCodexTranscript(t, []string{
		`{"timestamp":"2026-04-15T21:40:35.145Z","type":"event_msg","payload":{"type":"user_message","message":"hi"}}`,
		`{"timestamp":"2026-04-15T21:40:49.634Z","type":"event_msg","payload":{"type":"context_compacted"}}`,
		`{"timestamp":"2026-04-15T21:40:50.675Z","type":"event_msg","payload":{"type":"user_message","message":"yo"}}`,
	})
	restore := stubCodexHookPrimeContext(t, func(opts codexHookOptions) (string, error) {
		t.Fatal("prime context should not be generated")
		return "", nil
	})
	defer restore()

	var out bytes.Buffer
	if err := runCodexHook(strings.NewReader(codexHookPayload(projectDir, transcriptPath)), &out, codexHookOptions{}); err != nil {
		t.Fatalf("runCodexHook returned error: %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("expected quiet hook output, got %q", out.String())
	}
}

func writeCodexTranscript(t *testing.T, lines []string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	return path
}

func codexHookPayload(projectDir, transcriptPath string) string {
	data, _ := json.Marshal(codexHookInput{
		HookEventName:  "UserPromptSubmit",
		CWD:            projectDir,
		TranscriptPath: transcriptPath,
		SessionID:      "session",
		TurnID:         "turn",
	})
	return string(data)
}

func stubCodexHookPrimeContext(t *testing.T, fn func(codexHookOptions) (string, error)) func() {
	t.Helper()
	orig := codexHookPrimeContext
	codexHookPrimeContext = fn
	return func() {
		codexHookPrimeContext = orig
	}
}
