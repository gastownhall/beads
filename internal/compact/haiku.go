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
	"github.com/steveyegge/beads/internal/types"
)

const (
	defaultModel   = "claude-3-5-haiku-20241022"
	maxRetries     = 3
	initialBackoff = 1 * time.Second
)

// HaikuClient wraps the Anthropic API for issue summarization.
type HaikuClient struct {
	client         anthropic.Client
	model          anthropic.Model
	tier1Template  *template.Template
	tier2Template  *template.Template
	maxRetries     int
	initialBackoff time.Duration
}

// NewHaikuClient creates a new Haiku API client. Env var ANTHROPIC_API_KEY takes precedence over explicit apiKey.
func NewHaikuClient(apiKey string) (*HaikuClient, error) {
	envKey := os.Getenv("ANTHROPIC_API_KEY")
	if envKey != "" {
		apiKey = envKey
	}
	if apiKey == "" {
		return nil, fmt.Errorf("API key required: set ANTHROPIC_API_KEY environment variable or provide via config")
	}

	client := anthropic.NewClient(option.WithAPIKey(apiKey))

	tier1Tmpl, err := template.New("tier1").Parse(tier1PromptTemplate)
	if err != nil {
		return nil, fmt.Errorf("failed to parse tier1 template: %w", err)
	}

	tier2Tmpl, err := template.New("tier2").Parse(tier2PromptTemplate)
	if err != nil {
		return nil, fmt.Errorf("failed to parse tier2 template: %w", err)
	}

	return &HaikuClient{
		client:         client,
		model:          defaultModel,
		tier1Template:  tier1Tmpl,
		tier2Template:  tier2Tmpl,
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

	return h.callWithRetry(ctx, prompt)
}

// SummarizeTier2 creates an ultra-compressed single-paragraph summary (≤150 words).
func (h *HaikuClient) SummarizeTier2(ctx context.Context, issue *types.Issue) (string, error) {
	prompt, err := h.renderTier2Prompt(issue)
	if err != nil {
		return "", fmt.Errorf("failed to render prompt: %w", err)
	}

	return h.callWithRetry(ctx, prompt)
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
	if errors.As(err, &netErr) && (netErr.Timeout() || netErr.Temporary()) {
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

type tier2Data struct {
	Title              string
	CurrentDescription string
}

func (h *HaikuClient) renderTier2Prompt(issue *types.Issue) (string, error) {
	var buf []byte
	w := &bytesWriter{buf: buf}

	data := tier2Data{
		Title:              issue.Title,
		CurrentDescription: issue.Description,
	}

	if err := h.tier2Template.Execute(w, data); err != nil {
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

const tier1PromptTemplate = `You are summarizing a closed software issue for long-term storage. Compress the following issue into a concise summary that preserves key technical decisions and outcomes.

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

Provide a summary in this exact format:

**Summary:** [2-3 sentences covering what was done and why]

**Key Decisions:** [Bullet points of important technical choices or design decisions]

**Resolution:** [Final outcome and any lasting impact]`

const tier2PromptTemplate = `You are performing ultra-compression on a closed software issue. The issue has already been summarized once. Your task is to create a single concise paragraph (≤150 words) that captures the essence.

**Title:** {{.Title}}

**Current Summary:**
{{.CurrentDescription}}

Provide a single paragraph that covers:
- What was built/fixed
- Why it mattered
- Any lasting impact or decisions

Keep it under 150 words while retaining the most important context.`
