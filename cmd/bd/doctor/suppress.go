package doctor

import (
	"context"
	"os"
	"strings"

	"github.com/steveyegge/beads/internal/storage/dolt"
)

// SuppressConfigPrefix is the config namespace for suppressing specific doctor warnings.
// Users set keys like "doctor.suppress.git-hooks" = "true" to suppress checks.
const SuppressConfigPrefix = "doctor.suppress."

// GetSuppressedChecks reads doctor.suppress.* config keys from the database
// and returns a set of suppressed check slugs (e.g., "git-hooks", "pending-migrations").
// Returns an empty map if the database can't be opened or no suppressions are configured.
// Opens its own store; prefer GetSuppressedChecksWithStore when a shared store is available.
func GetSuppressedChecks(path string) map[string]bool {
	suppressed := make(map[string]bool)

	beadsDir := ResolveBeadsDirForRepo(path)

	if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
		return suppressed
	}

	ctx := context.Background()
	store, err := dolt.NewFromConfigWithCLIOptions(ctx, beadsDir, &dolt.Config{ReadOnly: true})
	if err != nil {
		return suppressed
	}
	defer func() { _ = store.Close() }()

	return getSuppressedChecksFromStore(store)
}

// GetSuppressedChecksWithStore reads suppressed checks using a shared store (GH#2636).
func GetSuppressedChecksWithStore(ss *SharedStore) map[string]bool {
	store := ss.Store()
	if store == nil {
		return make(map[string]bool)
	}
	return getSuppressedChecksFromStore(store)
}

func getSuppressedChecksFromStore(store *dolt.DoltStore) map[string]bool {
	suppressed := make(map[string]bool)

	ctx := context.Background()
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
