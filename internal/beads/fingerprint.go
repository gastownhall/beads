package beads

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
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

// RepoIDSource identifies how a repo fingerprint was derived.
type RepoIDSource string

const (
	// RepoIDSourceRemote means the fingerprint hashes the canonicalized
	// remote.origin.url — the same value on every host that clones the repo.
	RepoIDSourceRemote RepoIDSource = "remote"
	// RepoIDSourcePath means no origin remote was configured and the
	// fingerprint hashes the local repo root path — a host-local value that
	// can never match a remote-derived fingerprint stored by another host.
	RepoIDSourcePath RepoIDSource = "path"
)

// ComputeRepoID generates a unique identifier for this git repository
func ComputeRepoID() (string, error) {
	return ComputeRepoIDForPath("")
}

// ComputeRepoIDForPath generates a unique identifier for the git repository
// rooted at or containing repoPath. An empty repoPath uses the current cwd.
func ComputeRepoIDForPath(repoPath string) (string, error) {
	id, _, err := ComputeRepoIDForPathWithSource(repoPath)
	return id, err
}

// RepoFingerprint is the repository identity bd stamps into repo_id, plus
// how it was derived.
type RepoFingerprint struct {
	// ID is the fingerprint to store and compare.
	ID string
	// Source says whether ID hashes the remote URL or the local repo path.
	Source RepoIDSource
	// LegacyID is the pre-GH#5990 fingerprint of this checkout, which hashed
	// the written SSH alias, when it differs from ID; empty otherwise.
	LegacyID string
	// SSHAlias is the written SSH host when it was looked up as an alias
	// (GH#5990): resolved to SSHHostName through the local SSH configuration,
	// left unresolved for the reason in SSHAliasUnresolved, or a bare host
	// the configuration maps to itself (SSHAliasUnmapped). Empty otherwise.
	SSHAlias string
	// SSHHostName is the HostName the SSH alias resolved to; empty otherwise.
	SSHHostName string
	// SSHAliasUnresolved says why this session could not resolve SSHAlias:
	// ssh -G is missing, failed, timed out or printed no usable answer, or it
	// named a HostName for the alias but Git would use a custom SSH
	// transport. ID then hashes the alias as written, and a session that
	// resolves it computes a different ID. Empty otherwise.
	SSHAliasUnresolved string
	// SSHAliasUnmapped reports that SSHAlias is a bare host (no domain name)
	// that ssh -G maps to no other HostName, as it does both for the server's
	// own name and for an alias this machine's SSH config lacks. ID hashes it
	// as written; a machine whose config maps it to a HostName computes a
	// different ID.
	SSHAliasUnmapped bool
}

// Matches reports whether a stored repo_id identifies this checkout: its
// current fingerprint, or the legacy literal-alias fingerprint a database
// stamped by this checkout before alias resolution still holds. It cannot
// recognize the legacy fingerprint of another clone's alias.
func (f RepoFingerprint) Matches(stored string) bool {
	if stored == "" {
		return false
	}
	return stored == f.ID || (f.LegacyID != "" && stored == f.LegacyID)
}

// AliasResolved reports whether the remote's SSH host alias was replaced by
// its configured HostName.
func (f RepoFingerprint) AliasResolved() bool {
	return f.SSHHostName != ""
}

// AliasUnresolved reports whether ID hashes an SSH host alias as written
// because this session could not resolve it; SSHAliasUnresolved says why.
// Such an ID depends on the session's environment, so it is no evidence
// against a different stored ID and must not replace one unasked.
func (f RepoFingerprint) AliasUnresolved() bool {
	return f.SSHAliasUnresolved != ""
}

// AliasUnmapped reports whether ID hashes a bare SSH host that this
// machine's SSH config maps to no other HostName (SSHAliasUnmapped). It may
// be the server's own name or an alias configured only on other machines, so
// a different stored ID is no proof of another repository, and ID must not
// replace one unasked.
func (f RepoFingerprint) AliasUnmapped() bool {
	return f.SSHAliasUnmapped
}

