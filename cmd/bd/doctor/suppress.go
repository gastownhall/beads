package doctor

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/gastownhall/beads/internal/storage/dolt"
)

// SuppressConfigPrefix is the config namespace for suppressing specific doctor warnings.
// Users set keys like "doctor.suppress.git-hooks" = "true" to suppress checks.
const SuppressConfigPrefix = "doctor.suppress."

// GetSuppressedChecks reads doctor.suppress.* config keys from the database
// and returns a set of suppressed check slugs (e.g., "git-hooks", "pending-migrations").
// Returns an empty map if the database can't be opened or no suppressions are configured.
func GetSuppressedChecks(path string) map[string]bool {
	suppressed := make(map[string]bool)

	beadsDir := filepath.Join(path, ".beads")
	beadsDir = resolveBeadsDir(beadsDir)

	if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
		return suppressed
	}

	ctx := context.Background()
	store, err := dolt.NewFromConfigWithOptions(ctx, beadsDir, &dolt.Config{ReadOnly: true})
	if err != nil {
		return suppressed
	}
	defer func() { _ = store.Close() }()

	allConfig, err := store.GetAllConfig(ctx)
	if err != nil {
		return suppressed
	}

	for key, value := range allConfig {
		if strings.HasPrefix(key, SuppressConfigPrefix) && strings.ToLower(value) == "true" {
			slug := key[len(SuppressConfigPrefix):]
			if slug != "" {
				suppressed[slug] = true
			}
		}
	}

	return suppressed
}

// CheckNameToSlug converts a human-readable check name to a config-friendly slug.
// For example: "Git Hooks" → "git-hooks", "CLI Version" → "cli-version".
func CheckNameToSlug(name string) string {
	slug := strings.ToLower(name)
	slug = strings.ReplaceAll(slug, " ", "-")
	// Collapse multiple hyphens
	for strings.Contains(slug, "--") {
		slug = strings.ReplaceAll(slug, "--", "-")
	}
	return strings.Trim(slug, "-")
}
