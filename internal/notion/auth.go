package notion

import (
	"context"
	"os"
	"strings"
)

const configKeyToken = "notion.token"

type AuthSource string

const (
	AuthSourceConfigToken AuthSource = "config_token"
	AuthSourceEnv         AuthSource = "env"
)

type ResolvedAuth struct {
	Token  string
	Source AuthSource
}

// ConfigReader reads a Notion configuration value.
type ConfigReader interface {
	GetConfig(ctx context.Context, key string) (string, error)
}

// ResolveAuth resolves a configured Notion token before the environment fallback.
func ResolveAuth(ctx context.Context, reader ConfigReader) (*ResolvedAuth, error) {
	if reader != nil {
		if token, err := reader.GetConfig(ctx, configKeyToken); err == nil && strings.TrimSpace(token) != "" {
			return &ResolvedAuth{
				Token:  strings.TrimSpace(token),
				Source: AuthSourceConfigToken,
			}, nil
		}
	}

	if token := strings.TrimSpace(os.Getenv("NOTION_TOKEN")); token != "" {
		return &ResolvedAuth{Token: token, Source: AuthSourceEnv}, nil
	}
	return nil, nil
}