// AliasUncertainty says why another session or machine may compute a
// different ID for this checkout's remote, as a clause that follows "the
// ID": ID hashes an SSH host alias this session could not resolve
// (AliasUnresolved), or a bare host this machine maps to no other HostName
// (AliasUnmapped). It returns "" when neither applies.
func (f RepoFingerprint) AliasUncertainty() string {
	switch {
	case f.AliasUnresolved():
		return fmt.Sprintf("hashes the SSH host alias %q as written because %s", f.SSHAlias, f.SSHAliasUnresolved)
	case f.AliasUnmapped():
		return fmt.Sprintf("hashes the bare SSH host %q as written because this machine's SSH config maps it to no other HostName", f.SSHAlias)
	}
	return ""
}

// ComputeRepoIDForPathWithSource is ComputeRepoIDForPath plus which derivation
// produced the fingerprint, so callers (bd doctor, bd migrate --update-repo-id)
// can tell a canonical remote-derived fingerprint from the path fallback
// (bd-46vla: on a synced clone without the canonical origin remote, a
// path-fallback mismatch against the stored id is cosmetic, and stamping the
// local value into the versioned metadata table propagates it to every clone).
func ComputeRepoIDForPathWithSource(repoPath string) (string, RepoIDSource, error) {
	fp, err := ComputeRepoFingerprintForPath(repoPath)
	if err != nil {
		return "", "", err
	}
	return fp.ID, fp.Source, nil
}

// ComputeRepoFingerprintForPath computes the repository fingerprint for the
// git repository rooted at or containing repoPath. An empty repoPath uses
// the current cwd.
//
// GH#2867: When running from a git worktree, the path-based fallback (no remote)
// uses the main repository root instead of the worktree root. This ensures all
// worktrees sharing a database produce the same fingerprint. Without this,
// worktree operations would compute a different repo_id and bd doctor would
// report a fingerprint mismatch.
//
// GH#5990: an SSH remote written through a host alias (git@alias-a:org/repo)
// hashes the HostName the local SSH configuration maps the alias to, so
// clones reaching one repository through different aliases agree. An alias
// configured with a port other than 22 keeps its literal identity, and so
// does one this session cannot resolve, as SSHAliasUnresolved then records.
// A bare host the SSH configuration maps to itself keeps it too, marked
// SSHAliasUnmapped: it may be an alias configured only on other machines.
func ComputeRepoFingerprintForPath(repoPath string) (RepoFingerprint, error) {
	output, err := runGitInRepo(repoPath, "config", "--get", "remote.origin.url")
	if err != nil {
		// No remote configured — fall back to path-based fingerprint.
		// Use --git-common-dir to derive the main repo root so that
		// worktrees produce the same fingerprint as the main checkout.
		repoRoot, rootErr := mainRepoRootForPath(repoPath)
		if rootErr != nil {
			return RepoFingerprint{}, fmt.Errorf("not a git repository")
		}

		return RepoFingerprint{ID: hashRepoIdentity(normalizedRepoPath(repoRoot)), Source: RepoIDSourcePath}, nil
	}

	repoURL := strings.TrimSpace(string(output))
	canonical, err := canonicalizeGitURL(repoURL)
	if err != nil {
		return RepoFingerprint{}, fmt.Errorf("failed to canonicalize URL: %w", err)
	}

	fp := RepoFingerprint{ID: hashRepoIdentity(canonical), Source: RepoIDSourceRemote}
	alias := resolveSSHAlias(repoPath, repoURL, canonical)
	switch {
	case alias.canonical != "":
		fp.LegacyID = fp.ID
		fp.ID = hashRepoIdentity(alias.canonical)
		fp.SSHAlias = alias.host
		fp.SSHHostName = alias.hostName
	case alias.unresolved != "":
		fp.SSHAlias = alias.host
		fp.SSHAliasUnresolved = alias.unresolved
	case alias.unmapped:
		fp.SSHAlias = alias.host
		fp.SSHAliasUnmapped = true
	}
	return fp, nil
}

func hashRepoIdentity(canonical string) string {
	hash := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(hash[:16])
}

// Seams for SSH alias resolution, replaced by tests.
var (
	sshLookPath         = exec.LookPath
	sshResolveTimeout   = 2 * time.Second
	sshResolveWaitDelay = 500 * time.Millisecond
	resolveSSHEndpoint  = sshConfigEndpoint
)

// sshEndpoint is the HostName and port ssh -G reports for a destination.
type sshEndpoint struct {
	hostName string
	port     string
}

