package issueops

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/config"
)

// withYAMLCustomTypes points the process config at a throwaway
// .beads/config.yaml declaring types.custom, with HOME and XDG_CONFIG_HOME
// isolated so no user-level config can leak in.
func withYAMLCustomTypes(t *testing.T, customCSV string) {
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
	if customCSV != "" {
		content = "types:\n  custom: \"" + customCSV + "\"\n"
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

// customTypeSources describes one database + config.yaml state.
type customTypeSources struct {
	table     []string // custom_types rows
	configRow string   // config types.custom value ("" = unset)
	yaml      string   // config.yaml types.custom CSV ("" = unset)
}

func expectCustomTypesTable(mock sqlmock.Sqlmock, names []string) {
	rows := sqlmock.NewRows([]string{"name"})
	for _, n := range names {
		rows.AddRow(n)
	}
	mock.ExpectQuery(regexp.QuoteMeta("SELECT name FROM custom_types ORDER BY name")).WillReturnRows(rows)
}

// resolveViaCustomTypesInTx is the resolver issue create/update validation uses.
func resolveViaCustomTypesInTx(t *testing.T, src customTypeSources) []string {
	t.Helper()
	db, mock, tx := beginMockTx(t)
	defer db.Close()
	expectCustomTypesTable(mock, src.table)
	if len(src.table) == 0 {
		rows := sqlmock.NewRows([]string{"value"})
		if src.configRow != "" {
			rows.AddRow(src.configRow)
		}
		mock.ExpectQuery(regexp.QuoteMeta("SELECT value FROM config WHERE `key` = ?")).
			WithArgs("types.custom").WillReturnRows(rows)
	}
	got, err := ResolveCustomTypesInTx(context.Background(), tx)
	if err != nil {
		t.Fatalf("ResolveCustomTypesInTx: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
	return got
}

// resolveViaCustomConfigInTx is the resolver the server-mode store's
// GetCustomTypes (and so direct-mode `bd types`) uses.
func resolveViaCustomConfigInTx(t *testing.T, src customTypeSources) []string {
	t.Helper()
	db, mock, tx := beginMockTx(t)
	defer db.Close()
	// One custom status so the status half resolves from its table and the
	// config-row read below is driven by the types half alone.
	mock.ExpectQuery(regexp.QuoteMeta("SELECT name, category FROM custom_statuses ORDER BY name")).
		WillReturnRows(sqlmock.NewRows([]string{"name", "category"}).AddRow("review", "wip"))
	expectCustomTypesTable(mock, src.table)
	if len(src.table) == 0 {
		rows := sqlmock.NewRows([]string{"key", "value"})
		if src.configRow != "" {
			rows.AddRow("types.custom", src.configRow)
		}
		mock.ExpectQuery(regexp.QuoteMeta("SELECT `key`, value FROM config WHERE `key` IN (?,?)")).
			WithArgs("status.custom", "types.custom").WillReturnRows(rows)
	}
	_, got, err := ResolveCustomConfigInTx(context.Background(), tx)
	if err != nil {
		t.Fatalf("ResolveCustomConfigInTx: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
	return got
}

// TestCustomTypeResolversAgree pins the single composition rule for custom
// issue types (custom_types table, else the types.custom config row, always
// unioned with config.yaml types.custom). The listing path (`bd types`) and
// the validation path (create/update) must resolve the same set, or `bd types`
// advertises types that create rejects or vice versa.
func TestCustomTypeResolversAgree(t *testing.T) {
	cases := []struct {
		name string
		src  customTypeSources
		want []string
	}{
		{"nothing configured", customTypeSources{}, nil},
		{"table only", customTypeSources{table: []string{"convoy", "rig"}}, []string{"convoy", "rig"}},
		{"config row only", customTypeSources{configRow: "convoy,rig"}, []string{"convoy", "rig"}},
		{"yaml only", customTypeSources{yaml: "gizmo"}, []string{"gizmo"}},
		{"table plus yaml", customTypeSources{table: []string{"convoy"}, yaml: "gizmo"}, []string{"convoy", "gizmo"}},
		{"config row plus yaml", customTypeSources{configRow: "convoy", yaml: "gizmo,convoy"}, []string{"convoy", "gizmo"}},
		{"table wins over config row", customTypeSources{table: []string{"convoy"}, configRow: "convoy,rig", yaml: "gizmo"}, []string{"convoy", "gizmo"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withYAMLCustomTypes(t, tc.src.yaml)
			validate := resolveViaCustomTypesInTx(t, tc.src)
			list := resolveViaCustomConfigInTx(t, tc.src)
			if len(validate) == 0 && len(tc.want) == 0 && len(list) == 0 {
				return
			}
			if !reflect.DeepEqual(validate, tc.want) {
				t.Errorf("ResolveCustomTypesInTx = %#v, want %#v", validate, tc.want)
			}
			if !reflect.DeepEqual(list, tc.want) {
				t.Errorf("ResolveCustomConfigInTx custom types = %#v, want %#v (must match the validation resolver)", list, tc.want)
			}
		})
	}
}

func TestComposeCustomTypes(t *testing.T) {
	cases := []struct {
		name      string
		table     []string
		configRow string
		yaml      []string
		want      []string
	}{
		{"empty", nil, "", nil, nil},
		{"table trimmed and deduped", []string{" convoy ", "", "convoy", "rig"}, "", nil, []string{"convoy", "rig"}},
		{"config row json form", nil, `["convoy","rig"]`, nil, []string{"convoy", "rig"}},
		{"table ignores config row", []string{"convoy"}, "rig", nil, []string{"convoy"}},
		{"yaml appended after db", []string{"rig"}, "", []string{"gizmo", "rig"}, []string{"rig", "gizmo"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ComposeCustomTypes(tc.table, tc.configRow, tc.yaml)
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ComposeCustomTypes = %#v, want %#v", got, tc.want)
			}
		})
	}
}
