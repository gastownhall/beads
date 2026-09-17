package doctor

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var latestGitHubReleaseFetcher = fetchLatestGitHubRelease

// CheckCLIVersion checks if the CLI version is up to date.
// Takes cliVersion parameter since it can't access the Version variable from main package.
func CheckCLIVersion(cliVersion string) DoctorCheck {
	// A Homebrew --HEAD build carries no orderable version — CompareVersions
	// would read it as 0.0.0 and warn "update available" forever, and the
	// suggested upgrade would actually move the user off HEAD.
	if IsBrewHeadVersion(cliVersion) {
		return DoctorCheck{
			Name:    "CLI Version",
			Status:  StatusOK,
			Message: fmt.Sprintf("%s (Homebrew --HEAD build; release comparison skipped)", cliVersion),
		}
	}

	latestVersion, err := latestGitHubReleaseFetcher()
	if err != nil {
		// Network error or API issue - don't fail, just warn
		return DoctorCheck{
			Name:    "CLI Version",
			Status:  StatusOK,
			Message: fmt.Sprintf("%s (unable to check for updates)", cliVersion),
		}
	}

	if latestVersion == "" || latestVersion == cliVersion {
		return DoctorCheck{
			Name:    "CLI Version",
			Status:  StatusOK,
			Message: fmt.Sprintf("%s (latest)", cliVersion),
		}
	}

	// Compare versions using simple semver-aware comparison
	if CompareVersions(latestVersion, cliVersion) > 0 {
		upgradeCmd := getUpgradeCommand()
		return DoctorCheck{
			Name:    "CLI Version",
			Status:  StatusWarning,
			Message: fmt.Sprintf("%s (latest: %s; update: %s)", cliVersion, latestVersion, upgradeCmd),
			Fix:     fmt.Sprintf("Upgrade: %s", upgradeCmd),
		}
	}

	return DoctorCheck{
		Name:    "CLI Version",
		Status:  StatusOK,
		Message: fmt.Sprintf("%s (latest)", cliVersion),
	}
}

// CheckCLIVersionLocalOnly reports the local CLI version without making
// network calls. This is intended for machine-readable or other
// non-interactive contexts where deterministic exit behavior matters more than
// update discovery.
func CheckCLIVersionLocalOnly(cliVersion string) DoctorCheck {
	return DoctorCheck{
		Name:    "CLI Version",
		Status:  StatusOK,
		Message: fmt.Sprintf("%s (update check skipped in non-interactive mode)", cliVersion),
	}
}

// installScriptCommand is the default upgrade/install command for non-Homebrew installations.
const installScriptCommand = "curl -fsSL https://raw.githubusercontent.com/steveyegge/beads/main/scripts/install.sh | bash"

// getUpgradeCommand returns the appropriate upgrade command based on how bd was installed.
// Detects Homebrew on macOS/Linux, and falls back to the install script on all platforms.
func getUpgradeCommand() string {
	execPath, err := os.Executable()
	if err != nil {
		return installScriptCommand
	}

	// Resolve symlinks to get the real path
	realPath, err := filepath.EvalSymlinks(execPath)
	if err != nil {
		realPath = execPath
	}

	return upgradeCommandForPath(realPath)
}

// upgradeCommandForPath returns the upgrade command for a given executable path.
// The homebrew-core formula is named "beads" (Formula/b/beads.rb), so the correct
// command is "brew upgrade beads" — even for the legacy tap that used "bd.rb".
func upgradeCommandForPath(execPath string) string {
	lowerPath := strings.ToLower(execPath)

	// Check for Homebrew Cellar path (macOS/Linux)
	// Matches both homebrew-core formula "beads" and legacy tap formula "bd"
	if strings.Contains(lowerPath, "/cellar/beads/") ||
		strings.Contains(lowerPath, "/cellar/bd/") {
		return "brew upgrade beads"
	}

	// Default to install script (works on all platforms including Windows via WSL/Git Bash)
	return installScriptCommand
}

// localVersionFile is the gitignored file that stores the last bd version used locally.
// Must match the constant in version_tracking.go.
const localVersionFile = ".local_version"