// sshRemote is the destination part of an SSH remote URL.
type sshRemote struct {
	user string
	host string
	port string
}

// safeSSHToken matches the host and user tokens handed to ssh.
var safeSSHToken = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// parseSSHRemote splits an SSH remote (scp-style [user@]host:path, or an
// ssh://, git+ssh:// or ssh+git:// URL) into its destination. Other
// transports and local paths report false. Host detection mirrors
// canonicalizeGitURL so the written host is a prefix of its output.
func parseSSHRemote(rawURL string) (sshRemote, bool) {
	rawURL = strings.TrimSpace(rawURL)

	if i := strings.Index(rawURL, "://"); i >= 0 {
		switch strings.ToLower(rawURL[:i]) {
		case "ssh", "git+ssh", "ssh+git":
		default:
			return sshRemote{}, false
		}
		u, err := url.Parse(rawURL)
		if err != nil || u.Hostname() == "" {
			return sshRemote{}, false
		}
		remote := sshRemote{host: u.Hostname(), port: u.Port()}
		if u.User != nil {
			remote.user = u.User.Username()
		}
		return remote, true
	}

	colonIdx := strings.Index(rawURL, ":")
	slashIdx := strings.Index(rawURL, "/")
	if colonIdx <= 0 || (slashIdx != -1 && slashIdx < colonIdx) {
		return sshRemote{}, false
	}
	if colonIdx == 1 {
		// A one-letter host is indistinguishable from a Windows drive (C:...).
		return sshRemote{}, false
	}
	hostPart := rawURL[:colonIdx]
	remote := sshRemote{host: hostPart}
	if atIdx := strings.LastIndex(hostPart, "@"); atIdx >= 0 {
		remote.user = hostPart[:atIdx]
		remote.host = hostPart[atIdx+1:]
	}
	return remote, true
}

// sshAlias is what resolving the written host of an SSH remote found.
type sshAlias struct {
	// host is the written host, lowercased, when it was looked up.
	host string
	// canonical is the remote's identity with host replaced by hostName;
	// empty when the legacy identity stays in force.
	canonical string
	hostName  string
	// unresolved says why this session could not resolve host.
	unresolved string
	// unmapped reports that host is a bare host ssh maps to itself.
	unmapped bool
}

// resolveSSHAlias resolves the written host of an SSH remote through the
// local SSH configuration. The legacy identity stays in force, with no
// reason given, when the remote is not SSH, its host is never looked up, or
// ssh maps a domain name to itself, or the host to a HostName it may not
// adopt or to a port other than 22: such a host names the same endpoint in
// every session. A bare host ssh maps to itself keeps the legacy identity
// and is marked unmapped, since it may be an alias this machine does not
// configure. When ssh -G gives no usable answer (ssh is missing, fails,
// times out or prints none), or names a HostName for the alias while Git
// would use a custom SSH transport that answer cannot speak for, the legacy
// identity stays in force and unresolved says why.
func resolveSSHAlias(repoPath, rawURL, legacyCanonical string) sshAlias {
	remote, host, tail, ok := sshAliasCandidate(rawURL, legacyCanonical)
	if !ok {
		return sshAlias{}
	}

	ctx, cancel := context.WithTimeout(context.Background(), sshResolveTimeout)
	defer cancel()
	endpoint, err := resolveSSHEndpoint(ctx, remote.user, remote.host, remote.port)
	if err != nil {
		if ctx.Err() != nil {
			return sshAlias{host: host, unresolved: fmt.Sprintf("ssh -G did not answer within %v", sshResolveTimeout)}
		}
		return sshAlias{host: host, unresolved: err.Error()}
	}
	resolved, ok := normalizeSSHHostName(endpoint.hostName)
	if !ok {
		return sshAlias{host: host, unresolved: fmt.Sprintf("ssh -G printed an unusable hostname %q", endpoint.hostName)}
	}
	if resolved == strings.TrimSuffix(host, ".") {
		// Review (#8212): OpenSSH echoes a host its configuration does not
		// know. A domain name is taken as the server's own name, but a bare
		// host may as well be an alias configured only on other machines,
		// which compute its HostName's identity instead.
		return sshAlias{host: host, unmapped: !isDomainNameShaped(strings.Split(resolved, "."))}
	}
	if !sshHostNameAdoptable(host, resolved) {
		return sshAlias{}
	}
	// A port written in the URL was handed to ssh -G and is already in tail.
	// Without one, an alias whose configuration selects another port names
	// an endpoint its HostName alone does not: the same HostName on another
	// port can be a different server.
	if remote.port == "" && endpoint.port != "22" {
		return sshAlias{}
	}
	// The answer changes the identity, which holds only for the ssh Git runs
	// with its default configuration. Review (#8212): checked only here, so
	// every outcome above is the same with or without a custom transport.
	if setting := customSSHTransport(repoPath); setting != "" {
		return sshAlias{host: host, unresolved: setting + " selects a custom SSH transport"}
	}
	return sshAlias{host: host, canonical: resolved + tail, hostName: resolved}
}

