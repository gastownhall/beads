package beads

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// GH#5990: several machines can share one beads database while each reaches
// the same repository through its own SSH host alias (git@alias-a:org/repo.git
// on one machine, git@alias-b:org/repo.git on another). The fingerprint used
// to hash the written host verbatim, so every alias spelling produced a
// different id and bd doctor permanently reported a wrong-database error on
// every machine but the one that initialized the database. The helpers in
// this file resolve supported aliases through the local SSH configuration
// before hashing, and fall back to the literal-host fingerprint whenever
// resolution is unavailable, untrusted, or does not apply.

// sshRemoteSplit decomposes an SSH-family git remote into the parts the
// fingerprint hash is built from, so the written host can be replaced by a
// resolved one without disturbing path or explicit-port semantics.
type sshRemoteSplit struct {
	user       string // written username, "" when the remote has none
	host       string // written host as it appears in the remote
	port       string // explicit numeric port from an ssh:// remote, "" when none
	portSuffix string // ":port" per the canonical hash rules, "" when none
	tail       string // canonical path tail, beginning with "/"
}

// splitSSHRemote parses rawURL when it names an SSH transport — scp-style
// [user@]host:path, or ssh://, git+ssh:// and ssh+git:// URLs — and reports
// whether it did. Local paths, Windows paths and non-SSH transports return
// false: only an SSH remote can carry a host alias.
func splitSSHRemote(rawURL string) (sshRemoteSplit, bool) {
	rawURL = strings.TrimSpace(rawURL)

	if strings.Contains(rawURL, "://") {
		u, err := url.Parse(rawURL)
		if err != nil {
			return sshRemoteSplit{}, false
		}
		switch strings.ToLower(u.Scheme) {
		case "ssh", "git+ssh", "ssh+git":
		default:
			return sshRemoteSplit{}, false
		}
		s := sshRemoteSplit{
			user: u.User.Username(),
			host: u.Hostname(),
			port: u.Port(),
		}
		path := strings.TrimRight(u.Path, "/")
		path = strings.TrimSuffix(path, ".git")
		s.tail = filepath.ToSlash(path)
		if s.port != "" && s.port != "22" && s.port != "80" && s.port != "443" {
			s.portSuffix = ":" + s.port
		}
		return s, true
	}

	// Mirror canonicalizeGitURL's scp-style detection: a colon before any
	// slash that is not a Windows drive path.
	colonIdx := strings.Index(rawURL, ":")
	slashIdx := strings.Index(rawURL, "/")
	if colonIdx <= 0 || (slashIdx != -1 && colonIdx > slashIdx) {
		return sshRemoteSplit{}, false
	}
	if colonIdx == 1 && len(rawURL) > 2 && (rawURL[2] == '/' || rawURL[2] == '\\') {
		return sshRemoteSplit{}, false // Windows path
	}
	parts := strings.SplitN(rawURL, ":", 2)
	hostPart, pathPart := parts[0], parts[1]
	s := sshRemoteSplit{}
	if atIdx := strings.LastIndex(hostPart, "@"); atIdx >= 0 {
		s.user = hostPart[:atIdx]
		hostPart = hostPart[atIdx+1:]
	}
	s.host = hostPart
	path := strings.TrimRight(pathPart, "/")
	path = strings.TrimSuffix(path, ".git")
	s.tail = "/" + filepath.ToSlash(path)
	return s, true
}

// legacyCanonicalFromSplit rebuilds the string canonicalizeGitURL hashed for
// this remote before alias resolution: lowercased written host, port suffix
// per the canonical rules, and the trimmed path tail.
func (s sshRemoteSplit) legacyCanonicalFromSplit() string {
	return strings.ToLower(s.host) + s.portSuffix + s.tail
}

// sshHostLookupPolicy says whether and how a written SSH host may be
// resolved through the local SSH configuration.
type sshHostLookupPolicy int

const (
	// sshLookupNone never consults SSH config: the written host is unsafe to
	// pass to a subprocess, or an IP literal with no alias semantics.
	sshLookupNone sshHostLookupPolicy = iota
	// sshLookupAnyResult resolves bare aliases and adopts any valid changed
	// HostName.
	sshLookupAnyResult
)

// safeSSHHostToken is the shape a written host or username must have to be
// passed to the ssh subprocess: no whitespace or shell metacharacters, and
// no leading dash (option injection).
var safeSSHHostToken = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// sshHostLookupPolicyFor classifies a written SSH host for alias lookup.
func sshHostLookupPolicyFor(host string) sshHostLookupPolicy {
	if !safeSSHHostToken.MatchString(host) {
		return sshLookupNone
	}
	if net.ParseIP(host) != nil {
		return sshLookupNone
	}
	return sshLookupAnyResult
}

// sshGLookupTimeout bounds each ssh -G evaluation. Var, not const, so tests
// can shorten it.
var sshGLookupTimeout = 2 * time.Second

// sshGResolveHost asks the local SSH configuration what HostName user@host
// maps to, via `ssh -G`. It returns the raw hostname value ssh printed (not
// yet normalized). Package var as a dependency seam for tests.
var sshGResolveHost = runSSHGResolveHost

