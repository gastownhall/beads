//go:build cgo

package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	storeissueops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/testutil"
	"github.com/steveyegge/beads/issueops"
)

// `bd image` on three routes over ONE dataset.
//
// Byte equality across routes needs the same rows, and an embedded workspace
// and a server database never share rows: ids, timestamps and row_lock tokens
// differ. So the data is written once, by an embedded workspace, and then a
// dolt sql-server owned by this test serves that same data directory to a
// server-mode workspace (the direct DoltStore route) and a proxied-server
// workspace (the unit-of-work route). Every embedded read runs before the
// server starts, because the two cannot hold the directory at once.

// imageRoute runs bd on one route and returns its streams.
type imageRoute struct {
	name string
	run  func(t *testing.T, args ...string) (stdout, stderr string, err error)
}

func (r imageRoute) image(t *testing.T, args ...string) string {
	t.Helper()
	stdout, stderr, err := r.run(t, append([]string{"image"}, args...)...)
	if err != nil {
		t.Fatalf("%s: bd image %s failed: %v\nstdout:\n%s\nstderr:\n%s",
			r.name, strings.Join(args, " "), err, stdout, stderr)
	}
	return stdout
}

func embeddedImageRoute(bd, dir string) imageRoute {
	return imageRoute{name: "embedded", run: func(t *testing.T, args ...string) (string, string, error) {
		t.Helper()
		cmd := exec.Command(bd, args...)
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		return stdout.String(), stderr.String(), err
	}}
}

// startImageDoltServer serves dataDir on a free local port until the test
// ends. It is this test's own server, not the shared container, because only
// a server on this host can serve the embedded workspace's directory.
func startImageDoltServer(t *testing.T, dataDir string) int {
	t.Helper()
	port, err := testutil.FindFreePort()
	if err != nil {
		t.Fatalf("find free port: %v", err)
	}
	env := doltTestEnv(t)
	var logs bytes.Buffer
	cmd := exec.Command("dolt", "sql-server", "--host", "127.0.0.1", "--port", strconv.Itoa(port))
	cmd.Dir = dataDir
	cmd.Env = env
	cmd.Stdout = &logs
	cmd.Stderr = &logs
	if err := cmd.Start(); err != nil {
		t.Fatalf("start dolt sql-server: %v", err)
	}
	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(exited)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-exited:
		case <-time.After(20 * time.Second):
			_ = cmd.Process.Kill()
			<-exited
		}
	})
	if !testutil.WaitForServer(port, 60*time.Second) {
		t.Fatalf("dolt sql-server on %s did not come up:\n%s", dataDir, logs.String())
	}
	return port
}

// doltTestEnv is the environment for this test's own dolt processes: a
// private DOLT_ROOT_PATH carrying the identity dolt refuses to start without.
func doltTestEnv(t *testing.T) []string {
	t.Helper()
	env := append(envWithout(os.Environ(), "DOLT_ROOT_PATH"), "DOLT_ROOT_PATH="+t.TempDir())
	for _, kv := range [][2]string{{"user.name", "bd-test"}, {"user.email", "bd-test@example.com"}} {
		cmd := exec.Command("dolt", "config", "--global", "--add", kv[0], kv[1])
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("dolt config %s: %v\n%s", kv[0], err, out)
		}
	}
	return env
}

// serverModeImageRoute inits a `bd init --server` workspace on the test's
// server, adopting the database the embedded workspace created.
func serverModeImageRoute(t *testing.T, bd string, port int, database string) imageRoute {
	t.Helper()
	r, _ := serverModeImageRouteDir(t, bd, port, database)
	return r
}

