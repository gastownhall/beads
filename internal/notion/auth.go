package notion

import (
	"context"
	"os"
	"strings"

	"github.com/steveyegge/beads/internal/config"
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

// ResolveAuth resolves the Notion token: config.yaml first, then a token left
// in the Dolt database by an older bd, then the NOTION_TOKEN environment
// variable.
//
// notion.token is a yaml-only key, so `bd config set notion.token` writes it to
// config.yaml. It must never be written to the database, whose contents are
// pushed to Dolt remotes. The database read remains only so that a workspace
// configured before the key moved keeps authenticating after an upgrade.
func ResolveAuth(ctx context.Context, reader ConfigReader) (*ResolvedAuth, error) {
	if token := strings.TrimSpace(config.GetString(configKeyToken)); token != "" {
		return &ResolvedAuth{Token: token, Source: AuthSourceConfigToken}, nil
	}

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
