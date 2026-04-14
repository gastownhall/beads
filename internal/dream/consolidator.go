package dream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"os"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/steveyegge/beads/internal/config"
)

const (
	maxRetries     = 3
	initialBackoff = 1 * time.Second
	maxTokens      = 8192
)

// ErrAPIKeyRequired is returned when no Anthropic API key is configured.
var ErrAPIKeyRequired = errors.New("API key required")

// ErrEmptyPlan is returned when the model returns no operations and no summary.
var ErrEmptyPlan = errors.New("model returned no plan")

// ErrUnsafePlan is returned when the proposed plan would delete more than half
// of the memory store. Tripping this guard signals a misbehaving model and the
// pass aborts without applying anything.
var ErrUnsafePlan = errors.New("plan would delete too many memories")

// messagesNewer abstracts the one method this package needs from the Anthropic
// SDK so tests can inject a fake without spinning up an HTTP server.
type messagesNewer interface {
	New(ctx context.Context, body anthropic.MessageNewParams, opts ...option.RequestOption) (*anthropic.Message, error)
}

// Consolidator wraps the Anthropic client with the prompts and retry policy
// used by `bd dream`.
type Consolidator struct {
	api            messagesNewer
	model          string
	maxRetries     int
	initialBackoff time.Duration
}

// New creates a Consolidator. apiKey resolution mirrors internal/compact:
// ANTHROPIC_API_KEY env var > ai.api_key config > the apiKey argument.
// model falls back to config.DefaultAIModel() if empty.
func New(apiKey, model string) (*Consolidator, error) {
	if envKey := os.Getenv("ANTHROPIC_API_KEY"); envKey != "" {
		apiKey = envKey
	} else if cfgKey := config.GetString("ai.api_key"); cfgKey != "" {
		apiKey = cfgKey
	}
	if apiKey == "" {
		return nil, fmt.Errorf("%w: set ANTHROPIC_API_KEY environment variable or ai.api_key in config", ErrAPIKeyRequired)
	}
	if model == "" {
		model = config.DefaultAIModel()
	}
	client := anthropic.NewClient(option.WithAPIKey(apiKey))
	return &Consolidator{
		api:            &client.Messages,
		model:          model,
		maxRetries:     maxRetries,
		initialBackoff: initialBackoff,
	}, nil
}

// Consolidate asks the model to consolidate the provided memories and returns
// the proposed Plan. It does NOT apply the plan; callers are expected to
// inspect or apply it via the storage layer.
//
// Safety: if len(forget operations) > len(memories)/2, returns ErrUnsafePlan.
func (c *Consolidator) Consolidate(ctx context.Context, memories []Memory) (*Plan, error) {
	if len(memories) == 0 {
		return &Plan{Summary: "no memories"}, nil
	}

	userMsg, err := renderUserMessage(memories)
	if err != nil {
		return nil, err
	}

	tool := anthropic.ToolParam{
		Name:        toolName,
		Description: anthropic.String(toolDescription),
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: toolInputSchema()["properties"],
			Required:   []string{"operations", "summary"},
		},
	}

	params := anthropic.MessageNewParams{
		Model:     c.model,
		MaxTokens: maxTokens,
		System: []anthropic.TextBlockParam{
			{Text: systemPrompt},
		},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(userMsg)),
		},
		Tools: []anthropic.ToolUnionParam{
			{OfTool: &tool},
		},
		ToolChoice: anthropic.ToolChoiceUnionParam{
			OfTool: &anthropic.ToolChoiceToolParam{Name: toolName},
		},
	}

	msg, err := c.callWithRetry(ctx, params)
	if err != nil {
		return nil, err
	}

	plan, err := extractPlan(msg)
	if err != nil {
		return nil, err
	}

	if err := safetyCheck(plan, len(memories)); err != nil {
		return nil, err
	}
	return plan, nil
}

// callWithRetry runs the API call with exponential backoff on retryable errors.
// Mirrors internal/compact/haiku.go's retry pattern.
func (c *Consolidator) callWithRetry(ctx context.Context, params anthropic.MessageNewParams) (*anthropic.Message, error) {
	var lastErr error
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if attempt > 0 {
			backoff := c.initialBackoff * time.Duration(math.Pow(2, float64(attempt-1)))
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		msg, err := c.api.New(ctx, params)
		if err == nil {
			return msg, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if !isRetryable(err) {
			return nil, fmt.Errorf("non-retryable error: %w", err)
		}
	}
	return nil, fmt.Errorf("failed after %d retries: %w", c.maxRetries+1, lastErr)
}

// extractPlan pulls the tool_use block out of a message and decodes its input
// into a Plan. The caller is required to set ToolChoice so a tool_use block
// is guaranteed.
func extractPlan(msg *anthropic.Message) (*Plan, error) {
	for _, block := range msg.Content {
		if block.Type != "tool_use" {
			continue
		}
		tu := block.AsToolUse()
		if tu.Name != toolName {
			continue
		}
		var p Plan
		if err := json.Unmarshal([]byte(tu.Input), &p); err != nil {
			return nil, fmt.Errorf("decoding tool input: %w", err)
		}
		if len(p.Operations) == 0 && p.Summary == "" {
			return nil, ErrEmptyPlan
		}
		return &p, nil
	}
	return nil, fmt.Errorf("no %s tool_use block in response", toolName)
}

// safetyCheck rejects plans that would delete more than half of the memories.
// merge operations count toward the deletion total via their absorbed keys.
func safetyCheck(p *Plan, total int) error {
	if total == 0 {
		return nil
	}
	deletes := 0
	for _, op := range p.Operations {
		switch op.Action {
		case ActionForget:
			deletes++
		case ActionMerge:
			deletes += len(op.AbsorbedKeys)
		}
	}
	if deletes*2 > total {
		return fmt.Errorf("%w: would remove %d of %d", ErrUnsafePlan, deletes, total)
	}
	return nil
}

// isRetryable reports whether an Anthropic SDK error is worth retrying.
// Mirrors internal/compact/haiku.go.
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
		s := apiErr.StatusCode
		return s == 429 || s >= 500
	}
	return false
}
