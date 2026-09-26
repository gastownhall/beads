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
	// AuthSourceDatabaseLegacy marks a token still being read out of the config
	// table, where a bd from before notion.token was yaml-only put it — and from
	// where `bd dolt push` replicated it to every remote this workspace pushed
	// to.
	//
	// It is a SEPARATE source rather than another spelling of
	// AuthSourceConfigToken because it is the only signal that identifies the
	// population GH#6676 tells to rotate. Collapsing the two reports
	// `config_token` for the safe workspace and the leaked one alike, so the
	// users who must act are exactly the ones who cannot tell they must.
	AuthSourceDatabaseLegacy AuthSource = "database_legacy"
	AuthSourceEnv            AuthSource = "env"
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
// notion.token is a yaml-only key, so THE CLI DOOR keeps it out of the
// database: `bd config set notion.token` consults IsYamlOnlyKey before it
// reaches the store and writes config.yaml instead, and `bd config unset`
// clears the file and any row an older bd left behind. That matters because the
// config table's contents are replicated by `bd dolt push`.
//
// ONE DOOR IS STILL OPEN, and this comment deliberately does not claim
// otherwise: the routing lives in the CLI command, not at the write chokepoint,
// so PUT /v0/beads/config/notion.token — `bd serve`, and the dashboard behind
// it — still writes the row with no refusal. That hole is not Notion's; it is
// identical for github.token, jira.api_token, linear.api_key and ado.pat, which
// is why closing it belongs at the role (workapi.ValidateSettingWrite /
// SetSetting), where every tracker credential gets the rule in one change,
// rather than here. Tracked as follow-up to GH#6676.
//
// The database read below therefore remains, both as the upgrade path for a
// workspace configured before the key moved and as the reader for anything that
// door still writes. It reports AuthSourceDatabaseLegacy so `bd notion status`
// can tell that workspace to rotate.
func ResolveAuth(ctx context.Context, reader ConfigReader) (*ResolvedAuth, error) {
	if token := strings.TrimSpace(config.GetString(configKeyToken)); token != "" {
		return &ResolvedAuth{Token: token, Source: AuthSourceConfigToken}, nil
	}

	if reader != nil {
		if token, err := reader.GetConfig(ctx, configKeyToken); err == nil && strings.TrimSpace(token) != "" {
			return &ResolvedAuth{
				Token:  strings.TrimSpace(token),
				Source: AuthSourceDatabaseLegacy,
			}, nil
		}
	}

	if token := strings.TrimSpace(os.Getenv("NOTION_TOKEN")); token != "" {
		return &ResolvedAuth{Token: token, Source: AuthSourceEnv}, nil
	}
	return nil, nil
}
