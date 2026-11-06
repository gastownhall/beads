package dolt

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/types"
)

// The custom status/type vocabulary cache (bd-dpn4op, GH#5581): a read that
// fails must answer that one call from config.yaml without marking the caches
// loaded, so the next getter reads the database again instead of serving the
// fallback until the store is recreated.

var errCustomConfigInjected = errors.New("injected custom config read failure")

var (
	ccStatusesQuery = regexp.QuoteMeta("SELECT name, category FROM custom_statuses ORDER BY name")
	ccTypesQuery    = regexp.QuoteMeta("SELECT name FROM custom_types ORDER BY name")
	ccConfigQuery   = regexp.QuoteMeta("SELECT `key`, value FROM config WHERE `key` IN (?,?)")
)

// withCustomVocabularyYAML points the process config at a throwaway
// .beads/config.yaml declaring status.custom and types.custom ("" = unset),
// with HOME and XDG_CONFIG_HOME isolated so no user-level config leaks in.
func withCustomVocabularyYAML(t *testing.T, statusCSV, typeCSV string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", filepath.Join(dir, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "xdg"))
	t.Setenv("BEADS_DIR", "")
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	content := ""
	if statusCSV != "" {
		content += "status:\n  custom: \"" + statusCSV + "\"\n"
	}
	if typeCSV != "" {
		content += "types:\n  custom: \"" + typeCSV + "\"\n"
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(content), 0o644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}
	t.Chdir(dir)
	config.ResetForTesting()
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	t.Cleanup(config.ResetForTesting)
}

// ccFailure arms one vocabulary read that ultimately fails and returns the
// context the failing getter call must use.
type ccFailure struct {
	name string
	arm  func(t *testing.T, mock sqlmock.Sqlmock) context.Context
}

var ccReadFailures = []ccFailure{
	{"begin fails", func(_ *testing.T, mock sqlmock.Sqlmock) context.Context {
		mock.ExpectBegin().WillReturnError(errCustomConfigInjected)
		return context.Background()
	}},
	{"context cancelled", func(t *testing.T, _ sqlmock.Sqlmock) context.Context {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		return ctx
	}},
}

// ccGetter is one of the three public vocabulary getters, reduced to the
// names it reports.
type ccGetter struct {
	name string
	get  func(ctx context.Context, s *DoltStore) ([]string, error)
}

var ccGetters = []ccGetter{
	{"GetCustomStatuses", func(ctx context.Context, s *DoltStore) ([]string, error) {
		return s.GetCustomStatuses(ctx)
	}},
	{"GetCustomStatusesDetailed", func(ctx context.Context, s *DoltStore) ([]string, error) {
		detailed, err := s.GetCustomStatusesDetailed(ctx)
		var out []string
		for _, st := range detailed {
			out = append(out, st.Name+":"+string(st.Category))
		}
		return out, err
	}},
	{"GetCustomTypes", func(ctx context.Context, s *DoltStore) ([]string, error) {
		return s.GetCustomTypes(ctx)
	}},
}

func ccFlags(s *DoltStore) (statusLoaded, typeLoaded bool) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	return s.customStatusCached, s.customTypeCached
}

func ccAssertNames(t *testing.T, label string, got, want []string) {
	t.Helper()
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s = %#v, want %#v", label, got, want)
	}
}

// TestCustomConfigCacheFailedReadServesFallbackUnloaded pins AC-1: a getter
// whose read fails answers from config.yaml (empty when absent) with a nil
// error and leaves both caches unloaded.
func TestCustomConfigCacheFailedReadServesFallbackUnloaded(t *testing.T) {
	yamlCases := []struct {
		name               string
		statusCSV, typeCSV string
		want               map[string][]string
	}{
		{"yaml set", "triage:wip", "gizmo", map[string][]string{
			"GetCustomStatuses":         {"triage"},
			"GetCustomStatusesDetailed": {"triage:" + string(types.CategoryWIP)},
			"GetCustomTypes":            {"gizmo"},
		}},
		{"yaml absent", "", "", map[string][]string{}},
	}
	for _, yc := range yamlCases {
		for _, failure := range ccReadFailures {
			for _, getter := range ccGetters {
				t.Run(yc.name+"/"+failure.name+"/"+getter.name, func(t *testing.T) {
					withCustomVocabularyYAML(t, yc.statusCSV, yc.typeCSV)
					store, mock, _ := vhCountedStore(t)
					ctx := failure.arm(t, mock)

					got, err := getter.get(ctx, store)
					if err != nil {
						t.Fatalf("%s after a failed read: error %v, want nil", getter.name, err)
					}
					ccAssertNames(t, getter.name, got, yc.want[getter.name])
					if statusLoaded, typeLoaded := ccFlags(store); statusLoaded || typeLoaded {
						t.Errorf("cache flags after a failed read = (status %v, type %v), want both false", statusLoaded, typeLoaded)
					}
					if err := mock.ExpectationsWereMet(); err != nil {
						t.Errorf("unmet SQL expectations: %v", err)
					}
				})
			}
		}
	}
}
