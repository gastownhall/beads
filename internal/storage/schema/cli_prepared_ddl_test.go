package schema

import (
	"fmt"
	"strings"
	"testing"
)

// TestBundleMigrationsWithPreparedALTERAreOverriddenOrJustified is the guard
// that closes the 0060/0065 defect class going forward: a new main-plane
// migration cannot reach the CLI fresh bundle with a PREPARE'd ALTER TABLE
// unless someone either registered a direct-DDL override in
// cliCompatibleMigrationSQL or wrote down why the ALTER cannot fire on a fresh
// database. No Dolt binary is involved, so unlike the parity oracle a CLI
// upgrade cannot mask it.
func TestBundleMigrationsWithPreparedALTERAreOverriddenOrJustified(t *testing.T) {
	for _, f := range mainSource.list() {
		data, err := mainSource.files.ReadFile(mainSource.dir + "/" + f.name)
		if err != nil {
			t.Fatalf("read %s: %v", f.name, err)
		}
		hits := preparedALTERTableStatements(cliCompatibleMigrationSQL(f.name, string(data)))
		if len(hits) == 0 {
			continue
		}
		if _, ok := preparedALTERSafeOnFreshBundle[f.name]; ok {
			continue
		}
		var b strings.Builder
		for _, hit := range hits {
			fmt.Fprintf(&b, "\n  line %d: %s", hit.Line, hit.Statement)
		}
		t.Errorf(`%s reaches the CLI fresh bundle with a PREPARE'd ALTER TABLE:%s

The Dolt CLI batch path silently no-ops it while EXECUTE reports success
(dolthub/dolt#11345, fixed in 2.3.0 but the CLI is pinned to 2.2.0), so the
bundle would be missing this schema change while the runtime has it.

Either register a direct-DDL override for %s in cliCompatibleMigrationSQL
(precedent: 0060, 0065), or -- if the guard provably cannot fire on a fresh
database -- add an entry to preparedALTERSafeOnFreshBundle saying why.`,
			f.name, b.String(), f.name)
	}
}

// TestPreparedALTERSafeOnFreshBundleHasNoStaleEntries keeps the inventory
// honest in the other direction: an entry that no longer names a migration
// carrying a prepared ALTER is either a typo or a leftover, and either way it
// stops documenting anything.
func TestPreparedALTERSafeOnFreshBundleHasNoStaleEntries(t *testing.T) {
	live := make(map[string]bool)
	for _, f := range mainSource.list() {
		data, err := mainSource.files.ReadFile(mainSource.dir + "/" + f.name)
		if err != nil {
			t.Fatalf("read %s: %v", f.name, err)
		}
		if len(preparedALTERTableStatements(cliCompatibleMigrationSQL(f.name, string(data)))) > 0 {
			live[f.name] = true
		}
	}
	for name, why := range preparedALTERSafeOnFreshBundle {
		if !live[name] {
			t.Errorf("preparedALTERSafeOnFreshBundle[%q] is stale: that migration no longer reaches the bundle with a prepared ALTER TABLE", name)
		}
		if strings.TrimSpace(why) == "" {
			t.Errorf("preparedALTERSafeOnFreshBundle[%q] has no justification", name)
		}
	}
}

func TestPreparedALTERTableStatements(t *testing.T) {
	tests := []struct {
		name string
		sql  string
		want []string
	}{
		{
			name: "guarded prepared ALTER via IF",
			sql: `SET @needs = (SELECT 1);
SET @sql = IF(@needs = 1, 'ALTER TABLE wisp_comments MODIFY COLUMN text LONGTEXT NOT NULL', 'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;`,
			want: []string{"ALTER TABLE wisp_comments MODIFY COLUMN text LONGTEXT NOT NULL"},
		},
		{
			name: "DDL in the else branch is still found",
			sql: `SET @sql = IF(@needs = 1, 'SELECT 1', 'ALTER TABLE issues DROP COLUMN gone');
PREPARE stmt FROM @sql; EXECUTE stmt;`,
			want: []string{"ALTER TABLE issues DROP COLUMN gone"},
		},
		{
			name: "CONCAT'd fragment",
			sql: `SET @sql = IF(@clauses = '', 'SELECT 1', CONCAT('ALTER TABLE issues ', @clauses));
PREPARE stmt FROM @sql; EXECUTE stmt;`,
			want: []string{"ALTER TABLE issues"},
		},
		{
			name: "prepared from a literal, no variable",
			sql:  `PREPARE stmt FROM 'ALTER TABLE issues ADD COLUMN x INT'; EXECUTE stmt;`,
			want: []string{"ALTER TABLE issues ADD COLUMN x INT"},
		},
		{
			name: "multi-line assignment",
			sql: `SET @sql = IF(@needs = 1,
    'ALTER TABLE issues ADD COLUMN storage_class VARCHAR(16)',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;`,
			want: []string{"ALTER TABLE issues ADD COLUMN storage_class VARCHAR(16)"},
		},
		{
			name: "doubled quotes inside the DDL",
			sql: `SET @sql = IF(@needs = 1, 'ALTER TABLE issues MODIFY COLUMN close_reason LONGTEXT DEFAULT ''''', 'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt;`,
			want: []string{"ALTER TABLE issues MODIFY COLUMN close_reason LONGTEXT DEFAULT ''''"},
		},
		{
			name: "two prepared ALTERs sharing one variable name",
			sql: `SET @sql = IF(@a = 1, 'ALTER TABLE issues ADD COLUMN a INT', 'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt;
SET @sql = IF(@b = 1, 'ALTER TABLE wisps ADD COLUMN b INT', 'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt;`,
			want: []string{"ALTER TABLE issues ADD COLUMN a INT", "ALTER TABLE wisps ADD COLUMN b INT"},
		},
		{
			name: "direct ALTER is not prepared",
			sql:  `ALTER TABLE issues ADD COLUMN storage_class VARCHAR(16);`,
			want: nil,
		},
		{
			name: "an ALTER named only in a comment is not a statement",
			sql:  "-- SET @sql = IF(@x, 'ALTER TABLE issues DROP COLUMN y', 'SELECT 1');\nSELECT 1;",
			want: nil,
		},
		{
			name: "prepared CREATE INDEX is out of scope -- it executes on 2.2.0",
			sql: `SET @sql = IF(@needs = 1, 'CREATE INDEX idx_x ON issues (x)', 'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt;`,
			want: nil,
		},
		{
			name: "prepared RENAME TABLE is out of scope",
			sql: `SET @sql = IF(@exists = 0, 'RENAME TABLE __temp__leases TO leases', 'DROP TABLE __temp__leases');
PREPARE stmt FROM @sql; EXECUTE stmt;`,
			want: nil,
		},
		{
			name: "prepared DML belongs to check-migration-hygiene.sh, not here",
			sql: `SET @sql = IF(@needs = 1, 'UPDATE issues SET is_blocked = 0', 'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt;`,
			want: nil,
		},
		{
			name: "an unprepared variable holding DDL is not executed",
			sql:  `SET @sql = 'ALTER TABLE issues DROP COLUMN x'; SELECT @sql;`,
			want: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			for _, hit := range preparedALTERTableStatements(tc.sql) {
				got = append(got, hit.Statement)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %d hits %q, want %d %q", len(got), got, len(tc.want), tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("hit %d = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}