// serverModeImageRouteDir is serverModeImageRoute that also returns the
// workspace directory.
func serverModeImageRouteDir(t *testing.T, bd string, port int, database string) (imageRoute, string) {
	t.Helper()
	dir := t.TempDir()
	initGitRepoAt(t, dir)
	cmd := exec.Command(bd, "init", "--quiet", "--server",
		"--server-host", "127.0.0.1",
		"--server-port", strconv.Itoa(port),
		"--database", database,
		"--prefix", database,
		"--non-interactive", "--skip-agents", "--skip-hooks")
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd init --server failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	return imageRoute{name: "direct", run: func(t *testing.T, args ...string) (string, string, error) {
		t.Helper()
		cmd := exec.Command(bd, args...)
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		return stdout.String(), stderr.String(), err
	}}, dir
}

func proxiedImageRoute(t *testing.T, bd string, port int, database string) imageRoute {
	t.Helper()
	r, _ := proxiedImageRouteDir(t, bd, port, database)
	return r
}

// proxiedImageRouteDir is proxiedImageRoute that also returns the workspace
// directory.
func proxiedImageRouteDir(t *testing.T, bd string, port int, database string) (imageRoute, string) {
	t.Helper()
	p := bdProxiedInit(t, bd, database,
		"--database", database,
		"--proxied-server-external-host", "127.0.0.1",
		"--proxied-server-external-port", strconv.Itoa(port))
	return imageRoute{name: "proxied", run: func(t *testing.T, args ...string) (string, string, error) {
		t.Helper()
		return bdProxiedRunBuffers(t, bd, p.dir, args...)
	}}, p.dir
}

// decodeImageLines parses `bd image` output. Numbers stay json.Number so a
// re-encoded image is byte-identical and its revision can be recomputed.
func decodeImageLines(t *testing.T, out string) []issueops.Image {
	t.Helper()
	var images []issueops.Image
	for _, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		dec := json.NewDecoder(strings.NewReader(line))
		dec.UseNumber()
		var img issueops.Image
		if err := dec.Decode(&img); err != nil {
			t.Fatalf("decode image line %q: %v", line, err)
		}
		canonical, err := storeissueops.CanonicalImageJSON(img)
		if err != nil {
			t.Fatal(err)
		}
		if string(canonical) != line {
			t.Fatalf("line is not the canonical form:\n got %s\nwant %s", line, canonical)
		}
		if got := storeissueops.ComputeImageRevision(img); got != img.Revision {
			t.Fatalf("image %s revision %s, ComputeImageRevision says %s", img.ID, img.Revision, got)
		}
		images = append(images, img)
	}
	return images
}

func imageHasDependency(img issueops.Image, issueID, dependsOn string) bool {
	for _, row := range img.Dependencies {
		if row["issue_id"] == issueID && row["depends_on_issue_id"] == dependsOn {
			return true
		}
	}
	return false
}

// assertImageMissing runs a request naming absent ids and checks the refusal:
// exit 1, nothing on stdout, every missing id on stderr.
func assertImageMissing(t *testing.T, r imageRoute, present string) {
	t.Helper()
	stdout, stderr, err := r.run(t, "image", present, "imgr-nope1", "imgr-nope2")
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		t.Fatalf("%s: missing ids: err = %v, want exit 1\nstderr:\n%s", r.name, err, stderr)
	}
	if stdout != "" {
		t.Errorf("%s: missing ids printed to stdout:\n%s", r.name, stdout)
	}
	for _, id := range []string{"imgr-nope1", "imgr-nope2"} {
		if !strings.Contains(stderr, id) {
			t.Errorf("%s: stderr does not name %s:\n%s", r.name, id, stderr)
		}
	}
}

// assertImageFromFile checks --from-file against the same ids as arguments.
// The file carries a comment, a blank line and padding, as delete's does.
func assertImageFromFile(t *testing.T, r imageRoute, want string, ids ...string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ids.txt")
	body := "# images to archive\n\n"
	for _, id := range ids {
		body += "  " + id + "  \n"
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := r.image(t, "--from-file", path); got != want {
		t.Errorf("%s: --from-file output differs from the argument form:\n got %s\nwant %s", r.name, got, want)
	}
}

// writeBetweenReads runs write once, just before the batch's durable edge
// read for trigger: after the transaction has read every requested id's own
// row, labels, comments and events, and before it reads any edge.
type writeBetweenReads struct {
	storeissueops.DBTX
	trigger string
	write   func()
	fired   bool
}

func (w *writeBetweenReads) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if !w.fired && strings.Contains(query, " FROM dependencies t WHERE ") && slices.Contains(args, any(w.trigger)) {
		w.fired = true
		w.write()
	}
	return w.DBTX.QueryContext(ctx, query, args...)
}