// CheckMetadataVersionTracking checks if version tracking is properly configured.
// Version tracking uses .local_version file (gitignored) to track the last bd version used.
//
// GH#662: This was updated to check .local_version instead of metadata.json:LastBdVersion,
// which is now deprecated.
func CheckMetadataVersionTracking(path string, currentVersion string) DoctorCheck {
	beadsDir := ResolveBeadsDirForRepo(path)
	localVersionPath := filepath.Join(beadsDir, localVersionFile)

	// Read .local_version file
	// #nosec G304 - path is constructed from controlled beadsDir + constant
	data, err := os.ReadFile(localVersionPath)
	if err != nil {
		if os.IsNotExist(err) {
			// File doesn't exist yet - will be created on next bd command
			return DoctorCheck{
				Name:    "Version Tracking",
				Status:  StatusWarning,
				Message: "Version tracking not initialized",
				Detail:  "The .local_version file will be created on next bd command",
				Fix:     "Run any bd command (e.g., 'bd ready') to initialize version tracking",
			}
		}
		// Other error reading file
		return DoctorCheck{
			Name:    "Version Tracking",
			Status:  StatusError,
			Message: "Unable to read .local_version file",
			Detail:  err.Error(),
			Fix:     "Check file permissions on .beads/.local_version",
		}
	}

	lastVersion := strings.TrimSpace(string(data))

	// Check if file is empty
	if lastVersion == "" {
		return DoctorCheck{
			Name:    "Version Tracking",
			Status:  StatusWarning,
			Message: ".local_version file is empty",
			Detail:  "Version tracking will be initialized on next command",
			Fix:     "Run any bd command to initialize version tracking",
		}
	}

	trackingActive := DoctorCheck{
		Name:    "Version Tracking",
		Status:  StatusOK,
		Message: fmt.Sprintf("Version tracking active (last: %s, current: %s)", lastVersion, currentVersion),
	}

	// A Homebrew --HEAD stamp carries no ordering, so the staleness heuristic
	// below cannot apply — but it is a healthy marker, not a malformed one.
	// Warning about it would be permanent: the offered fix rewrites the same
	// stamp back.
	if IsBrewHeadVersion(lastVersion) {
		return trackingActive
	}

	// Validate that version is a valid semver-like string
	if !IsValidSemver(lastVersion) {
		return DoctorCheck{
			Name:    "Version Tracking",
			Status:  StatusWarning,
			Message: fmt.Sprintf("Invalid version format in .local_version: %q", lastVersion),
			Detail:  "Expected semver format like '0.24.2'",
			Fix:     "Run any bd command to reset version tracking to current version",
		}
	}

	// Check if version is very old (> 10 versions behind)
	versionDiff := CompareVersions(currentVersion, lastVersion)
	if versionDiff > 0 {
		// Current version is newer - check how far behind
		currentParts := ParseVersionParts(currentVersion)
		lastParts := ParseVersionParts(lastVersion)

		// Guard against short version strings (e.g., "5" → [5] has no [1])
		if len(currentParts) < 2 || len(lastParts) < 2 {
			return trackingActive
		}

		// Simple heuristic: warn if minor version is 10+ behind or major version differs by 1+
		majorDiff := currentParts[0] - lastParts[0]
		minorDiff := currentParts[1] - lastParts[1]

		if majorDiff >= 1 || (majorDiff == 0 && minorDiff >= 10) {
			return DoctorCheck{
				Name:    "Version Tracking",
				Status:  StatusWarning,
				Message: fmt.Sprintf("Last recorded version is very old: %s (current: %s)", lastVersion, currentVersion),
				Detail:  "You may have missed important upgrade notifications",
				Fix:     "Run 'bd upgrade review' to see recent changes",
			}
		}

		// Version is behind but not too old - this is normal after upgrade
		return trackingActive
	}

	// Version is current or ahead
	return DoctorCheck{
		Name:    "Version Tracking",
		Status:  StatusOK,
		Message: fmt.Sprintf("Version tracking active (version: %s)", lastVersion),
	}
}

