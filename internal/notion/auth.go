package notion

import (
	"context"
	"os"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
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

func ResolveAuth(ctx context.Context, store storage.Storage) (*ResolvedAuth, error) {
	if store != nil {
		if token, err := store.GetConfig(ctx, configKeyToken); err == nil && strings.TrimSpace(token) != "" {
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