// sshAliasCandidate returns the destination of an SSH remote whose written
// host may be an alias, that host lowercased, and the rest of the legacy
// canonical identity after it. Remotes that are not SSH, and hosts or users
// that are never looked up, report false.
func sshAliasCandidate(rawURL, legacyCanonical string) (remote sshRemote, host, tail string, ok bool) {
	remote, ok = parseSSHRemote(rawURL)
	if !ok {
		return sshRemote{}, "", "", false
	}
	host = strings.ToLower(remote.host)
	if !strings.HasPrefix(legacyCanonical, host) || !sshAliasLookupAllowed(host) {
		return sshRemote{}, "", "", false
	}
	// An unsafe user would be skipped rather than dropped: ssh applies
	// Match user rules, so a different user could select a different host.
	if remote.user != "" && !safeSSHToken.MatchString(remote.user) {
		return sshRemote{}, "", "", false
	}
	return remote, host, legacyCanonical[len(host):], true
}

// sshAliasLookupAllowed decides whether a written (lowercased) host may be
// an alias worth resolving. Unsafe tokens and IP literals never are. A
// two-label domain name (github.com) is taken as the forge's own name: its
// SSH transport may be configured elsewhere (ssh.github.com) without that
// changing which repository the remote names. Bare aliases and domain names
// of three or more labels are looked up.
func sshAliasLookupAllowed(host string) bool {
	if !safeSSHToken.MatchString(host) || net.ParseIP(host) != nil {
		return false
	}
	labels := strings.Split(strings.TrimSuffix(host, "."), ".")
	return !isDomainNameShaped(labels) || len(labels) >= 3
}

// sshHostNameAdoptable decides whether the HostName an alias resolved to may
// replace it in the fingerprint. A bare alias (alias-a, github.com-work)
// takes any valid HostName. A domain name only takes a parent domain it is a
// strict subdomain of (work.github.com -> github.com), never a sibling or
// child transport endpoint. These are heuristics: two-label aliases and
// unrelated dotted aliases keep their literal spelling.
func sshHostNameAdoptable(host, resolved string) bool {
	host = strings.TrimSuffix(host, ".")
	if !isDomainNameShaped(strings.Split(host, ".")) {
		return true
	}
	return strings.Contains(resolved, ".") && net.ParseIP(resolved) == nil &&
		strings.HasSuffix(host, "."+resolved)
}

// isDomainNameShaped reports whether labels look like a fully qualified
// domain name: at least two labels, the last a TLD of two or more ASCII
// letters or an IDNA (xn--) label.
func isDomainNameShaped(labels []string) bool {
	if len(labels) < 2 {
		return false
	}
	tld := labels[len(labels)-1]
	if strings.HasPrefix(tld, "xn--") {
		return true
	}
	if len(tld) < 2 {
		return false
	}
	for _, r := range tld {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') {
			return false
		}
	}
	return true
}

// customSSHTransport names the setting that makes Git reach the remote
// through an SSH command other than ssh with its default configuration,
// whose HostName mapping ssh -G cannot speak for, in Git's order of
// precedence; "" when there is none. GIT_SSH_VARIANT alone does not count:
// it changes only how Git passes options to the plain ssh it still runs.
func customSSHTransport(repoPath string) string {
	if strings.TrimSpace(os.Getenv("GIT_SSH_COMMAND")) != "" {
		return "GIT_SSH_COMMAND"
	}
	if out, err := runGitInRepo(repoPath, "config", "--get", "core.sshCommand"); err == nil && strings.TrimSpace(string(out)) != "" {
		return "core.sshCommand"
	}
	if strings.TrimSpace(os.Getenv("GIT_SSH")) != "" {
		return "GIT_SSH"
	}
	return ""
}

