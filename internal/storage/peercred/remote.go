package peercred

import (
	"net/url"
	"regexp"
	"strings"
)

// doltNativeRemotesAPIHost is the host Dolt's DOLT_REMOTE('add') prefixes to
// a native owner/repo remote (env.DefaultRemotesApiHost; the procedure reads
// no remotes-API config).
const doltNativeRemotesAPIHost = "doltremoteapi.dolthub.com"

// nativeRemotePattern is the owner/repo shape recorded as a DoltHub remote:
// a first segment Dolt cannot take for a host (no dot) and a plain repo name.
var nativeRemotePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+/[A-Za-z0-9_][A-Za-z0-9_.-]*$`)

// RemoteURLMatches reports whether live, the URL the database lists for a
// peer's remote, is the destination its credentials were stored for.
//
// stored is the raw remote_url add-peer bound the password to. live passes
// when it is identical, or when it is exactly what DOLT_REMOTE('add') records
// for stored in one of the forms Dolt rewrites: a git remote ending in .git
// (http, https, ssh or file URL, or scp-like [user@]host:path) and a native
// owner/repo remote. Any other difference fails, including host, account or
// path case, port, scheme, query, fragment and a trailing slash or .git: an
// equivalence nobody verified must not let a password reach another server.
func RemoteURLMatches(stored, live string) bool {
	if stored == "" || live == "" {
		return false
	}
	if stored == live {
		return true
	}
	readBack, ok := doltReadBack(stored)
	return ok && readBack == live
}

// doltReadBack returns the URL Dolt records for stored, for the rewritten
// forms RemoteURLMatches accepts. ok is false for every other input,
// including ones Dolt rewrites through the local filesystem or that
// url.URL.String would re-escape.
func doltReadBack(stored string) (string, bool) {
	if strings.TrimSpace(stored) != stored {
		return "", false
	}
	base := stored
	if i := strings.IndexByte(base, '#'); i >= 0 {
		base = base[:i]
	}
	if i := strings.IndexByte(base, '?'); i >= 0 {
		base = base[:i]
	}
	if !strings.HasSuffix(base, ".git") {
		if nativeRemotePattern.MatchString(stored) {
			return "https://" + doltNativeRemotesAPIHost + "/" + stored, true
		}
		return "", false
	}

	if strings.Contains(stored, "://") {
		u, err := url.Parse(stored)
		if err != nil || u.String() != stored {
			return "", false
		}
		switch u.Scheme {
		case "http", "https", "ssh", "file":
			return "git+" + stored, true
		}
		return "", false
	}

	// scp-like [user@]host:path.git, matched as Dolt matches it.
	host, path, found := strings.Cut(stored, ":")
	if !found || host == "" || path == "" || strings.Contains(host, "/") {
		return "", false
	}
	if !strings.Contains(host, ".") && !strings.Contains(host, "@") {
		return "", false
	}
	if !strings.HasPrefix(path, "/") {
		path = "/./" + path
	}
	readBack := "git+ssh://" + host + path
	if u, err := url.Parse(readBack); err != nil || u.String() != readBack {
		return "", false
	}
	return readBack, true
}
