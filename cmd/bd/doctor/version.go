package doctor

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
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
		upgradeCmds := `  • Homebrew: brew upgrade bd
  • Script: curl -fsSL https://raw.githubusercontent.com/steveyegge/beads/main/scripts/install.sh | bash`

		return DoctorCheck{
			Name:    "CLI Version",
			Status:  StatusWarning,
			Message: fmt.Sprintf("%s (latest: %s)", cliVersion, latestVersion),
			Fix:     fmt.Sprintf("Upgrade to latest version:\n%s", upgradeCmds),
		}
	}

	return DoctorCheck{
		Name:    "CLI Version",
		Status:  StatusOK,
		Message: fmt.Sprintf("%s (latest)", cliVersion),
	}
}

// CheckMetadataVersionTracking checks if metadata.json has proper version tracking.
func CheckMetadataVersionTracking(path string, currentVersion string) DoctorCheck {
	beadsDir := filepath.Join(path, ".beads")

	// Load metadata.json
	cfg, err := configfile.Load(beadsDir)
	if err != nil {
		return DoctorCheck{
			Name:    "Metadata Version Tracking",
			Status:  StatusError,
			Message: "Unable to read metadata.json",
			Detail:  err.Error(),
			Fix:     "Ensure metadata.json exists and is valid JSON. Run 'bd init' if needed.",
		}
	}

	// Check if metadata.json exists
	if cfg == nil {
		return DoctorCheck{
			Name:    "Metadata Version Tracking",
			Status:  StatusWarning,
			Message: "metadata.json not found",
			Fix:     "Run any bd command to create metadata.json with version tracking",
		}
	}

	// Check if LastBdVersion field is present
	if cfg.LastBdVersion == "" {
		return DoctorCheck{
			Name:    "Metadata Version Tracking",
			Status:  StatusWarning,
			Message: "LastBdVersion field is empty (first run)",
			Detail:  "Version tracking will be initialized on next command",
			Fix:     "Run any bd command to initialize version tracking",
		}
	}

	// Validate that LastBdVersion is a valid semver-like string
	// Simple validation: should be X.Y.Z format where X, Y, Z are numbers
	if !IsValidSemver(cfg.LastBdVersion) {
		return DoctorCheck{
			Name:    "Metadata Version Tracking",
			Status:  StatusWarning,
			Message: fmt.Sprintf("LastBdVersion has invalid format: %q", cfg.LastBdVersion),
			Detail:  "Expected semver format like '0.24.2'",
			Fix:     "Run any bd command to reset version tracking to current version",
		}
	}

	// Check if LastBdVersion is very old (> 10 versions behind)
	// Calculate version distance
	versionDiff := CompareVersions(currentVersion, cfg.LastBdVersion)
	if versionDiff > 0 {
		// Current version is newer - check how far behind
		currentParts := ParseVersionParts(currentVersion)
		lastParts := ParseVersionParts(cfg.LastBdVersion)

		// Simple heuristic: warn if minor version is 10+ behind or major version differs by 1+
		majorDiff := currentParts[0] - lastParts[0]
		minorDiff := currentParts[1] - lastParts[1]

		if majorDiff >= 1 || (majorDiff == 0 && minorDiff >= 10) {
			return DoctorCheck{
				Name:    "Metadata Version Tracking",
				Status:  StatusWarning,
				Message: fmt.Sprintf("LastBdVersion is very old: %s (current: %s)", cfg.LastBdVersion, currentVersion),
				Detail:  "You may have missed important upgrade notifications",
				Fix:     "Run 'bd upgrade review' to see recent changes",
			}
		}

		// Version is behind but not too old
		return DoctorCheck{
			Name:    "Metadata Version Tracking",
			Status:  StatusOK,
			Message: fmt.Sprintf("Version tracking active (last: %s, current: %s)", cfg.LastBdVersion, currentVersion),
		}
	}

	// Version is current or ahead (shouldn't happen, but handle it)
	return DoctorCheck{
		Name:    "Metadata Version Tracking",
		Status:  StatusOK,
		Message: fmt.Sprintf("Version tracking active (version: %s)", cfg.LastBdVersion),
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
