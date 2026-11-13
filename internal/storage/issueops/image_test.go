package issueops

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	doltembed "github.com/dolthub/driver/v2"

	publicops "github.com/steveyegge/beads/issueops"
)

func imageFixture(reverse bool) publicops.Image {
	labels := []map[string]any{
		{"issue_id": "bd-1", "label": "a"},
		{"issue_id": "bd-1", "label": "b"},
	}
	events := []map[string]any{
		{"id": "e1", "issue_id": "bd-1", "event_type": "created", "comment": nil},
		{"id": "e2", "issue_id": "bd-1", "event_type": "closed", "comment": nil},
	}
	row := map[string]any{}
	keys := []string{"id", "title", "metadata", "priority", "closed_at"}
	vals := []any{"bd-1", "T <&>", `{"k": 1}`, int64(2), nil}
	if reverse {
		labels[0], labels[1] = labels[1], labels[0]
		events[0], events[1] = events[1], events[0]
		for i := len(keys) - 1; i >= 0; i-- {
			row[keys[i]] = vals[i]
		}
	} else {
		for i := range keys {
			row[keys[i]] = vals[i]
		}
	}
	return publicops.Image{ID: "bd-1", Table: "issues", Row: row, Labels: labels, Events: events}
}

func TestCanonicalImageJSONStableAndOrderIndependent(t *testing.T) {
	a, err := CanonicalImageJSON(imageFixture(false))
	if err != nil {
		t.Fatal(err)
	}
	again, err := CanonicalImageJSON(imageFixture(false))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, again) {
		t.Fatalf("two calls differ:\n%s\n%s", a, again)
	}
	for i := 0; i < 20; i++ {
		b, err := CanonicalImageJSON(imageFixture(true))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(a, b) {
			t.Fatalf("row or key order changed the canonical form:\n%s\n%s", a, b)
		}
	}
	want := `{"comments":[],"dependencies":[],"events":[{"comment":null,"event_type":"created","id":"e1","issue_id":"bd-1"},{"comment":null,"event_type":"closed","id":"e2","issue_id":"bd-1"}],` +
		`"id":"bd-1","labels":[{"issue_id":"bd-1","label":"a"},{"issue_id":"bd-1","label":"b"}],` +
		`"row":{"closed_at":null,"id":"bd-1","metadata":"{\"k\": 1}","priority":2,"title":"T <&>"},"table":"issues"}`
	if string(a) != want {
		t.Fatalf("canonical form\n got %s\nwant %s", a, want)
	}

	// The caller's slices are not reordered.
	img := imageFixture(true)
	if _, err := CanonicalImageJSON(img); err != nil {
		t.Fatal(err)
	}
	if img.Labels[0]["label"] != "b" {
		t.Fatalf("CanonicalImageJSON reordered the caller's labels: %v", img.Labels)
	}
}

func TestComputeImageRevisionIgnoresRevisionField(t *testing.T) {
	img := imageFixture(false)
	rev := ComputeImageRevision(img)
	if len(rev) != 64 {
		t.Fatalf("revision %q is not sha256 hex", rev)
	}
	img.Revision = "stale"
	if got := ComputeImageRevision(img); got != rev {
		t.Fatalf("revision depends on the Revision field: %s vs %s", got, rev)
	}
	withRev, err := CanonicalImageJSON(img)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(withRev, []byte(`"revision":"stale"`)) {
		t.Fatalf("canonical form drops a set revision: %s", withRev)
	}
	img.Revision = ""
	img.Labels = append(img.Labels, map[string]any{"issue_id": "bd-1", "label": "c"})
	if ComputeImageRevision(img) == rev {
		t.Fatal("a label add did not change the revision")
	}
}

