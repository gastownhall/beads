package mcpclient

import (
	"context"
	"fmt"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/steveyegge/beads/internal/notion/state"
)

const NotionMCPURL = "https://mcp.notion.com/mcp"

type Session interface {
	CallTool(ctx context.Context, params *mcp.CallToolParams) (*mcp.CallToolResult, error)
	ListTools(ctx context.Context, params *mcp.ListToolsParams) (*mcp.ListToolsResult, error)
	Close() error
}

type Client struct {
	authStore *state.AuthStore
	endpoint  string
}

func New(authStore *state.AuthStore) *Client {
	return &Client{
		authStore: authStore,
		endpoint:  NotionMCPURL,
	}
}

func (c *Client) Connect(ctx context.Context) (Session, error) {
	httpClient, err := c.httpClient()
	if err != nil {
		return nil, err
	}
	transport := &mcp.StreamableClientTransport{
		Endpoint:             c.endpoint,
		HTTPClient:           httpClient,
		DisableStandaloneSSE: true,
	}
	client := mcp.NewClient(&mcp.Implementation{
		Name:    "bd",
		Version: "0.1.0",
	}, nil)
	return client.Connect(ctx, transport, nil)
}

func (c *Client) httpClient() (*http.Client, error) {
	tokens, err := c.authStore.ReadTokens()
	if err != nil {
		return nil, err
	}
	base := http.DefaultTransport
	if tokens == nil || tokens.AccessToken == "" {
		return &http.Client{Transport: base}, nil
	}
	return &http.Client{
		Transport: &bearerTransport{base: base, token: tokens.AccessToken},
	}, nil
}

type bearerTransport struct {
	base  http.RoundTripper
	token string
}

func (t *bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	if t.token != "" {
		clone.Header.Set("Authorization", fmt.Sprintf("Bearer %s", t.token))
	}
	return t.base.RoundTrip(clone)
}