func TestImageRoutesAgreeOnOneDataset(t *testing.T) {
	requireProxiedServerEnv(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	const database = "imgr"
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", database)
	a := bdCreate(t, bd, dir, "imaged durable", "--description", "body <&> \"quoted\"")
	b := bdCreate(t, bd, dir, "depends on the imaged durable")
	w := bdCreate(t, bd, dir, "imaged wisp", "--ephemeral")
	c := bdCreate(t, bd, dir, "snapshot first")
	d := bdCreate(t, bd, dir, "snapshot second")
	for _, args := range [][]string{
		{"label", "add", a.ID, "archive"},
		{"comments", "add", a.ID, "a comment"},
		{"dep", "add", b.ID, a.ID},
		{"label", "add", w.ID, "wisp-label"},
	} {
		runClassic(t, bd, dir, args...)
	}

	embedded := embeddedImageRoute(bd, dir)
	embeddedOut := embedded.image(t, a.ID, w.ID, a.ID)
	assertImageMissing(t, embedded, a.ID)
	assertImageFromFile(t, embedded, embeddedOut, a.ID, w.ID)

	port := startImageDoltServer(t, filepath.Join(beadsDir, "embeddeddolt"))
	direct := serverModeImageRoute(t, bd, port, database)
	proxied := proxiedImageRoute(t, bd, port, database)

	directOut := direct.image(t, a.ID, w.ID, a.ID)
	proxiedOut := proxied.image(t, a.ID, w.ID, a.ID)
	if directOut != embeddedOut {
		t.Errorf("direct and embedded lines differ:\ndirect:   %s\nembedded: %s", directOut, embeddedOut)
	}
	if proxiedOut != embeddedOut {
		t.Errorf("proxied and embedded lines differ:\nproxied:  %s\nembedded: %s", proxiedOut, embeddedOut)
	}

	images := decodeImageLines(t, embeddedOut)
	if len(images) != 2 || images[0].ID != a.ID || images[1].ID != w.ID {
		t.Fatalf("want images of %s then %s (duplicate collapsed), got %d lines:\n%s", a.ID, w.ID, len(images), embeddedOut)
	}
	durable, wisp := images[0], images[1]
	if durable.Table != "issues" || durable.Row["description"] != "body <&> \"quoted\"" {
		t.Errorf("durable image: table %q, description %v", durable.Table, durable.Row["description"])
	}
	if len(durable.Labels) != 1 || durable.Labels[0]["label"] != "archive" {
		t.Errorf("durable labels = %v", durable.Labels)
	}
	if len(durable.Comments) != 1 || durable.Comments[0]["text"] != "a comment" {
		t.Errorf("durable comments = %v", durable.Comments)
	}
	if !imageHasDependency(durable, b.ID, a.ID) {
		t.Errorf("durable image misses the inbound edge from %s: %v", b.ID, durable.Dependencies)
	}
	if wisp.Table != "wisps" || len(wisp.Labels) != 1 || wisp.Labels[0]["label"] != "wisp-label" {
		t.Errorf("wisp image: table %q, labels %v", wisp.Table, wisp.Labels)
	}

	for _, r := range []imageRoute{direct, proxied} {
		assertImageMissing(t, r, a.ID)
		assertImageFromFile(t, r, embeddedOut, a.ID, w.ID)
	}

	// ONE TRANSACTION: a write committed by a second connection between two
	// reads of one call shows in every part of both images or in none. The
	// write is an edge d -> c, which lands in both images' Dependencies and
	// also changes a part read earlier (its event or a row). It runs through
	// ImageInTx on one transaction — the body all three roles wrap in their
	// one read transaction — with the write hooked in front of the edge read,
	// after every other part is read. Each image must then be the one from
	// before the write or the one from after it, both from the same side: a
	// read that is not one snapshot mixes the earlier parts from before with
	// the edges from after, and matches neither.
	t.Run("one_transaction", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		db, err := sql.Open("mysql", fmt.Sprintf("root:@tcp(127.0.0.1:%d)/%s?parseTime=true", port, database))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		before, err := storeissueops.ImageInTx(ctx, db, []string{c.ID, d.ID})
		if err != nil {
			t.Fatal(err)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()
		hooked := &writeBetweenReads{DBTX: tx, trigger: d.ID, write: func() {
			if stdout, stderr, err := direct.run(t, "dep", "add", d.ID, c.ID); err != nil {
				t.Fatalf("mid-read dep add: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
			}
		}}
		snap, err := storeissueops.ImageInTx(ctx, hooked, []string{c.ID, d.ID})
		if err != nil {
			t.Fatal(err)
		}
		if !hooked.fired {
			t.Fatal("the write never ran between the reads")
		}
		after, err := storeissueops.ImageInTx(ctx, db, []string{c.ID, d.ID})
		if err != nil {
			t.Fatal(err)
		}
		withoutEdges := func(img issueops.Image) string {
			img.Dependencies, img.Revision = nil, ""
			b, err := storeissueops.CanonicalImageJSON(img)
			if err != nil {
				t.Fatal(err)
			}
			return string(b)
		}
		if withoutEdges(before[0]) == withoutEdges(after[0]) && withoutEdges(before[1]) == withoutEdges(after[1]) {
			t.Fatal("the write changed nothing but the edges, so a mixed read would go unseen")
		}
		side := func(i int) string {
			switch snap[i].Revision {
			case before[i].Revision:
				return "before"
			case after[i].Revision:
				return "after"
			}
			return "neither"
		}
		if side(0) == "neither" || side(0) != side(1) {
			t.Fatalf("one read is not one snapshot: %s's image is from %s the write, %s's from %s", c.ID, side(0), d.ID, side(1))
		}
		t.Logf("one read saw the state from %s the mid-read write (before is the snapshot)", side(0))

		// The write did land: a fresh read on every route sees it in both.
		fresh := decodeImageLines(t, proxied.image(t, c.ID, d.ID))
		if !imageHasDependency(fresh[0], d.ID, c.ID) || !imageHasDependency(fresh[1], d.ID, c.ID) {
			t.Fatalf("the committed edge is missing from a fresh read: %v / %v", fresh[0].Dependencies, fresh[1].Dependencies)
		}
		if got := direct.image(t, c.ID, d.ID); got != proxied.image(t, c.ID, d.ID) {
			t.Errorf("direct and proxied differ after the write")
		}
	})
}

// imageStartupState is everything a bd invocation's startup or post-command
// maintenance writes after a CLI upgrade: the version metadata, both schema
// cursors, the Dolt history an auto-commit extends, and the workspace's
// version marker.
type imageStartupState struct {
	bdVersion, cursor, ignoredCursor, commits, localVersion string
}

// imageStateReader runs one scalar query against a route's database.
type imageStateReader func(t *testing.T, query string) string

func readImageStartupState(t *testing.T, query imageStateReader, workspace string) imageStartupState {
	t.Helper()
	marker, err := os.ReadFile(filepath.Join(workspace, ".beads", localVersionFile))
	if err != nil {
		t.Fatalf("read %s: %v", localVersionFile, err)
	}
	return imageStartupState{
		bdVersion:     query(t, "SELECT value FROM local_metadata WHERE `key` = 'bd_version'"),
		cursor:        query(t, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations"),
		ignoredCursor: query(t, "SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations"),
		commits:       query(t, "SELECT COUNT(*) FROM dolt_log"),
		localVersion:  string(marker),
	}
}

// imageUpgradedFrom is the version the simulated upgrade starts from: older
// than any binary under test, but current-era (1.0 or later), so the legacy
// upgrade guard admits the workspace instead of refusing a cross-era one.
const imageUpgradedFrom = "1.0.0"

// seedImageUpgrade makes the next bd invocation in workspace look like the
// first after a CLI upgrade from imageUpgradedFrom: the marker trackBdVersion
// compares and the version metadata the post-upgrade migration and
// reconciliation rewrite.
func seedImageUpgrade(t *testing.T, exec imageStateReader, workspace string) {
	t.Helper()
	const oldVersion = imageUpgradedFrom
	if err := os.WriteFile(filepath.Join(workspace, ".beads", localVersionFile), []byte(oldVersion+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	exec(t, "UPDATE local_metadata SET value = '"+oldVersion+"' WHERE `key` = 'bd_version'")
}

// assertImageWritesNothing runs an image and a missing-id image on r after a
// simulated upgrade and checks that neither changed anything.
func assertImageWritesNothing(t *testing.T, r imageRoute, query imageStateReader, workspace, id string) {
	t.Helper()
	seedImageUpgrade(t, query, workspace)
	before := readImageStartupState(t, query, workspace)
	if before.bdVersion != imageUpgradedFrom {
		t.Fatalf("%s: seeded bd_version = %q", r.name, before.bdVersion)
	}
	decodeImageLines(t, r.image(t, id))
	assertImageMissing(t, r, id)
	if after := readImageStartupState(t, query, workspace); after != before {
		t.Errorf("%s: bd image wrote during startup or maintenance:\nbefore %+v\nafter  %+v", r.name, before, after)
	}
}

// doltCLIReader queries an embedded database directory with the dolt CLI. It
// is used only while no bd process and no server holds the directory.
func doltCLIReader(dbDir string, env []string) imageStateReader {
	return func(t *testing.T, query string) string {
		t.Helper()
		cmd := exec.Command("dolt", "sql", "-r", "csv", "-q", query)
		cmd.Dir = dbDir
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("dolt sql %q: %v\n%s", query, err, out)
		}
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		if strings.HasPrefix(strings.ToUpper(query), "UPDATE") {
			return ""
		}
		return lines[len(lines)-1]
	}
}

func sqlReader(db *sql.DB) imageStateReader {
	return func(t *testing.T, query string) string {
		t.Helper()
		if strings.HasPrefix(strings.ToUpper(query), "UPDATE") {
			if _, err := db.Exec(query); err != nil {
				t.Fatalf("%s: %v", query, err)
			}
			return ""
		}
		var v string
		if err := db.QueryRow(query).Scan(&v); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		return v
	}
}

// TestImageStartupWritesNothing pins that after a CLI upgrade, `bd image`
// (and its missing-id refusal) must leave the version metadata, the schema
// cursors, the Dolt history and the workspace version marker as they were, on
// every route. As a classified read it would rewrite
// bd_version through the post-upgrade reconciliation, and .local_version
// through version tracking. (A 0.56.0 seed passes the proxied route but is a
// cross-era server workspace the direct route rightly refuses before any
// write, so the fixture starts from 1.0.0.)
func TestImageStartupWritesNothing(t *testing.T) {
	requireProxiedServerEnv(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	const database = "imnw"
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", database)
	bead := bdCreate(t, bd, dir, "read-only image probe")
	dataDir := filepath.Join(beadsDir, "embeddeddolt")

	assertImageWritesNothing(t, embeddedImageRoute(bd, dir),
		doltCLIReader(filepath.Join(dataDir, database), doltTestEnv(t)), dir, bead.ID)

	port := startImageDoltServer(t, dataDir)
	direct, directDir := serverModeImageRouteDir(t, bd, port, database)
	proxied, proxiedDir := proxiedImageRouteDir(t, bd, port, database)
	db, err := sql.Open("mysql", fmt.Sprintf("root:@tcp(127.0.0.1:%d)/%s?parseTime=true", port, database))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	assertImageWritesNothing(t, direct, sqlReader(db), directDir, bead.ID)
	assertImageWritesNothing(t, proxied, sqlReader(db), proxiedDir, bead.ID)
}

// imageBackupEnv turns auto-backup on and makes it due on every command.
var imageBackupEnv = []string{"BD_BACKUP_ENABLED=true", "BD_BACKUP_INTERVAL=1ms"}

// assertImageBacksNothingUp runs an image and a missing-id image with
// auto-backup enabled and due, and checks that neither attempted a backup:
// no .beads/backup directory (backup_state.json is written there even after a
// refused backup) and no backup remote registered in the database.
func assertImageBacksNothingUp(t *testing.T, r imageRoute, query imageStateReader, workspace, id string) {
	t.Helper()
	backupDir := filepath.Join(workspace, ".beads", "backup")
	if _, err := os.Stat(backupDir); !os.IsNotExist(err) {
		t.Fatalf("%s: %s exists before the image (err %v)", r.name, backupDir, err)
	}
	remotes := query(t, "SELECT COUNT(*) FROM dolt_backups")
	decodeImageLines(t, r.image(t, id))
	assertImageMissing(t, r, id)
	if entries, err := os.ReadDir(backupDir); !os.IsNotExist(err) {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("%s: bd image wrote backup files under %s: %v (err %v)", r.name, backupDir, names, err)
	}
	if after := query(t, "SELECT COUNT(*) FROM dolt_backups"); after != remotes {
		t.Errorf("%s: bd image registered a backup remote: dolt_backups %s -> %s", r.name, remotes, after)
	}
}

// TestImageAutoBackupWritesNothing pins that with auto-backup enabled and
// due, `bd image` and its missing-id refusal must not back up or write backup
// state on any route. Were the direct and embedded post-run maintenance to run
// auto-backup unconditionally, a workspace with backup on would get
// backup_state.json from every image — on the embedded route even when the
// write-refusing preview store made the backup itself fail.
func TestImageAutoBackupWritesNothing(t *testing.T) {
	requireProxiedServerEnv(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	const database = "imbk"
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", database)
	bead := bdCreate(t, bd, dir, "read-only image backup probe")
	dataDir := filepath.Join(beadsDir, "embeddeddolt")

	runIn := func(name, workspace string) imageRoute {
		return imageRoute{name: name, run: func(t *testing.T, args ...string) (string, string, error) {
			t.Helper()
			cmd := exec.Command(bd, args...)
			cmd.Dir = workspace
			cmd.Env = append(bdEnv(workspace), imageBackupEnv...)
			stdout, stderr, err := runCommandBuffers(t, cmd)
			return stdout.String(), stderr.String(), err
		}}
	}

	assertImageBacksNothingUp(t, runIn("embedded", dir),
		doltCLIReader(filepath.Join(dataDir, database), doltTestEnv(t)), dir, bead.ID)

	port := startImageDoltServer(t, dataDir)
	_, directDir := serverModeImageRouteDir(t, bd, port, database)
	_, proxiedDir := proxiedImageRouteDir(t, bd, port, database)
	db, err := sql.Open("mysql", fmt.Sprintf("root:@tcp(127.0.0.1:%d)/%s?parseTime=true", port, database))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	assertImageBacksNothingUp(t, runIn("direct", directDir), sqlReader(db), directDir, bead.ID)
	proxied := imageRoute{name: "proxied", run: func(t *testing.T, args ...string) (string, string, error) {
		t.Helper()
		return bdProxiedRunBuffersWithEnv(t, bd, proxiedDir, imageBackupEnv, args...)
	}}
	assertImageBacksNothingUp(t, proxied, sqlReader(db), proxiedDir, bead.ID)
}