// normalizeSSHHostName validates a HostName printed by ssh -G and returns it
// lowercased without a trailing dot.
func normalizeSSHHostName(hostName string) (string, bool) {
	hostName = strings.TrimSuffix(strings.ToLower(hostName), ".")
	if hostName == "" || len(hostName) > 253 {
		return hostName, false
	}
	if net.ParseIP(hostName) != nil {
		return hostName, true
	}
	return hostName, validSSHHostName.MatchString(hostName)
}

// validSSHHostName matches a lowercased DNS hostname: dot-separated labels
// that start and end alphanumerically and contain no underscores.
var validSSHHostName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$`)

// sshConfigEndpoint asks ssh which HostName and port its configuration gives
// the destination. ssh -G evaluates the configuration without connecting,
// but it is local policy, not a pure function: Match exec runs programs and
// CanonicalizeHostname may consult DNS. The lookup is bounded by ctx and
// WaitDelay; stdin and stderr are not connected, and an ssh found only
// through a relative PATH entry is refused.
func sshConfigEndpoint(ctx context.Context, user, host, port string) (sshEndpoint, error) {
	sshPath, err := sshLookPath("ssh")
	if err != nil {
		return sshEndpoint{}, fmt.Errorf("ssh was not found: %w", err)
	}
	if !filepath.IsAbs(sshPath) {
		return sshEndpoint{}, fmt.Errorf("ssh resolved to relative path %q", sshPath)
	}

	dest := host
	if user != "" {
		dest = user + "@" + host
	}
	args := []string{"-G"}
	if port != "" {
		args = append(args, "-p", port)
	}
	args = append(args, "--", dest)

	cmd := exec.CommandContext(ctx, sshPath, args...)
	cmd.WaitDelay = sshResolveWaitDelay
	out, err := cmd.Output()
	if err != nil {
		return sshEndpoint{}, fmt.Errorf("ssh -G failed: %w", err)
	}
	return parseSSHConfigEndpoint(out)
}

// parseSSHConfigEndpoint returns the values of the first hostname line and
// the first port line of ssh -G output, which OpenSSH always prints.
func parseSSHConfigEndpoint(out []byte) (sshEndpoint, error) {
	var endpoint sshEndpoint
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(strings.TrimRight(line, "\r"))
		if len(fields) == 0 {
			continue
		}
		var value *string
		switch strings.ToLower(fields[0]) {
		case "hostname":
			value = &endpoint.hostName
		case "port":
			value = &endpoint.port
		default:
			continue
		}
		if *value != "" {
			continue
		}
		if len(fields) != 2 {
			return sshEndpoint{}, fmt.Errorf("ssh -G printed a malformed %s line %q", strings.ToLower(fields[0]), line)
		}
		*value = fields[1]
	}
	if endpoint.hostName == "" {
		return sshEndpoint{}, errors.New("ssh -G printed no hostname")
	}
	if endpoint.port == "" {
		return sshEndpoint{}, errors.New("ssh -G printed no port")
	}
	return endpoint, nil
}

func canonicalizeGitURL(rawURL string) (string, error) {
	rawURL = strings.TrimSpace(rawURL)

	if strings.Contains(rawURL, "://") {
		u, err := url.Parse(rawURL)
		if err != nil {
			return "", fmt.Errorf("invalid URL: %w", err)
		}

		host := strings.ToLower(u.Hostname())
		if port := u.Port(); port != "" && port != "22" && port != "80" && port != "443" {
			host = host + ":" + port
		}

		path := strings.TrimRight(u.Path, "/")
		path = strings.TrimSuffix(path, ".git")
		path = filepath.ToSlash(path)

		return host + path, nil
	}

	// Detect scp-style URLs: [user@]host:path
	// Must contain ":" before any "/" and not be a Windows path
	colonIdx := strings.Index(rawURL, ":")
	slashIdx := strings.Index(rawURL, "/")
	if colonIdx > 0 && (slashIdx == -1 || colonIdx < slashIdx) {
		// Could be scp-style or Windows path (C:/)
		// Windows paths have colon at position 1 and are followed by backslash or forward slash
		if colonIdx == 1 && len(rawURL) > 2 && (rawURL[2] == '/' || rawURL[2] == '\\') {
			// Windows path, fall through to local path handling
		} else {
			// scp-style: [user@]host:path
			parts := strings.SplitN(rawURL, ":", 2)
			if len(parts) == 2 {
				hostPart := parts[0]
				pathPart := parts[1]

				atIdx := strings.LastIndex(hostPart, "@")
				if atIdx >= 0 {
					hostPart = hostPart[atIdx+1:]
				}

				host := strings.ToLower(hostPart)
				path := strings.TrimRight(pathPart, "/")
				path = strings.TrimSuffix(path, ".git")
				path = filepath.ToSlash(path)

				return host + "/" + path, nil
			}
		}
	}

	absPath, err := filepath.Abs(rawURL)
	if err != nil {
		absPath = rawURL
	}

	evalPath, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		evalPath = absPath
	}

	return filepath.ToSlash(evalPath), nil
}

// GetCloneID generates a unique ID for this specific clone (not shared with other clones)
func GetCloneID() (string, error) {
	return GetCloneIDForPath("")
}

// GetCloneIDForPath generates a unique ID for the specific clone rooted at or
// containing repoPath. An empty repoPath uses the current cwd.
//
// GH#2867: Uses the main repo root (not worktree root) so that all worktrees
// sharing a database produce the same clone ID.
func GetCloneIDForPath(repoPath string) (string, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return "", fmt.Errorf("failed to get hostname: %w", err)
	}

	repoRoot, err := mainRepoRootForPath(repoPath)
	if err != nil {
		return "", fmt.Errorf("not a git repository: %w", err)
	}

	normalizedPath := normalizedRepoPath(repoRoot)
	hash := sha256.Sum256([]byte(hostname + ":" + normalizedPath))
	return hex.EncodeToString(hash[:8]), nil
}

func runGitInRepo(repoPath string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	if repoPath != "" {
		cmd.Dir = repoPath
	}
	return cmd.Output()
}

// mainRepoRootForPath returns the main repository root for a given path,
// correctly handling git worktrees. For worktrees, this returns the parent
// of --git-common-dir (the main repo root), not --show-toplevel (the
// worktree root). For regular repos, both are equivalent.
//
// GH#2867: This ensures fingerprint computation uses a stable path that is
// the same across all worktrees sharing a database.
func mainRepoRootForPath(repoPath string) (string, error) {
	// Get both toplevel and common-dir in one pass to detect worktrees.
	cmd := exec.Command("git", "rev-parse", "--show-toplevel", "--git-common-dir")
	if repoPath != "" {
		cmd.Dir = repoPath
	}
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("not a git repository: %w", err)
	}

	lines := strings.SplitN(strings.TrimSpace(string(output)), "\n", 2)
	if len(lines) < 2 {
		return "", fmt.Errorf("unexpected git rev-parse output")
	}

	toplevel := strings.TrimSpace(lines[0])
	commonDir := strings.TrimSpace(lines[1])

	// Resolve to absolute paths for comparison.
	absToplevel, err := filepath.Abs(toplevel)
	if err != nil {
		return toplevel, nil
	}

	// commonDir may be relative; resolve relative to the working directory
	// (repoPath if set, else CWD).
	if !filepath.IsAbs(commonDir) {
		base := repoPath
		if base == "" {
			base, _ = os.Getwd()
		}
		commonDir = filepath.Join(base, commonDir)
	}
	absCommonDir, err := filepath.Abs(commonDir)
	if err != nil {
		return absToplevel, nil
	}

	// Derive gitDir path for comparison. In a normal repo, --git-common-dir
	// equals <toplevel>/.git. In a worktree, it points to the main repo's .git.
	absGitDir := filepath.Join(absToplevel, ".git")

	if absCommonDir != absGitDir {
		// Worktree detected: the main repo root is the parent of common-dir.
		return filepath.Dir(absCommonDir), nil
	}

	// Regular repo: toplevel is the main root.
	return absToplevel, nil
}

func normalizedRepoPath(repoPath string) string {
	absPath, err := filepath.Abs(repoPath)
	if err != nil {
		absPath = repoPath
	}

	evalPath, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		evalPath = absPath
	}

	return filepath.ToSlash(evalPath)
}
