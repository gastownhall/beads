package syncauth

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
)

// GitCredentialOperation handles a `git credential` helper invocation for OAuth.
// It reads the credential request from r and writes the response to w.
func GitCredentialOperation(ctx context.Context, op string, r io.Reader, w io.Writer, kr Keyring) error {
	host, err := parseGitCredentialRequest(r)
	if err != nil {
		return err
	}

	switch op {
	case "get":
		return handleGet(ctx, host, w, kr)
	case "store", "erase":
		// OAuth tokens are managed by bd; ignore store/erase.
		return nil
	default:
		return fmt.Errorf("unknown git credential operation %q", op)
	}
}

// RunGitCredential is a convenience entry point for the `bd github-sync git-credential` command.
func RunGitCredential(ctx context.Context) error {
	if len(os.Args) < 2 {
		return fmt.Errorf("usage: bd github-sync git-credential <operation>")
	}
	op := os.Args[len(os.Args)-1]
	return GitCredentialOperation(ctx, op, os.Stdin, os.Stdout, DefaultKeyring())
}

func parseGitCredentialRequest(r io.Reader) (string, error) {
	host := ""
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			break
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if key == "host" {
			host = value
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	if host == "" {
		return "", fmt.Errorf("git credential request did not include host")
	}
	return host, nil
}

func handleGet(ctx context.Context, host string, w io.Writer, kr Keyring) error {
	oa := newOAuthAuth(Config{Host: host}, kr)
	tok, err := oa.Token(ctx)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(w, "username=oauth2\n")
	_, _ = fmt.Fprintf(w, "password=%s\n", tok.AccessToken)
	_, _ = fmt.Fprintln(w)
	return nil
}