// fetchLatestGitHubRelease fetches the latest release version from GitHub API.
func fetchLatestGitHubRelease() (string, error) {
	url := "https://api.github.com/repos/steveyegge/beads/releases/latest"

	client := &http.Client{
		Timeout: 5 * time.Second,
	}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}

	// Set User-Agent as required by GitHub API
	req.Header.Set("User-Agent", "beads-cli-doctor")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github api returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	var release struct {
		TagName string `json:"tag_name"`
	}

	if err := json.Unmarshal(body, &release); err != nil {
		return "", err
	}

	// Strip 'v' prefix if present
	version := strings.TrimPrefix(release.TagName, "v")

	return version, nil
}

// CompareVersions compares two semantic version strings, with semver-2.0
// prerelease-suffix semantics: a version carrying a "-<prerelease>" suffix
// sorts below the identical version without one ("1.3.1-rc.1" < "1.3.1"),
// and two prerelease suffixes are compared identifier-by-identifier (each
// identifier numeric if all-digits, else lexical), with the shorter
// identifier list sorting lower when one is a prefix of the other.
//
// Fixes gastownhall/beads#6595: the prior parser split the whole string on
// "." without splitting off the prerelease suffix first, so "1.3.1-rc.1"
// tokenized as ["1","3","1-rc","1"] and Sscanf's tolerant parse of "1-rc"
// silently read just "1" — re-merging the trailing ".1" into a 4th numeric
// part instead of dropping it, so the rc sorted ABOVE its own stable
// release ("1.3.1" < "1.3.1-rc.1"), the opposite of what an rc's own
// release notes promise ("return to the stable release once it ships").
//
// Returns: -1 if v1 < v2, 0 if v1 == v2, 1 if v1 > v2
// Handles versions like "0.20.1", "1.2.3", "1.3.1-rc.1", etc.
func CompareVersions(v1, v2 string) int {
	core1, pre1 := splitPrereleaseSuffix(v1)
	core2, pre2 := splitPrereleaseSuffix(v2)

	if c := compareVersionCore(core1, core2); c != 0 {
		return c
	}
	return comparePrereleaseSuffix(pre1, pre2)
}

// splitPrereleaseSuffix splits a version string on its first "-" into the
// numeric core and the prerelease suffix (empty when there is none).
func splitPrereleaseSuffix(v string) (core, prerelease string) {
	if i := strings.IndexByte(v, '-'); i >= 0 {
		return v[:i], v[i+1:]
	}
	return v, ""
}

// compareVersionCore compares the dot-separated numeric core of two
// versions (e.g. major.minor.patch). A part missing from either side
// defaults to 0, matching the prior behavior for differing part counts.
func compareVersionCore(v1, v2 string) int {
	parts1 := strings.Split(v1, ".")
	parts2 := strings.Split(v2, ".")

	maxLen := len(parts1)
	if len(parts2) > maxLen {
		maxLen = len(parts2)
	}

	for i := 0; i < maxLen; i++ {
		var p1, p2 int

		if i < len(parts1) {
			_, _ = fmt.Sscanf(parts1[i], "%d", &p1)
		}
		if i < len(parts2) {
			_, _ = fmt.Sscanf(parts2[i], "%d", &p2)
		}

		if p1 < p2 {
			return -1
		}
		if p1 > p2 {
			return 1
		}
	}

	return 0
}

// comparePrereleaseSuffix compares two prerelease suffixes per semver 2.0
// precedence rule 11: a version WITH a prerelease suffix sorts below the
// identical version without one; two prereleases compare identifier by
// identifier (dot-separated), each identifier numeric-if-all-digits else
// lexical, and the identifier list with fewer identifiers sorts lower when
// one is a prefix of the other (e.g. "alpha" < "alpha.1").
func comparePrereleaseSuffix(pre1, pre2 string) int {
	if pre1 == "" && pre2 == "" {
		return 0
	}
	if pre1 == "" {
		return 1 // v1 is a release, v2 is a prerelease of the same core: v1 wins.
	}
	if pre2 == "" {
		return -1
	}

	ids1 := strings.Split(pre1, ".")
	ids2 := strings.Split(pre2, ".")

	maxLen := len(ids1)
	if len(ids2) > maxLen {
		maxLen = len(ids2)
	}

	for i := 0; i < maxLen; i++ {
		if i >= len(ids1) {
			return -1
		}
		if i >= len(ids2) {
			return 1
		}
		if c := comparePrereleaseIdentifier(ids1[i], ids2[i]); c != 0 {
			return c
		}
	}
	return 0
}