func TestImageInTxMissingIDsNamedTogether(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM wisps LIMIT 1")).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM issues WHERE id IN (?,?,?)")).
		WithArgs("bd-1", "bd-x", "bd-y").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("bd-1"))

	images, err := ImageInTx(context.Background(), db, []string{"bd-1", "bd-x", "bd-1", "bd-y"})
	if images != nil {
		t.Fatalf("partial result returned: %v", images)
	}
	if !errors.Is(err, publicops.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	var nf *publicops.NotFoundError
	if !errors.As(err, &nf) || len(nf.IDs) != 2 || nf.IDs[0] != "bd-x" || nf.IDs[1] != "bd-y" {
		t.Fatalf("err = %#v, want both missing ids", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestImageInTxValidation(t *testing.T) {
	for _, ids := range [][]string{nil, {"bd-1", " "}} {
		if _, err := ImageInTx(context.Background(), nil, ids); !errors.Is(err, publicops.ErrValidation) {
			t.Fatalf("ids %q: err = %v, want ErrValidation", ids, err)
		}
	}
}

// showColumns answers SHOW COLUMNS; each row is field, type, extra.
func showColumns(rows ...[3]string) *sqlmock.Rows {
	out := sqlmock.NewRows([]string{"Field", "Type", "Null", "Key", "Default", "Extra"})
	for _, r := range rows {
		out.AddRow(r[0], r[1], "YES", "", nil, r[2])
	}
	return out
}

// expectColumns expects one table's discovery: SHOW COLUMNS.
func expectColumns(mock sqlmock.Sqlmock, table string, cols ...[3]string) {
	mock.ExpectQuery(regexp.QuoteMeta("SHOW COLUMNS FROM " + table)).WillReturnRows(showColumns(cols...))
}

// imageArmSQL is the arm imageSelect sends to read table's cols where match
// equals one of n requested ids.
func imageArmSQL(table, match, cols string, n int) string {
	which := make([]string, n)
	eq := make([]string, n)
	for i := range which {
		which[i] = fmt.Sprintf("CASE WHEN t.`%s` = ? THEN '%d' END", match, i)
		eq[i] = "t.`" + match + "` = ?"
	}
	return "SELECT CONCAT_WS(',', " + strings.Join(which, ", ") + "), " + cols +
		" FROM " + table + " t WHERE " + strings.Join(eq, " OR ")
}

// TestImageInTxRawRowsFromTextProtocol drives one durable image through the
// text-protocol shape (every value []byte): a stored or virtual generated
// column is neither selected nor imaged, an expression default is not
// mistaken for one, integers
// and timestamps normalize by declared type, and a missing wisp edge table
// means no wisp edges.
func TestImageInTxRawRowsFromTextProtocol(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM wisps LIMIT 1")).WillReturnError(missingTableError("wisps"))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM issues WHERE id IN (?)")).
		WithArgs("bd-1").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("bd-1"))
	expectColumns(mock, "issues",
		[3]string{"id", "varchar(255)", ""},
		[3]string{"priority", "int", ""},
		[3]string{"metadata", "json", "DEFAULT_GENERATED"},
		[3]string{"created_at", "datetime(6)", "DEFAULT_GENERATED on update CURRENT_TIMESTAMP(6)"},
		[3]string{"closed_at", "datetime", ""},
		[3]string{"s", "varchar(255)", "STORED GENERATED"},
		[3]string{"v", "int", "VIRTUAL GENERATED"},
	)
	mock.ExpectQuery(regexp.QuoteMeta(imageArmSQL("issues", "id", "t.`id`, t.`priority`, t.`metadata`, t.`created_at`, t.`closed_at`", 1))).
		WithArgs("bd-1", "bd-1").
		WillReturnRows(sqlmock.NewRows([]string{"which", "id", "priority", "metadata", "created_at", "closed_at"}).
			AddRow([]byte("0"), []byte("bd-1"), []byte("2"), []byte(`{"owner": {"team": "x"}}`), []byte("2026-10-24 17:05:44.120000"), nil))
	expectColumns(mock, "labels", [3]string{"issue_id", "varchar(255)", ""}, [3]string{"label", "varchar(255)", ""})
	mock.ExpectQuery(regexp.QuoteMeta(imageArmSQL("labels", "issue_id", "t.`issue_id`, t.`label`", 1))).
		WithArgs("bd-1", "bd-1").
		WillReturnRows(sqlmock.NewRows([]string{"which", "issue_id", "label"}).
			AddRow([]byte("0"), []byte("bd-1"), []byte("z")).AddRow([]byte("0"), []byte("bd-1"), []byte("a")))
	expectColumns(mock, "comments", [3]string{"id", "char(36)", ""}, [3]string{"issue_id", "varchar(255)", ""})
	mock.ExpectQuery(regexp.QuoteMeta(imageArmSQL("comments", "issue_id", "t.`id`, t.`issue_id`", 1))).
		WithArgs("bd-1", "bd-1").
		WillReturnRows(sqlmock.NewRows([]string{"which", "id", "issue_id"}))
	expectColumns(mock, "events", [3]string{"id", "char(36)", ""}, [3]string{"issue_id", "varchar(255)", ""})
	mock.ExpectQuery(regexp.QuoteMeta(imageArmSQL("events", "issue_id", "t.`id`, t.`issue_id`", 1))).
		WithArgs("bd-1", "bd-1").
		WillReturnRows(sqlmock.NewRows([]string{"which", "id", "issue_id"}))
	expectColumns(mock, "dependencies",
		[3]string{"id", "char(36)", ""},
		[3]string{"issue_id", "varchar(255)", ""},
		[3]string{"depends_on_issue_id", "varchar(255)", ""},
		[3]string{"depends_on_wisp_id", "varchar(255)", ""},
		[3]string{"depends_on_external", "varchar(255)", ""},
	)
	depCols := "t.`id`, t.`issue_id`, t.`depends_on_issue_id`, t.`depends_on_wisp_id`, t.`depends_on_external`"
	mock.ExpectQuery(regexp.QuoteMeta(
		imageArmSQL("dependencies", "issue_id", depCols, 1)+
			" UNION ALL "+imageArmSQL("dependencies", "depends_on_issue_id", depCols, 1)+
			" UNION ALL "+imageArmSQL("dependencies", "depends_on_wisp_id", depCols, 1))).
		WithArgs("bd-1", "bd-1", "bd-1", "bd-1", "bd-1", "bd-1").
		WillReturnRows(sqlmock.NewRows([]string{"which", "id", "issue_id", "depends_on_issue_id", "depends_on_wisp_id", "depends_on_external"}).
			AddRow([]byte("0"), []byte("d1"), []byte("bd-2"), []byte("bd-1"), nil, nil))
	mock.ExpectQuery(regexp.QuoteMeta("SHOW COLUMNS FROM wisp_dependencies")).
		WillReturnError(missingTableError("wisp_dependencies"))

	images, err := ImageInTx(context.Background(), db, []string{"bd-1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	if len(images) != 1 {
		t.Fatalf("got %d images", len(images))
	}
	img := images[0]
	if img.Table != "issues" || img.ID != "bd-1" {
		t.Fatalf("image header = %q %q", img.ID, img.Table)
	}
	for _, col := range []string{"s", "v"} {
		if _, ok := img.Row[col]; ok {
			t.Fatalf("generated column %s imaged: %v", col, img.Row)
		}
	}
	if img.Row["priority"] != int64(2) {
		t.Fatalf("priority = %#v, want int64(2)", img.Row["priority"])
	}
	if img.Row["metadata"] != `{"owner": {"team": "x"}}` {
		t.Fatalf("metadata = %#v, want the stored text", img.Row["metadata"])
	}
	if img.Row["created_at"] != "2026-10-24T17:05:44.120000Z" {
		t.Fatalf("created_at = %#v", img.Row["created_at"])
	}
	if v, ok := img.Row["closed_at"]; !ok || v != nil {
		t.Fatalf("closed_at = %#v (present %v), want null", v, ok)
	}
	if len(img.Labels) != 2 || img.Labels[0]["label"] != "a" {
		t.Fatalf("labels not sorted by key: %v", img.Labels)
	}
	if img.Comments == nil || img.Events == nil || len(img.Dependencies) != 1 {
		t.Fatalf("lists = %v %v %v", img.Comments, img.Events, img.Dependencies)
	}
	if img.Revision != ComputeImageRevision(img) {
		t.Fatal("Revision is not ComputeImageRevision of the image")
	}
}

// TestImageInTxReadsEachTableOncePerBatch images two durable beads with one
// read per table: each row goes to every requested id the database says it
// matched, by index in the batch (bd-2 is 0, bd-1 is 1), an edge between the
// two belongs to both images (as separate maps), and a self-edge is listed
// once.
func TestImageInTxReadsEachTableOncePerBatch(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM wisps LIMIT 1")).WillReturnError(missingTableError("wisps"))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM issues WHERE id IN (?,?)")).
		WithArgs("bd-2", "bd-1").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("bd-1").AddRow("bd-2"))
	expectColumns(mock, "issues", [3]string{"id", "varchar(255)", ""}, [3]string{"title", "text", ""})
	mock.ExpectQuery(regexp.QuoteMeta(imageArmSQL("issues", "id", "t.`id`, t.`title`", 2))).
		WithArgs("bd-2", "bd-1", "bd-2", "bd-1").
		WillReturnRows(sqlmock.NewRows([]string{"which", "id", "title"}).AddRow("1", "bd-1", "one").AddRow("0", "bd-2", "two"))
	expectColumns(mock, "labels", [3]string{"issue_id", "varchar(255)", ""}, [3]string{"label", "varchar(255)", ""})
	mock.ExpectQuery(regexp.QuoteMeta(imageArmSQL("labels", "issue_id", "t.`issue_id`, t.`label`", 2))).
		WithArgs("bd-2", "bd-1", "bd-2", "bd-1").
		WillReturnRows(sqlmock.NewRows([]string{"which", "issue_id", "label"}).
			AddRow("0", "bd-2", "b").AddRow("1", "bd-1", "a").AddRow("0", "bd-2", "a"))
	expectColumns(mock, "comments", [3]string{"id", "char(36)", ""}, [3]string{"issue_id", "varchar(255)", ""})
	mock.ExpectQuery(regexp.QuoteMeta(imageArmSQL("comments", "issue_id", "t.`id`, t.`issue_id`", 2))).
		WithArgs("bd-2", "bd-1", "bd-2", "bd-1").
		WillReturnRows(sqlmock.NewRows([]string{"which", "id", "issue_id"}).AddRow("1", "c1", "bd-1"))
	expectColumns(mock, "events", [3]string{"id", "char(36)", ""}, [3]string{"issue_id", "varchar(255)", ""})
	mock.ExpectQuery(regexp.QuoteMeta(imageArmSQL("events", "issue_id", "t.`id`, t.`issue_id`", 2))).
		WithArgs("bd-2", "bd-1", "bd-2", "bd-1").
		WillReturnRows(sqlmock.NewRows([]string{"which", "id", "issue_id"}))
	expectColumns(mock, "dependencies",
		[3]string{"id", "char(36)", ""},
		[3]string{"issue_id", "varchar(255)", ""},
		[3]string{"depends_on_issue_id", "varchar(255)", ""},
	)
	depCols := "t.`id`, t.`issue_id`, t.`depends_on_issue_id`"
	mock.ExpectQuery(regexp.QuoteMeta(
		imageArmSQL("dependencies", "issue_id", depCols, 2)+
			" UNION ALL "+imageArmSQL("dependencies", "depends_on_issue_id", depCols, 2))).
		WithArgs("bd-2", "bd-1", "bd-2", "bd-1", "bd-2", "bd-1", "bd-2", "bd-1").
		WillReturnRows(sqlmock.NewRows([]string{"which", "id", "issue_id", "depends_on_issue_id"}).
			// Arm one, by issue_id; arm two, by depends_on_issue_id. The
			// self-edge d2 comes from both arms.
			AddRow("0", "d1", "bd-2", "bd-1").
			AddRow("1", "d2", "bd-1", "bd-1").
			AddRow("1", "d1", "bd-2", "bd-1").
			AddRow("1", "d2", "bd-1", "bd-1").
			AddRow("0", "d3", "bd-9", "bd-2"))
	mock.ExpectQuery(regexp.QuoteMeta("SHOW COLUMNS FROM wisp_dependencies")).
		WillReturnError(missingTableError("wisp_dependencies"))

	images, err := ImageInTx(context.Background(), db, []string{"bd-2", "bd-1", "bd-2"})
	if err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	if len(images) != 2 || images[0].ID != "bd-2" || images[1].ID != "bd-1" {
		t.Fatalf("images not in first-mention order: %v", images)
	}
	two, one := images[0], images[1]
	if two.Row["title"] != "two" || one.Row["title"] != "one" {
		t.Fatalf("rows = %v / %v", two.Row, one.Row)
	}
	if len(two.Labels) != 2 || two.Labels[0]["label"] != "a" || len(one.Labels) != 1 {
		t.Fatalf("labels = %v / %v", two.Labels, one.Labels)
	}
	if len(two.Comments) != 0 || len(one.Comments) != 1 || two.Comments == nil || two.Events == nil {
		t.Fatalf("comments = %v / %v, events = %v", two.Comments, one.Comments, two.Events)
	}
	ids := func(rows []map[string]any) string {
		var out []string
		for _, row := range rows {
			out = append(out, row["id"].(string))
		}
		return strings.Join(out, ",")
	}
	if got := ids(two.Dependencies); got != "d1,d3" {
		t.Fatalf("bd-2 edges = %s, want d1,d3", got)
	}
	if got := ids(one.Dependencies); got != "d1,d2" {
		t.Fatalf("bd-1 edges = %s, want d1,d2 (self-edge once)", got)
	}
	for _, img := range images {
		if img.Revision != ComputeImageRevision(img) {
			t.Fatalf("%s: Revision is not ComputeImageRevision of the image", img.ID)
		}
	}
	two.Dependencies[0]["issue_id"] = "changed"
	if one.Dependencies[0]["issue_id"] != "bd-2" {
		t.Fatal("two images share one edge row map")
	}
}

