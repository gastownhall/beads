package syncauth

import (
	"context"
	"fmt"
	"os/exec"
)

// ghAuth authenticates using the GitHub CLI (gh).
type ghAuth struct {
	runner
	cfg Config
}

func newGHAuth(cfg Config) *ghAuth {
	return &ghAuth{cfg: cfg}
}

func (g *ghAuth) Name() Provider { return ProviderGH }

func (g *ghAuth) Detect(ctx context.Context) (bool, error) {
	return detectCLI(ctx, g.runner, "gh", g.host())
}

func (g *ghAuth) Token(ctx context.Context) (Token, error) {
	path, err := exec.LookPath("gh")
	if err != nil {
		return Token{}, fmt.Errorf("%w: gh not found in PATH", ErrNoAuth)
	}

	args := []string{"auth", "token"}
	if host := g.host(); host != "" && host != "github.com" {
		args = append(args, "--hostname", host)
	}
	line, err := g.execFirstLine(ctx, path, args...)
	if err != nil {
		return Token{}, fmt.Errorf("gh auth token: %w", err)
	}

	return Token{
		AccessToken: line,
		Provider:    ProviderGH,
		Host:        g.host(),
	}, nil
}

func (g *ghAuth) GitConfigParameter(host string) (string, error) {
	if host == "" {
		host = g.host()
	}
	path, err := exec.LookPath("gh")
	if err != nil {
		return "", fmt.Errorf("gh not found in PATH: %w", err)
	}
	return fmt.Sprintf("!%s auth git-credential", path), nil
}

func (g *ghAuth) host() string {
	if g.cfg.Host == "" {
		return "github.com"
	}
	return normalizeHost(g.cfg.Host)
}
