package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

var (
	runHookCodexMode   bool
	runHookStealthMode bool
)

const (
	codexTranscriptReadChunk = 32 * 1024
	codexRunHookStateFile    = "bd-run-hook-state.json"
)

type codexHookOptions struct {
	Stealth bool
	CWD     string
}

type codexHookInput struct {
	HookEventName  string `json:"hook_event_name"`
	CWD            string `json:"cwd"`
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	TurnID         string `json:"turn_id"`
}

type codexHookOutput struct {
	HookSpecificOutput *codexHookSpecificOutput `json:"hookSpecificOutput,omitempty"`
	SystemMessage      string                   `json:"systemMessage,omitempty"`
}

type codexHookSpecificOutput struct {
	HookEventName     string `json:"hookEventName"`
	AdditionalContext string `json:"additionalContext,omitempty"`
}

type codexTranscriptRecord struct {
	Timestamp string `json:"timestamp"`
	Type      string `json:"type"`
	Payload   struct {
		Type    string                       `json:"type"`
		Role    string                       `json:"role"`
		Content []codexTranscriptContentItem `json:"content"`
	} `json:"payload"`
}

type codexTranscriptContentItem struct {
	Text string `json:"text"`
}

type codexCompactionMarker struct {
	ID        string
	Timestamp string
}

type codexHookState struct {
	ProcessedCompactions map[string]string `json:"processed_compactions,omitempty"`
}

var codexHookPrimeContext = func(opts codexHookOptions) (string, error) {
	if opts.CWD != "" {
		oldCWD, err := os.Getwd()
		if err != nil {
			return "", err
		}
		if err := os.Chdir(opts.CWD); err != nil {
			return "", err
		}
		defer func() { _ = os.Chdir(oldCWD) }()

		loadEnvironment()
		loadServerModeFromConfig()
	}

	var buf bytes.Buffer
	err := runPrime(&buf, primeRunOptions{
		Codex:   true,
		Stealth: opts.Stealth,
	})
	return buf.String(), err
}

var runHookCmd = &cobra.Command{
	Use:     "run-hook",
	GroupID: "advanced",
	Short:   "Run an agent integration hook",
	Hidden:  true,
	Args:    cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		if !runHookCodexMode {
			FatalError("run-hook requires --codex")
		}
		if err := runCodexHook(os.Stdin, os.Stdout, codexHookOptions{
			Stealth: runHookStealthMode,
		}); err != nil {
			_ = writeCodexHookSystemMessage(os.Stdout, fmt.Sprintf("bd Codex hook failed: %v", err))
		}
	},
}

func init() {
	runHookCmd.Flags().BoolVar(&runHookCodexMode, "codex", false, "Read and run a Codex hook payload from stdin")
	runHookCmd.Flags().BoolVar(&runHookStealthMode, "stealth", false, "Stealth mode for generated prime context")
	rootCmd.AddCommand(runHookCmd)
}

func runCodexHook(in io.Reader, out io.Writer, opts codexHookOptions) error {
	var payload codexHookInput
	if err := json.NewDecoder(in).Decode(&payload); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}

	switch payload.HookEventName {
	case "UserPromptSubmit":
		return runCodexUserPromptSubmitHook(payload, out, opts)
	default:
		return nil
	}
}

func runCodexUserPromptSubmitHook(payload codexHookInput, out io.Writer, opts codexHookOptions) error {
	if payload.TranscriptPath == "" {
		return nil
	}

	marker, ok, err := findPendingCodexCompaction(payload.TranscriptPath)
	if err != nil || !ok {
		return nil
	}

	projectDir := codexHookProjectDir(payload)
	statePath := codexHookStatePath(projectDir)
	state := readCodexHookState(statePath)

	if state.ProcessedCompactions[payload.TranscriptPath] == marker.ID {
		return nil
	}

	opts.CWD = projectDir
	context, err := codexHookPrimeContext(opts)
	if err != nil {
		return writeCodexHookSystemMessage(out, fmt.Sprintf("compaction recovery failed: %v", err))
	}
	if strings.TrimSpace(context) == "" {
		return writeCodexHookSystemMessage(out, "compaction recovery produced no context")
	}

	state.ProcessedCompactions[payload.TranscriptPath] = marker.ID
	_ = writeCodexHookState(statePath, state)

	return writeCodexHookAdditionalContext(out, payload.HookEventName, context)
}

