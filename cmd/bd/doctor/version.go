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

// CheckCLIVersion checks if the CLI version is up to date.
// Takes cliVersion parameter since it can't access the Version variable from main package.
func CheckCLIVersion(cliVersion string) DoctorCheck {
	latestVersion, err := fetchLatestGitHubRelease()
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
			Message: fmt.Sprintf("%s (latest: %s)", cliVersion, latestVersion),
			Fix:     fmt.Sprintf("Upgrade: %s", upgradeCmd),
		}
	}

	return DoctorCheck{
		Name:    "CLI Version",
		Status:  StatusOK,
		Message: fmt.Sprintf("%s (latest)", cliVersion),
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
	// Follow redirect to resolve actual beads directory (bd-tvus fix)
	beadsDir := resolveBeadsDir(filepath.Join(path, ".beads"))
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
		return DoctorCheck{
			Name:    "Version Tracking",
			Status:  StatusOK,
			Message: fmt.Sprintf("Version tracking active (last: %s, current: %s)", lastVersion, currentVersion),
		}
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

// CompareVersions compares two semantic version strings.
// Returns: -1 if v1 < v2, 0 if v1 == v2, 1 if v1 > v2
// Handles versions like "0.20.1", "1.2.3", etc.
func CompareVersions(v1, v2 string) int {
	// Split versions into parts
	parts1 := strings.Split(v1, ".")
	parts2 := strings.Split(v2, ".")

	// Compare each part
	maxLen := len(parts1)
	if len(parts2) > maxLen {
		maxLen = len(parts2)
	}

	for i := 0; i < maxLen; i++ {
		var p1, p2 int

		// Get part value or default to 0 if part doesn't exist
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
