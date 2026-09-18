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
// expectHost, when non-empty, is the host this helper was configured for —
// requests for any other host (or for non-https protocols) get no answer, so
// the token is only served where it was designed to be used.
func GitCredentialOperation(ctx context.Context, op string, r io.Reader, w io.Writer, kr Keyring, expectHost string) error {
	protocol, host, err := parseGitCredentialRequest(r)
	if err != nil {
		return err
	}

	switch op {
	case "get":
		return handleGet(ctx, protocol, host, w, kr, expectHost)
	case "store", "erase":
		// OAuth tokens are managed by bd; ignore store/erase.
		return nil
	default:
		return fmt.Errorf("unknown git credential operation %q", op)
	}
}

// RunGitCredential is a convenience entry point for the `bd github-sync git-credential` command.
func RunGitCredential(ctx context.Context, op, expectHost string) error {
	return GitCredentialOperation(ctx, op, os.Stdin, os.Stdout, DefaultKeyring(), expectHost)
}

func parseGitCredentialRequest(r io.Reader) (protocol, host string, err error) {
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
		switch key {
		case "protocol":
			protocol = value
		case "host":
			host = value
		}
	}
	if err := scanner.Err(); err != nil {
		return "", "", err
	}
	if host == "" {
		return "", "", fmt.Errorf("git credential request did not include host")
	}
	return protocol, host, nil
}

func handleGet(ctx context.Context, protocol, host string, w io.Writer, kr Keyring, expectHost string) error {
	// Decline requests that are not ours to answer: anything but https, or a
	// host other than the one the helper was configured for. Returning an
	// empty response lets git fall through to the next helper.
	if protocol != "https" || (expectHost != "" && !strings.EqualFold(host, expectHost)) {
		return nil
	}
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
