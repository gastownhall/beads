// Package compact provides AI-powered issue compaction using Claude Haiku.
package compact

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"os"
	"text/template"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/steveyegge/beads/internal/audit"
	"github.com/steveyegge/beads/internal/types"
)

const (
	defaultModel   = "claude-3-5-haiku-20241022"
	maxRetries     = 3
	initialBackoff = 1 * time.Second
)

// ErrAPIKeyRequired is returned when an API key is needed but not provided.
var ErrAPIKeyRequired = errors.New("API key required")

// HaikuClient wraps the Anthropic API for issue summarization.
type HaikuClient struct {
	client         anthropic.Client
	model          anthropic.Model
	tier1Template  *template.Template
	maxRetries     int
	initialBackoff time.Duration
	auditEnabled   bool
	auditActor     string
}

// NewHaikuClient creates a new Haiku API client. Env var ANTHROPIC_API_KEY takes precedence over explicit apiKey.
func NewHaikuClient(apiKey string) (*HaikuClient, error) {
	envKey := os.Getenv("ANTHROPIC_API_KEY")
	if envKey != "" {
		apiKey = envKey
	}
	if apiKey == "" {
		return nil, fmt.Errorf("%w: set ANTHROPIC_API_KEY environment variable or provide via config", ErrAPIKeyRequired)
	}

	client := anthropic.NewClient(option.WithAPIKey(apiKey))

	tier1Tmpl, err := template.New("tier1").Parse(tier1PromptTemplate)
	if err != nil {
		return nil, fmt.Errorf("failed to parse tier1 template: %w", err)
	}

	return &HaikuClient{
		client:         client,
		model:          defaultModel,
		tier1Template:  tier1Tmpl,
		maxRetries:     maxRetries,
		initialBackoff: initialBackoff,
	}, nil
}

// SummarizeTier1 creates a structured summary of an issue (Summary, Key Decisions, Resolution).
func (h *HaikuClient) SummarizeTier1(ctx context.Context, issue *types.Issue) (string, error) {
	prompt, err := h.renderTier1Prompt(issue)
	if err != nil {
		return "", fmt.Errorf("failed to render prompt: %w", err)
	}

	resp, callErr := h.callWithRetry(ctx, prompt)
	if h.auditEnabled {
		// Best-effort: never fail compaction because audit logging failed.
		e := &audit.Entry{
			Kind:     "llm_call",
			Actor:    h.auditActor,
			IssueID:  issue.ID,
			Model:    string(h.model),
			Prompt:   prompt,
			Response: resp,
		}
		if callErr != nil {
			e.Error = callErr.Error()
		}
		_, _ = audit.Append(e)
	}
	return resp, callErr
}

func (h *HaikuClient) callWithRetry(ctx context.Context, prompt string) (string, error) {
	var lastErr error
	params := anthropic.MessageNewParams{
		Model:     h.model,
		MaxTokens: 1024,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(prompt)),
		},
	}

	for attempt := 0; attempt <= h.maxRetries; attempt++ {
		if attempt > 0 {
			backoff := h.initialBackoff * time.Duration(math.Pow(2, float64(attempt-1)))
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}

		message, err := h.client.Messages.New(ctx, params)

		if err == nil {
			if len(message.Content) > 0 {
				content := message.Content[0]
				if content.Type == "text" {
					return content.Text, nil
				}
				return "", fmt.Errorf("unexpected response format: not a text block (type=%s)", content.Type)
			}
			return "", fmt.Errorf("unexpected response format: no content blocks")
		}

		lastErr = err

		if ctx.Err() != nil {
			return "", ctx.Err()
		}

		if !isRetryable(err) {
			return "", fmt.Errorf("non-retryable error: %w", err)
		}
	}

	return "", fmt.Errorf("failed after %d retries: %w", h.maxRetries+1, lastErr)
}

func isRetryable(err error) bool {
	if err == nil {
		return false
	}

	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}

	var apiErr *anthropic.Error
	if errors.As(err, &apiErr) {
		statusCode := apiErr.StatusCode
		if statusCode == 429 || statusCode >= 500 {
			return true
		}
		return false
	}

	return false
}

type tier1Data struct {
	Title              string
	Description        string
	Design             string
	AcceptanceCriteria string
	Notes              string
}

func (h *HaikuClient) renderTier1Prompt(issue *types.Issue) (string, error) {
	var buf []byte
	w := &bytesWriter{buf: buf}

	data := tier1Data{
		Title:              issue.Title,
		Description:        issue.Description,
		Design:             issue.Design,
		AcceptanceCriteria: issue.AcceptanceCriteria,
		Notes:              issue.Notes,
	}

	if err := h.tier1Template.Execute(w, data); err != nil {
		return "", err
	}
	return string(w.buf), nil
}

type bytesWriter struct {
	buf []byte
}

func (w *bytesWriter) Write(p []byte) (n int, err error) {
	w.buf = append(w.buf, p...)
	return len(p), nil
}

const tier1PromptTemplate = `You are summarizing a closed software issue for long-term storage. Your goal is to COMPRESS the content - the output MUST be significantly shorter than the input while preserving key technical decisions and outcomes.

**Title:** {{.Title}}

**Description:**
{{.Description}}

{{if .Design}}**Design:**
{{.Design}}
{{end}}

{{if .AcceptanceCriteria}}**Acceptance Criteria:**
{{.AcceptanceCriteria}}
{{end}}

{{if .Notes}}**Notes:**
{{.Notes}}
{{end}}

IMPORTANT: Your summary must be shorter than the original. Be concise and eliminate redundancy.

Provide a summary in this exact format:

**Summary:** [2-3 concise sentences covering what was done and why]

**Key Decisions:** [Brief bullet points of only the most important technical choices]

**Resolution:** [One sentence on final outcome and lasting impact]`
