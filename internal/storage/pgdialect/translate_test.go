package pgdialect

import "testing"

func TestTranslate(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "placeholders numbered",
			in:   `SELECT * FROM issues WHERE status = ? AND priority = ?`,
			want: `SELECT * FROM issues WHERE status = $1 AND priority = $2`,
		},
		{
			name: "placeholder inside string is left alone",
			in:   `SELECT '?', assignee FROM issues WHERE id = ?`,
			want: `SELECT '?', assignee FROM issues WHERE id = $1`,
		},
		{
			name: "backtick reserved-word identifier",
			in:   "SELECT value FROM config WHERE `key` = ?",
			want: `SELECT value FROM config WHERE "key" = $1`,
		},
		{
			name: "INSERT IGNORE -> ON CONFLICT DO NOTHING",
			in:   `INSERT IGNORE INTO labels (issue_id, label) VALUES (?, ?)`,
			want: `INSERT INTO labels (issue_id, label) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
		},
		{
			name: "ON DUPLICATE KEY type=type -> ON CONFLICT DO NOTHING",
			in:   `INSERT INTO dependencies (id, issue_id, depends_on_issue_id, type, created_by, created_at) VALUES (?, ?, ?, ?, ?, ?) ON DUPLICATE KEY UPDATE type = type`,
			want: `INSERT INTO dependencies (id, issue_id, depends_on_issue_id, type, created_by, created_at) VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT DO NOTHING`,
		},
		{
			name: "issue upsert VALUES(col) -> EXCLUDED.col with (id) target",
			in:   `INSERT INTO issues (id, title) VALUES (?, ?) ON DUPLICATE KEY UPDATE title = VALUES(title)`,
			want: `INSERT INTO issues (id, title) VALUES ($1, $2) ON CONFLICT (id) DO UPDATE SET title = EXCLUDED.title`,
		},
		{
			name: "stale-guard IF(VALUES...)-> CASE WHEN EXCLUDED",
			in:   `INSERT INTO issues (id, title, updated_at) VALUES (?, ?, ?) ON DUPLICATE KEY UPDATE title = IF(VALUES(updated_at) > updated_at, VALUES(title), title)`,
			want: `INSERT INTO issues (id, title, updated_at) VALUES ($1, $2, $3) ON CONFLICT (id) DO UPDATE SET title = CASE WHEN EXCLUDED.updated_at > updated_at THEN EXCLUDED.title ELSE title END`,
		},
		{
			name: "child counter GREATEST upsert with (parent_id) target",
			in:   `INSERT INTO child_counters (parent_id, last_child) VALUES (?, ?) ON DUPLICATE KEY UPDATE last_child = GREATEST(last_child, ?)`,
			want: `INSERT INTO child_counters (parent_id, last_child) VALUES ($1, $2) ON CONFLICT (parent_id) DO UPDATE SET last_child = GREATEST(last_child, $3)`,
		},
		{
			name: "UPDATE SET unqualifies target, keeps RHS alias reference",
			in:   `UPDATE issues i SET i.is_blocked = 1, i.updated_at = i.updated_at WHERE i.id = ?`,
			want: `UPDATE issues i SET is_blocked = 1, updated_at = i.updated_at WHERE i.id = $1`,
		},
		{
			name: "REPLACE INTO -> INSERT ON CONFLICT DO UPDATE",
			in:   "REPLACE INTO config (`key`, value) VALUES (?, ?)",
			want: `INSERT INTO config ("key", value) VALUES ($1, $2) ON CONFLICT ("key") DO UPDATE SET value = EXCLUDED.value`,
		},
		{
			name: "CONCAT -> || (with LIKE and escaped percent)",
			in:   `SELECT id FROM issues WHERE id LIKE CONCAT(?, '.%')`,
			want: `SELECT id FROM issues WHERE id LIKE ($1 || '.%')`,
		},
		{
			name: "LOCATE(CONCAT(...)) nested -> POSITION(... IN ...)",
			in:   `SELECT 1 WHERE LOCATE(CONCAT(',', e.issue_id, ','), d.path) = 0`,
			want: `SELECT 1 WHERE POSITION((',' || e.issue_id || ',') IN d.path) = 0`,
		},
		{
			name: "UTC_TIMESTAMP()",
			in:   `SELECT 1 WHERE defer_until IS NULL OR defer_until <= UTC_TIMESTAMP()`,
			want: `SELECT 1 WHERE defer_until IS NULL OR defer_until <= (CURRENT_TIMESTAMP AT TIME ZONE 'utc')`,
		},
		{
			name: "literal JSON gate path",
			in:   `SELECT 1 WHERE JSON_UNQUOTE(JSON_EXTRACT(d.metadata, '$.gate')) = 'any-children'`,
			want: `SELECT 1 WHERE (d.metadata #>> '{gate}') = 'any-children'`,
		},
		{
			name: "binary label collation",
			in:   `SELECT label FROM labels WHERE issue_id = ? ORDER BY label`,
			want: `SELECT label FROM labels WHERE issue_id = $1 ORDER BY label COLLATE "C"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Translate(tc.in)
			if err != nil {
				t.Fatalf("Translate error: %v", err)
			}
			if got != tc.want {
				t.Errorf("Translate mismatch\n in:   %s\n got:  %s\n want: %s", tc.in, got, tc.want)
			}
		})
	}
}