func codexHookProjectDir(payload codexHookInput) string {
	if payload.CWD != "" {
		return payload.CWD
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return "."
}

func codexHookStatePath(projectDir string) string {
	return filepath.Join(projectDir, ".codex", codexRunHookStateFile)
}

func readCodexHookState(path string) codexHookState {
	state := codexHookState{
		ProcessedCompactions: make(map[string]string),
	}
	data, err := os.ReadFile(path) // #nosec G304 -- project-local hook state path
	if err != nil {
		return state
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return codexHookState{ProcessedCompactions: make(map[string]string)}
	}
	if state.ProcessedCompactions == nil {
		state.ProcessedCompactions = make(map[string]string)
	}
	return state
}

func writeCodexHookState(path string, state codexHookState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteFile(path, data)
}

func findPendingCodexCompaction(transcriptPath string) (codexCompactionMarker, bool, error) {
	var marker codexCompactionMarker
	err := scanCodexTranscriptBackward(transcriptPath, func(line []byte) (bool, error) {
		var record codexTranscriptRecord
		if err := json.Unmarshal(line, &record); err != nil {
			return false, nil
		}
		if codexTranscriptRecordHasPrimeContext(record) {
			return true, nil
		}
		if record.Type != "event_msg" {
			return false, nil
		}

		switch record.Payload.Type {
		case "user_message":
			return true, nil
		case "context_compacted":
			id := record.Timestamp
			if id == "" {
				id = string(bytes.TrimSpace(line))
			}
			marker = codexCompactionMarker{
				ID:        id,
				Timestamp: record.Timestamp,
			}
			return true, nil
		default:
			return false, nil
		}
	})
	if err != nil {
		return codexCompactionMarker{}, false, err
	}
	return marker, marker.ID != "", nil
}

func codexTranscriptRecordHasPrimeContext(record codexTranscriptRecord) bool {
	if record.Type != "response_item" || record.Payload.Type != "message" {
		return false
	}
	if record.Payload.Role != "developer" && record.Payload.Role != "user" {
		return false
	}
	for _, item := range record.Payload.Content {
		if strings.Contains(item.Text, beadsPrimeContextHeading) {
			return true
		}
	}
	return false
}

func scanCodexTranscriptBackward(path string, visit func(line []byte) (stop bool, err error)) error {
	file, err := os.Open(path) // #nosec G304 -- transcript path is supplied by Codex hook payload
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer func() { _ = file.Close() }()

	info, err := file.Stat()
	if err != nil {
		return err
	}

	offset := info.Size()
	var carry []byte
	for offset > 0 {
		chunkSize := int64(codexTranscriptReadChunk)
		if offset < chunkSize {
			chunkSize = offset
		}
		offset -= chunkSize

		chunk := make([]byte, chunkSize)
		if _, err := file.ReadAt(chunk, offset); err != nil && !errors.Is(err, io.EOF) {
			return err
		}

		block := append(chunk, carry...)
		lines := bytes.Split(block, []byte{'\n'})
		if offset > 0 {
			carry = append(carry[:0], lines[0]...)
			lines = lines[1:]
		} else {
			carry = nil
		}

		for i := len(lines) - 1; i >= 0; i-- {
			line := bytes.TrimSpace(lines[i])
			if len(line) == 0 {
				continue
			}
			stop, err := visit(line)
			if err != nil || stop {
				return err
			}
		}
	}

	if len(bytes.TrimSpace(carry)) > 0 {
		_, err := visit(bytes.TrimSpace(carry))
		return err
	}
	return nil
}

func writeCodexHookAdditionalContext(w io.Writer, eventName string, context string) error {
	return json.NewEncoder(w).Encode(codexHookOutput{
		HookSpecificOutput: &codexHookSpecificOutput{
			HookEventName:     eventName,
			AdditionalContext: context,
		},
	})
}

func writeCodexHookSystemMessage(w io.Writer, message string) error {
	return json.NewEncoder(w).Encode(codexHookOutput{
		SystemMessage: message,
	})
}