// TestImageInTxMatchesUnderColumnCollation reads images from a real embedded
// Dolt database whose default collation is case-insensitive, where a label,
// comment and edge may name a bead in another case and still be the bead's (the
// foreign keys accept them). A batch must hand each such row to the bead its
// single-id `col = ?` read did, not drop it for differing in bytes; a
// self-edge naming the bead in two cases is listed once.
func TestImageInTxMatchesUnderColumnCollation(t *testing.T) {
	ctx := context.Background()
	cfg, err := doltembed.ParseDSN("file://" + t.TempDir() + "?commitname=test&commitemail=test@example.com")
	if err != nil {
		t.Fatal(err)
	}
	connector, err := doltembed.NewConnector(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer connector.Close()
	pool := sql.OpenDB(connector)
	defer pool.Close()
	conn, err := pool.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for _, stmt := range []string{
		"CREATE DATABASE images COLLATE utf8mb4_0900_ai_ci",
		"USE images",
		"CREATE TABLE issues (id VARCHAR(255) PRIMARY KEY, title TEXT)",
		"CREATE TABLE labels (issue_id VARCHAR(255), label VARCHAR(255), PRIMARY KEY (issue_id, label), FOREIGN KEY (issue_id) REFERENCES issues(id))",
		"CREATE TABLE comments (id VARCHAR(255) PRIMARY KEY, issue_id VARCHAR(255))",
		"CREATE TABLE events (id VARCHAR(255) PRIMARY KEY, issue_id VARCHAR(255))",
		"CREATE TABLE dependencies (id VARCHAR(255) PRIMARY KEY, issue_id VARCHAR(255), depends_on_issue_id VARCHAR(255)," +
			" FOREIGN KEY (issue_id) REFERENCES issues(id), FOREIGN KEY (depends_on_issue_id) REFERENCES issues(id))",
		"INSERT INTO issues VALUES ('bd-000', 'one'), ('bd-200', 'two')",
		"INSERT INTO labels VALUES ('bd-000', 'a'), ('BD-000', 'case-label')",
		"INSERT INTO comments VALUES ('c-case', 'BD-000')",
		"INSERT INTO dependencies VALUES ('edge', 'bd-000', 'bd-200'), ('case-edge', 'bd-200', 'BD-000'), ('self', 'bd-000', 'BD-000')",
	} {
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}

	rowIDs := func(rows []map[string]any, key string) string {
		var out []string
		for _, row := range rows {
			out = append(out, imageText(row[key]))
		}
		return strings.Join(out, ",")
	}
	alone := map[string]publicops.Image{}
	for _, id := range []string{"bd-000", "bd-200"} {
		images, err := ImageInTx(ctx, conn, []string{id})
		if err != nil {
			t.Fatal(err)
		}
		alone[id] = images[0]
	}
	zero := alone["bd-000"]
	if got := rowIDs(zero.Labels, "label"); got != "case-label,a" {
		t.Errorf("bd-000 labels = %s, want case-label,a", got)
	}
	if got := rowIDs(zero.Comments, "id"); got != "c-case" {
		t.Errorf("bd-000 comments = %s, want c-case", got)
	}
	if got := rowIDs(zero.Dependencies, "id"); got != "case-edge,edge,self" {
		t.Errorf("bd-000 edges = %s, want case-edge,edge,self", got)
	}
	if got := rowIDs(alone["bd-200"].Dependencies, "id"); got != "case-edge,edge" {
		t.Errorf("bd-200 edges = %s, want case-edge,edge", got)
	}

	batch, err := ImageInTx(ctx, conn, []string{"bd-200", "bd-000"})
	if err != nil {
		t.Fatal(err)
	}
	for _, img := range batch {
		got, err := CanonicalImageJSON(img)
		if err != nil {
			t.Fatal(err)
		}
		want, err := CanonicalImageJSON(alone[img.ID])
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s read in a batch differs from its read alone:\n got %s\nwant %s", img.ID, got, want)
		}
	}
}