func runSSHGResolveHost(user, host, port string) (string, error) {
	sshPath, err := exec.LookPath("ssh")
	if err != nil {
		return "", fmt.Errorf("ssh not found: %w", err)
	}
	if !filepath.IsAbs(sshPath) {
		// Never execute an ssh resolved through a relative PATH entry (the
		// current directory): that would let a repo-local file choose the
		// binary bd runs.
		return "", fmt.Errorf("ssh resolved to non-absolute path %q", sshPath)
	}

	args := []string{"-G"}
	if cfgPath, ok := sshUserConfigPath(); ok {
		// ssh locates its default config through the passwd home, ignoring
		// $HOME; pass the caller's config explicitly so evaluation follows
		// the environment bd sees.
		args = append(args, "-F", cfgPath)
	}
	if port != "" {
		// An explicit URL port participates in config evaluation (Port
		// settings and Match blocks); the hash keeps its own port rules.
		args = append(args, "-p", port)
	}
	args = append(args, "--")
	target := host
	if user != "" {
		target = user + "@" + host
	}
	args = append(args, target)

	ctx, cancel := context.WithTimeout(context.Background(), sshGLookupTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, sshPath, args...)
	// No stdin is supplied and stderr is discarded. WaitDelay bounds Wait
	// even when the child (or a grandchild it spawned) holds the output pipe
	// open past the deadline.
	cmd.WaitDelay = 500 * time.Millisecond
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	raw, ok := parseSSHConfigHostName(out)
	if !ok {
		return "", fmt.Errorf("ssh -G produced no usable hostname for %s", host)
	}
	return raw, nil
}

// sshUserConfigPath returns the ssh config file alias resolution should
// read: $HOME/.ssh/config (or the user's home .ssh/config when HOME is
// unset) when such a regular file exists. ok is false when there is none;
// ssh then reads only its own defaults.
func sshUserConfigPath() (string, bool) {
	home := os.Getenv("HOME")
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil || home == "" {
			return "", false
		}
	}
	if !filepath.IsAbs(home) {
		return "", false
	}
	path := filepath.Join(home, ".ssh", "config")
	if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
		return path, true
	}
	return "", false
}

// parseSSHConfigHostName extracts the hostname ssh -G evaluated for the
// target: the first "hostname" line, key matched case-insensitively with CR
// tolerated. It rejects values that are empty, contain whitespace, or look
// like options rather than hostnames.
func parseSSHConfigHostName(out []byte) (string, bool) {
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimRight(line, "\r")
		idx := strings.IndexAny(line, " \t")
		if idx <= 0 {
			continue
		}
		if !strings.EqualFold(line[:idx], "hostname") {
			continue
		}
		value := strings.TrimSpace(line[idx+1:])
		if value == "" || strings.ContainsAny(value, " \t") || strings.HasPrefix(value, "-") {
			return "", false
		}
		return value, true
	}
	return "", false
}

// validSSHHostName matches a lowercased DNS hostname: dot-separated labels
// that start and end alphanumerically and contain no underscores.
var validSSHHostName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$`)

// normalizeResolvedHost lowercases a resolved HostName, drops one trailing
// dot, and accepts it only as a DNS hostname or IP literal.
func normalizeResolvedHost(raw string) (string, bool) {
	host := strings.ToLower(strings.TrimSuffix(raw, "."))
	if host == "" || len(host) > 253 {
		return "", false
	}
	if net.ParseIP(host) != nil {
		return host, true
	}
	if !validSSHHostName.MatchString(host) {
		return "", false
	}
	return host, true
}

// adoptResolvedHost applies the lookup policy to a normalized resolved
// HostName and reports the host the fingerprint should hash. A HostName
// equal to the written host (case-insensitively) is not an adoption.
func adoptResolvedHost(written, resolved string, policy sshHostLookupPolicy) (string, bool) {
	if resolved == strings.ToLower(written) {
		return "", false
	}
	if policy == sshLookupAnyResult {
		return resolved, true
	}
	return "", false
}

// resolveSSHAliasHost returns the canonical HostName to hash for an SSH
// remote when the local SSH configuration maps its written host somewhere
// else. On any failure — no policy for the host, an unsafe username, no ssh
// binary, a timeout, unusable output — it keeps the legacy literal-host
// fingerprint and reports no alias.
func resolveSSHAliasHost(split sshRemoteSplit) (string, bool) {
	policy := sshHostLookupPolicyFor(split.host)
	if policy == sshLookupNone {
		return "", false
	}
	if split.user != "" && !safeSSHHostToken.MatchString(split.user) {
		// An unsafe username would evaluate different ssh config rules;
		// skip resolution entirely rather than hash a mis-evaluated host.
		return "", false
	}
	raw, err := sshGResolveHost(split.user, split.host, split.port)
	if err != nil {
		return "", false
	}
	resolved, ok := normalizeResolvedHost(raw)
	if !ok {
		return "", false
	}
	return adoptResolvedHost(split.host, resolved, policy)
}
