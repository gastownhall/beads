package notion

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/steveyegge/beads/internal/notion/mcpclient"
	"github.com/steveyegge/beads/internal/notion/output"
	"github.com/steveyegge/beads/internal/notion/state"
	"github.com/steveyegge/beads/internal/notion/wire"
)

func Logout(io *output.IO, store *state.AuthStore) error {
	if err := store.DeleteAuthFilesOnly(); err != nil {
		return output.Wrap(err, "failed to delete auth files")
	}
	return io.JSON(map[string]string{"status": "logged_out"})
}

func WhoAmI(ctx context.Context, io *output.IO, store *state.AuthStore) error {
	result, err := verifyLiveAuth(ctx, store)
	if err != nil {
		return err
	}
	payload, err := normalizeLiveAuthPayload(result)
	if err != nil {
		return output.Wrap(err, "failed to normalize current user payload")
	}
	return io.JSON(payload)
}

func verifyLiveAuth(ctx context.Context, store *state.AuthStore) (*mcp.CallToolResult, error) {
	ok, err := store.HasTokens()
	if err != nil {
		return nil, output.Wrap(err, "failed to read tokens")
	}
	if !ok {
		return nil, output.NewError(
			"Not authenticated",
			"bdnotion could not find saved Notion credentials",
			"Run \"bdnotion login\" first",
			1,
		)
	}

	client := mcpclient.New(store)
	session, err := client.Connect(ctx)
	if err != nil {
		return nil, output.NewError(
			"Not authenticated",
			"bdnotion could not authenticate against the Notion MCP",
			"Run \"bdnotion login\" again",
			1,
		)
	}
	defer func() { _ = session.Close() }()

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "notion-get-users",
		Arguments: map[string]any{"user_id": "self"},
	})
	if err != nil {
		return nil, output.NewError(
			"Not authenticated",
			"bdnotion could not authenticate against the Notion MCP",
			"Run \"bdnotion login\" again",
			1,
		)
	}
	return result, nil
}

func normalizeLiveAuthPayload(result *mcp.CallToolResult) (map[string]any, error) {
	return wire.ResultJSONMap(result, "notion-get-users self")
}
