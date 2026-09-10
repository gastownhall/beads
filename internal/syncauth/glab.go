package syncauth

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

var glabTokenRE = regexp.MustCompile(`(?i)token[:=]\s*([A-Za-z0-9_\-\.]+)`)

// glabAuth authenticates using the GitLab CLI (glab).
type glabAuth struct {
	runner
	cfg Config
}

func newGLabAuth(cfg Config) *glabAuth {
	return &glabAuth{cfg: cfg}
}

func (g *glabAuth) Name() Provider { return ProviderGLab }

func (g *glabAuth) Detect(ctx context.Context) (bool, error) {
	return detectCLI(ctx, g.runner, "glab", g.host())
}

func (g *glabAuth) Token(ctx context.Context) (Token, error) {
	path, err := exec.LookPath("glab")
	if err != nil {
		return Token{}, fmt.Errorf("%w: glab not found in PATH", ErrNoAuth)
	}

	args := []string{"auth", "status", "--show-token"}
	if host := g.host(); host != "" && host != "gitlab.com" {
		args = append(args, "--hostname", host)
	}
	out, _, err := g.exec(ctx, path, args...)
	if err != nil {
		return Token{}, fmt.Errorf("glab auth status: %w", err)
	}

	tok, ok := extractGLabToken(string(out))
	if !ok {
		return Token{}, fmt.Errorf("glab auth status did not return a token")
	}

	return Token{
		AccessToken: tok,
		Provider:    ProviderGLab,
		Host:        g.host(),
	}, nil
}

func (g *glabAuth) GitConfigParameter(host string) (string, error) {
	if host == "" {
		host = g.host()
	}
	path, err := exec.LookPath("glab")
	if err != nil {
		return "", fmt.Errorf("glab not found in PATH: %w", err)
	}
	return fmt.Sprintf("!%s auth git-credential", path), nil
}

func extractGLabToken(out string) (string, bool) {
	// glab --show-token output commonly includes a line like:
	//   ✓ Logged in to gitlab.com as user (token: glpat-...)
	for _, line := range strings.Split(out, "\n") {
		m := glabTokenRE.FindStringSubmatch(line)
		if len(m) == 2 && m[1] != "" {
			return m[1], true
		}
	}
	return "", false
}

func (g *glabAuth) host() string {
	if g.cfg.Host == "" {
		return "gitlab.com"
	}
	return normalizeHost(g.cfg.Host)
}
