//go:build cgo

package fix

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/types"
)

// flattenFKFixture is a migrated server store with a multi-commit history,
// a durable issue and a wisp, each with clone-local children (an events row
// and a wisp_labels row).
type flattenFKFixture struct {
	t     *testing.T
	ctx   context.Context
	store *dolt.DoltStore
	db    *sql.DB
}

func newFlattenFKFixture(t *testing.T) *flattenFKFixture {
	t.Helper()
	requireFixDoltContainer(t)
	port := fixTestServerPort()
	ctx := context.Background()

	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("create .beads: %v", err)
	}
	h := sha256.Sum256([]byte(t.Name() + fmt.Sprintf("%d", time.Now().UnixNano())))
	dbName := "fixtest_" + hex.EncodeToString(h[:6])
	store, err := dolt.New(ctx, &dolt.Config{
		Path:            filepath.Join(beadsDir, "beads.db"),
		ServerHost:      "127.0.0.1",
		ServerPort:      port,
		Database:        dbName,
		CreateIfMissing: true,
	})
	if err != nil {
		t.Fatalf("dolt.New: %v", err)
	}
	t.Cleanup(func() {
		store.Close()
		dropFixTestDatabase(dbName, port)
	})
	if err := store.SetConfig(ctx, "issue_prefix", "fl"); err != nil {
		t.Fatalf("set issue_prefix: %v", err)
	}

	f := &flattenFKFixture{t: t, ctx: ctx, store: store, db: store.UnderlyingDB()}
	for _, issue := range []*types.Issue{
		{ID: "fl-one", Title: "one", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "fl-wisp", Title: "wisp", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true},
	} {
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("CreateIssue %s: %v", issue.ID, err)
		}
		f.exec("CALL DOLT_ADD('-A')")
		f.exec("CALL DOLT_COMMIT('-m', ?, '--allow-empty')", "add "+issue.ID)
	}
	f.exec("INSERT INTO wisp_labels (issue_id, label) VALUES ('fl-wisp', 'keep')")
	if severed := f.scan(); len(severed) != 0 {
		t.Fatalf("severed clone-local FKs before flatten = %v, want none", severed)
	}
	return f
}

func (f *flattenFKFixture) exec(query string, args ...any) {
	f.t.Helper()
	if _, err := f.db.ExecContext(f.ctx, query, args...); err != nil {
		f.t.Fatalf("exec %q: %v", query, err)
	}
}

func (f *flattenFKFixture) count(query string, args ...any) int {
	f.t.Helper()
	var n int
	if err := f.db.QueryRowContext(f.ctx, query, args...).Scan(&n); err != nil {
		f.t.Fatalf("%s: %v", query, err)
	}
	return n
}

// scan runs the doctor's severed-FK scan.
func (f *flattenFKFixture) scan() []SeveredCloneLocalFK {
	f.t.Helper()
	severed, err := scanSeveredCloneLocalFKs(f.db)
	if err != nil {
		f.t.Fatalf("scan clone-local FKs: %v", err)
	}
	return severed
}

func (f *flattenFKFixture) head() string {
	f.t.Helper()
	var h string
	if err := f.db.QueryRowContext(f.ctx, "SELECT HASHOF('HEAD')").Scan(&h); err != nil {
		f.t.Fatalf("read HEAD: %v", err)
	}
	return h
}

// flatten runs the store's Flatten and checks the outcome every flatten
// must have: the pre-flatten snapshot under the initial commit plus one
// snapshot commit.
func (f *flattenFKFixture) flatten() {
	f.t.Helper()
	head := f.head()
	if err := f.store.Flatten(f.ctx); err != nil {
		f.t.Fatalf("Flatten: %v", err)
	}
	if n := f.count("SELECT COUNT(*) FROM dolt_log"); n != 2 {
		f.t.Errorf("history after flatten = %d commits, want the initial commit plus the flattened snapshot", n)
	}
	if n := f.count("SELECT COUNT(*) FROM dolt_diff_summary(?, 'HEAD')", head); n != 0 {
		f.t.Errorf("flattened HEAD differs from the pre-flatten HEAD in %d table(s), want the same snapshot", n)
	}
}

// wantRefused checks insert fails on a foreign key.
func (f *flattenFKFixture) wantRefused(insert string) {
	f.t.Helper()
	if _, err := f.db.ExecContext(f.ctx, insert); err == nil || !strings.Contains(strings.ToLower(err.Error()), "foreign key") {
		f.t.Errorf("%s: err = %v, want a foreign key violation", insert, err)
	}
}

// TestFlattenKeepsCloneLocalFKs_Server: Flatten on a dolt sql-server keeps
// every clone-local FK enforcing and cascading (bd-7bpkd); its old final
// DOLT_RESET('--hard') dropped all eight.
func TestFlattenKeepsCloneLocalFKs_Server(t *testing.T) {
	f := newFlattenFKFixture(t)

	f.flatten()

	if severed := f.scan(); len(severed) != 0 {
		t.Fatalf("severed clone-local FKs after flatten = %v, want none", severed)
	}
	f.wantRefused("INSERT INTO events (id, issue_id, event_type, actor) VALUES ('11111111-1111-1111-1111-111111111111', 'fl-no-such-issue', 'created', 'test')")
	f.wantRefused("INSERT INTO wisp_labels (issue_id, label) VALUES ('fl-no-such-wisp', 'orphan')")

	if f.count("SELECT COUNT(*) FROM events WHERE issue_id = 'fl-one'") == 0 ||
		f.count("SELECT COUNT(*) FROM wisp_labels WHERE issue_id = 'fl-wisp'") == 0 {
		t.Fatalf("clone-local children missing before the cascade check")
	}
	f.exec("DELETE FROM issues WHERE id = 'fl-one'")
	f.exec("DELETE FROM wisps WHERE id = 'fl-wisp'")
	if n := f.count("SELECT COUNT(*) FROM events WHERE issue_id = 'fl-one'"); n != 0 {
		t.Errorf("events rows of a deleted issue = %d, want the cascade to remove them", n)
	}
	if n := f.count("SELECT COUNT(*) FROM wisp_labels WHERE issue_id = 'fl-wisp'"); n != 0 {
		t.Errorf("wisp_labels rows of a deleted wisp = %d, want the cascade to remove them", n)
	}
}