// TestImageInTxMatchesUnderEachColumnsCollation reads wisps whose id column is
// binary while their label, comment and event tables, created after the
// database default changed, are case-insensitive. Each auxiliary row must
// match under its own column's collation, as its single-id `col = ?` read did,
// not under the id column's: bd-wisp-000's rows naming BD-WISP-000 are its
// own. BD-WISP-000, a second wisp under the binary id, owns the same rows, so
// a batch of both gives each image every one of them, in maps of its own.
func TestImageInTxMatchesUnderEachColumnsCollation(t *testing.T) {
	ctx := context.Background()
	cfg, err := doltembed.ParseDSN("file://" + t.TempDir() + "?commitname=test&commitemail=test@example.com")
	if err != nil {
		t.Fatal(err)
	}
	connector, err := doltembed.NewConnector(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer connector.Close()
	pool := sql.OpenDB(connector)
	defer pool.Close()
	conn, err := pool.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	exec := func(stmts ...string) {
		t.Helper()
		for _, stmt := range stmts {
			if _, err := conn.ExecContext(ctx, stmt); err != nil {
				t.Fatalf("%s: %v", stmt, err)
			}
		}
	}
	exec(
		"CREATE DATABASE mixed COLLATE utf8mb4_0900_bin",
		"USE mixed",
		"CREATE TABLE wisps (id VARCHAR(255) PRIMARY KEY, title TEXT)",
		"ALTER DATABASE mixed COLLATE utf8mb4_0900_ai_ci",
		"CREATE TABLE wisp_labels (issue_id VARCHAR(255), label VARCHAR(255), PRIMARY KEY (issue_id, label))",
		"CREATE TABLE wisp_comments (id VARCHAR(255) PRIMARY KEY, issue_id VARCHAR(255))",
		"CREATE TABLE wisp_events (id VARCHAR(255) PRIMARY KEY, issue_id VARCHAR(255))",
		"CREATE TABLE wisp_dependencies (id VARCHAR(255) PRIMARY KEY, issue_id VARCHAR(255) COLLATE utf8mb4_0900_bin,"+
			" depends_on_wisp_id VARCHAR(255) COLLATE utf8mb4_0900_bin,"+
			" FOREIGN KEY (issue_id) REFERENCES wisps(id), FOREIGN KEY (depends_on_wisp_id) REFERENCES wisps(id))",
		"INSERT INTO wisps VALUES ('bd-wisp-000', 'one')",
		"INSERT INTO wisp_labels VALUES ('BD-WISP-000', 'case-label')",
		"INSERT INTO wisp_comments VALUES ('comment', 'BD-WISP-000')",
		"INSERT INTO wisp_events VALUES ('event', 'BD-WISP-000')",
	)

	rowIDs := func(rows []map[string]any, key string) string {
		var out []string
		for _, row := range rows {
			out = append(out, imageText(row[key]))
		}
		return strings.Join(out, ",")
	}
	wantRows := func(img publicops.Image, edges string) {
		t.Helper()
		if got := rowIDs(img.Labels, "label"); got != "case-label" {
			t.Errorf("%s labels = %q, want case-label", img.ID, got)
		}
		if got := rowIDs(img.Comments, "id"); got != "comment" {
			t.Errorf("%s comments = %q, want comment", img.ID, got)
		}
		if got := rowIDs(img.Events, "id"); got != "event" {
			t.Errorf("%s events = %q, want event", img.ID, got)
		}
		if got := rowIDs(img.Dependencies, "id"); got != edges {
			t.Errorf("%s edges = %q, want %q", img.ID, got, edges)
		}
	}
	images, err := ImageInTx(ctx, conn, []string{"bd-wisp-000"})
	if err != nil {
		t.Fatal(err)
	}
	wantRows(images[0], "")

	// The binary id column holds both cases, and the binary edge columns
	// match each only exactly.
	exec(
		"INSERT INTO wisps VALUES ('BD-WISP-000', 'two')",
		"INSERT INTO wisp_dependencies VALUES ('edge', 'bd-wisp-000', 'BD-WISP-000')",
	)
	alone := map[string]publicops.Image{}
	for _, id := range []string{"bd-wisp-000", "BD-WISP-000"} {
		images, err := ImageInTx(ctx, conn, []string{id})
		if err != nil {
			t.Fatal(err)
		}
		wantRows(images[0], "edge")
		if title := imageText(images[0].Row["title"]); title != map[string]string{"bd-wisp-000": "one", "BD-WISP-000": "two"}[id] {
			t.Errorf("%s row title = %q", id, title)
		}
		alone[id] = images[0]
	}
	batch, err := ImageInTx(ctx, conn, []string{"BD-WISP-000", "bd-wisp-000"})
	if err != nil {
		t.Fatal(err)
	}
	for _, img := range batch {
		got, err := CanonicalImageJSON(img)
		if err != nil {
			t.Fatal(err)
		}
		want, err := CanonicalImageJSON(alone[img.ID])
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s read in a batch differs from its read alone:\n got %s\nwant %s", img.ID, got, want)
		}
	}
	batch[0].Labels[0]["label"] = "changed"
	if batch[1].Labels[0]["label"] != "case-label" {
		t.Error("two images share one label row map")
	}
}

func TestNormalizeImageValueDriverShapesAgree(t *testing.T) {
	cases := []struct {
		typ  string
		a, b any
	}{
		{"int", []byte("7"), int64(7)},
		{"bigint unsigned", []byte("7"), uint64(7)},
		{"tinyint(1)", "1", int64(1)},
		{"double", []byte("1.5"), 1.5},
		{"datetime", []byte("2026-01-02 03:04:05"), "2026-01-02T03:04:05Z"},
		{"varchar(255)", []byte("x"), "x"},
	}
	for _, c := range cases {
		a, err := normalizeImageValue(c.typ, c.a)
		if err != nil {
			t.Fatal(err)
		}
		b, err := normalizeImageValue(c.typ, c.b)
		if err != nil {
			t.Fatal(err)
		}
		if a != b {
			t.Fatalf("%s: %#v vs %#v", c.typ, a, b)
		}
	}
}