// comparePrereleaseIdentifier compares one dot-separated prerelease
// identifier. Numeric identifiers (all digits) compare numerically so
// "rc.2" < "rc.10"; anything else compares lexically. Per semver 2.0,
// numeric identifiers always sort lower than alphanumeric ones.
func comparePrereleaseIdentifier(a, b string) int {
	aNum, aIsNum := prereleaseIdentifierAsInt(a)
	bNum, bIsNum := prereleaseIdentifierAsInt(b)

	switch {
	case aIsNum && bIsNum:
		switch {
		case aNum < bNum:
			return -1
		case aNum > bNum:
			return 1
		default:
			return 0
		}
	case aIsNum && !bIsNum:
		return -1
	case !aIsNum && bIsNum:
		return 1
	default:
		return strings.Compare(a, b)
	}
}

// prereleaseIdentifierAsInt reports whether s is a non-empty run of ASCII
// digits, and its integer value when it is.
func prereleaseIdentifierAsInt(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		return 0, false
	}
	return n, true
}

// IsBrewHeadVersion recognizes the version Homebrew stamps into --HEAD
// installs of the core beads formula: HEAD-<shortsha>, a bare HEAD when the
// head spec resolves no commit, and either shape carrying Homebrew's
// _<revision> suffix. Such a stamp is a healthy current-era version marker,
// not a malformed one — the binary rewrites it identically on every run, so
// nothing the user does can "fix" it into semver.
//
// This is the canonical definition; every consumer of a version stamp that
// needs to tell a --HEAD build apart from a release delegates here so the
// ends cannot drift.
func IsBrewHeadVersion(version string) bool {
	rest, ok := strings.CutPrefix(version, "HEAD")
	if !ok {
		return false
	}
	// Homebrew's pkg_version appends _<revision> once the formula carries a
	// revision, so a revision bump must not read as malformed. The revision is
	// an unsigned decimal; a digits-only check keeps Atoi's tolerance of
	// signed forms ("+1", "-0") from admitting shapes brew never emits.
	if cut := strings.IndexByte(rest, '_'); cut >= 0 {
		revision := rest[cut+1:]
		if revision == "" || strings.Trim(revision, "0123456789") != "" {
			return false
		}
		rest = rest[:cut]
	}
	// Homebrew leaves the version as a bare "HEAD" whenever the head spec
	// cannot resolve a commit.
	if rest == "" {
		return true
	}
	sha, ok := strings.CutPrefix(rest, "-")
	if !ok {
		return false
	}
	// Bound the tail to a plausible git abbreviation so that arbitrary hex
	// cannot stand in for a commit; git never abbreviates below 7 characters.
	if len(sha) < 7 || len(sha) > 40 {
		return false
	}
	return strings.Trim(sha, "0123456789abcdefABCDEF") == ""
}

// IsValidSemver checks if a version string is valid semver-like format (X.Y.Z)
func IsValidSemver(version string) bool {
	if version == "" {
		return false
	}

	// Split by dots and ensure all parts are numeric
	versionParts := strings.Split(version, ".")
	if len(versionParts) < 1 {
		return false
	}

	// Parse each part to ensure it's a valid number
	for _, part := range versionParts {
		if part == "" {
			return false
		}
		var num int
		if _, err := fmt.Sscanf(part, "%d", &num); err != nil {
			return false
		}
		if num < 0 {
			return false
		}
	}

	return true
}

// ParseVersionParts parses version string into numeric parts
// Returns [major, minor, patch, ...] or empty slice on error
func ParseVersionParts(version string) []int {
	parts := strings.Split(version, ".")
	result := make([]int, 0, len(parts))

	for _, part := range parts {
		var num int
		if _, err := fmt.Sscanf(part, "%d", &num); err != nil {
			return result
		}
		result = append(result, num)
	}

	return result
}
