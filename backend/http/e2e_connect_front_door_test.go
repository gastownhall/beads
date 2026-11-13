//go:build cgo

// The bd connect front-door matrix (G3), run: every case in
// testdata/connect_front_door.yaml (classified by
// TestConnectFrontDoorClassifiesEveryCommand) driven through the real bd
// binary over `bd connect`, against an in-process server over embedded Dolt,
// and compared with embedded workspaces seeded the same way.
//
// Each part builds one server (startE2EServerWithStore, behind a counting
// proxy for per-case request diagnostics) and three workspaces: the served
// workspace S, an embedded twin T and an embedded oracle O. works cases run on
// S and T and must agree; refuses and pending-ruling cases run on S, must not
// write it (checked: the server's HEAD and working set, its config and wisps
// by content, and S's .beads; frontDoorStoreState says what is not compared),
// and run on O only to prove the argv is not broken. So S and T stay
// identical by construction and no case's outcome depends on its order in its
// part.
//
// The parts are declared consecutively so the binary's round-robin sharding
// deals them to distinct shards of //backend/http:http_served_test.
package bdhttp_test

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/types"
)

func TestE2E_ConnectFrontDoorMatrixPart1(t *testing.T) { testConnectFrontDoorMatrix(t, 1) }
func TestE2E_ConnectFrontDoorMatrixPart2(t *testing.T) { testConnectFrontDoorMatrix(t, 2) }
func TestE2E_ConnectFrontDoorMatrixPart3(t *testing.T) { testConnectFrontDoorMatrix(t, 3) }
func TestE2E_ConnectFrontDoorMatrixPart4(t *testing.T) { testConnectFrontDoorMatrix(t, 4) }

// frontDoorPartTests lists the Part tests above, in order. Every part checks
// it against frontDoorParts (frontDoorPartsDeclaredErr) before the engine
// gate, so raising the constant without declaring the new Part test, or
// dropping a Part test, fails instead of silently not running its cases.
// (A function, not a var: the Part tests call back into the check.)
func frontDoorPartTests() []func(*testing.T) {
	return []func(*testing.T){
		TestE2E_ConnectFrontDoorMatrixPart1,
		TestE2E_ConnectFrontDoorMatrixPart2,
		TestE2E_ConnectFrontDoorMatrixPart3,
		TestE2E_ConnectFrontDoorMatrixPart4,
	}
}

func frontDoorFuncName(fn func(*testing.T)) string {
	name := runtime.FuncForPC(reflect.ValueOf(fn).Pointer()).Name()
	return name[strings.LastIndex(name, ".")+1:]
}

const (
	// frontDoorBDTimeout bounds every bd run; a hung command reports exit -1.
	frontDoorBDTimeout = 90 * time.Second
	// frontDoorListenTimeout bounds how long an observed listens case waits
	// for its listening line.
	frontDoorListenTimeout = 30 * time.Second
	frontDoorStopTimeout   = 15 * time.Second
	frontDoorFooter        = "if this change is intended (a native fix), flip the case in backend/http/" + connectFrontDoorPath + " and cite its decision record"
)

// frontDoorSide is one workspace: its directory (also its HOME) and its own
// assets directory, which {file:NAME} expands into.
type frontDoorSide struct {
	name, dir, files string
}

type frontDoorResult struct {
	stdout, stderr string
	code           int
	timedOut       bool
}

func (r frontDoorResult) String() string {
	return fmt.Sprintf("exit=%d timed_out=%v\nstdout:\n%s\nstderr:\n%s", r.code, r.timedOut, frontDoorClip(r.stdout, 3000), frontDoorClip(r.stderr, 3000))
}

func frontDoorClip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + fmt.Sprintf("\n... (%d more bytes)", len(s)-n)
}

// frontDoorCommonEnv keeps every bd run off the side channels that would make
// the sides differ for reasons the matrix does not test.
var frontDoorCommonEnv = []string{"BEADS_ACTOR=worker", "BD_BACKUP_ENABLED=false", "BD_EXPORT_AUTO=false"}

func frontDoorEnv(sd *frontDoorSide, extra []string) []string {
	env := append(append([]string(nil), frontDoorCommonEnv...), extra...)
	return minimalBDEnv(sd.dir, env...)
}

// frontDoorExec runs bd in sd; goroutine-safe, so it returns errors instead
// of failing a test.
func frontDoorExec(ctx context.Context, bin string, sd *frontDoorSide, stdin string, env []string, args ...string) (frontDoorResult, error) {
	ctx, cancel := context.WithTimeout(ctx, frontDoorBDTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = sd.dir
	cmd.Env = frontDoorEnv(sd, env)
	cmd.Stdin = strings.NewReader(stdin)
	var o, e strings.Builder
	cmd.Stdout, cmd.Stderr = &o, &e
	err := cmd.Run()
	r := frontDoorResult{stdout: o.String(), stderr: e.String()}
	if err != nil {
		var ee *exec.ExitError
		switch {
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			r.code, r.timedOut = -1, true
		case errors.As(err, &ee):
			r.code = ee.ExitCode()
		default:
			return r, fmt.Errorf("running bd %s on %s: %w", strings.Join(args, " "), sd.name, err)
		}
	}
	return r, nil
}

var frontDoorPlaceholder = regexp.MustCompile(`\{file:([^}]+)\}`)

// frontDoorExpand replaces {file:NAME} with sd's copy of NAME.
func frontDoorExpand(sd *frontDoorSide, args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = frontDoorPlaceholder.ReplaceAllStringFunc(a, func(m string) string {
			return filepath.Join(sd.files, frontDoorPlaceholder.FindStringSubmatch(m)[1])
		})
	}
	return out
}

func frontDoorWriteAssets(f *frontDoorFile, sd *frontDoorSide) error {
	for _, a := range f.Files {
		if err := os.WriteFile(filepath.Join(sd.files, a.Name), []byte(a.Content), 0o600); err != nil {
			return err
		}
		if a.InstallTo == "" {
			continue
		}
		dst := filepath.Join(sd.dir, filepath.FromSlash(a.InstallTo))
		if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
			return err
		}
		if err := os.WriteFile(dst, []byte(a.Content), 0o600); err != nil {
			return err
		}
	}
	return nil
}

func frontDoorConnectArgs(addr string) []string {
	return []string{"connect", "http://" + addr, "--expect-project-id", e2eProjectID, "--json"}
}

var frontDoorInitArgs = []string{"init", "--prefix", "e2e", "--quiet", "--non-interactive", "--skip-hooks", "--skip-agents"}

// frontDoorPrepare attaches sd (attach is bd connect or bd init), writes the
// assets and, when seed is set, runs the seed; an embedded side also takes
// seed_kv through bd kv set (the served side's go into the store directly).
func frontDoorPrepare(ctx context.Context, bin string, f *frontDoorFile, sd *frontDoorSide, attach []string, seed, embedded bool) error {
	r, err := frontDoorExec(ctx, bin, sd, "", nil, attach...)
	if err != nil {
		return err
	}
	if r.code != 0 {
		return fmt.Errorf("bd %s on %s: %s", strings.Join(attach, " "), sd.name, r)
	}
	if err := frontDoorWriteAssets(f, sd); err != nil {
		return fmt.Errorf("%s assets: %w", sd.name, err)
	}
	if !seed {
		return nil
	}
	for _, args := range f.Seed {
		r, err := frontDoorExec(ctx, bin, sd, "", nil, frontDoorExpand(sd, args)...)
		if err != nil {
			return err
		}
		if r.code != 0 {
			return fmt.Errorf("seed %q on %s: %s", args, sd.name, r)
		}
	}
	if embedded {
		for _, kv := range f.SeedKV {
			r, err := frontDoorExec(ctx, bin, sd, "", nil, "kv", "set", kv.Key, kv.Value)
			if err != nil {
				return err
			}
			if r.code != 0 {
				return fmt.Errorf("seed_kv %s on %s: %s", kv.Key, sd.name, r)
			}
		}
	}
	return nil
}

// frontDoorMatrix is one part's server and workspaces.
type frontDoorMatrix struct {
	bin     string
	file    *frontDoorFile
	store   *embeddeddolt.EmbeddedDoltStore
	proxy   string
	counter *requestCounter
	served  *frontDoorSide
	twin    *frontDoorSide
	oracle  *frontDoorSide
}

func newFrontDoorSide(t *testing.T, name string) *frontDoorSide {
	return &frontDoorSide{name: name, dir: t.TempDir(), files: t.TempDir()}
}

// startFrontDoorServer is the one place a part's server is built.
func startFrontDoorServer(t *testing.T) (string, *embeddeddolt.EmbeddedDoltStore, *requestCounter) {
	t.Helper()
	addr, store := startE2EServerWithStore(t, e2eProjectID, e2eDatabase)
	proxy, counter := startCountingProxy(t, addr)
	return proxy, store, counter
}

func testConnectFrontDoorMatrix(t *testing.T, part int) {
	if err := frontDoorPartNameErr(t.Name(), part); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, fn := range frontDoorPartTests() {
		names = append(names, frontDoorFuncName(fn))
	}
	if err := frontDoorPartsDeclaredErr(names); err != nil {
		t.Fatal(err)
	}
	if err := frontDoorCompareErr(); err != nil {
		t.Fatal(err)
	}
	skipUnlessEmbeddedDolt(t)
	f := loadConnectFrontDoor(t)
	runs := frontDoorPart(frontDoorRuns(f), part, frontDoorParts)
	bin := buildBD(t)

	start := time.Now()
	m := &frontDoorMatrix{bin: bin, file: f}
	m.proxy, m.store, m.counter = startFrontDoorServer(t)
	m.served = newFrontDoorSide(t, "served")
	m.twin = newFrontDoorSide(t, "embedded twin")
	m.oracle = newFrontDoorSide(t, "embedded oracle")

	// The sides prepare concurrently; the first failure cancels the others,
	// whose runs then die (exit -1), so only that first failure is reported.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var (
		firstMu  sync.Mutex
		firstErr error
	)
	fail := func(err error) {
		if err == nil {
			return
		}
		firstMu.Lock()
		defer firstMu.Unlock()
		if firstErr == nil {
			firstErr = err
			cancel()
		}
	}
	var wg sync.WaitGroup
	for _, sd := range []*frontDoorSide{m.twin, m.oracle} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			fail(frontDoorPrepare(ctx, bin, f, sd, frontDoorInitArgs, true, true))
		}()
	}
	err := frontDoorPrepare(ctx, bin, f, m.served, frontDoorConnectArgs(m.proxy), true, false)
	for _, kv := range f.SeedKV {
		if err != nil {
			break
		}
		err = m.store.SetConfig(context.Background(), "kv."+kv.Key, kv.Value)
	}
	fail(err)
	wg.Wait()
	if firstErr != nil {
		t.Fatalf("MATRIX BUG: part %d setup: %v", part, firstErr)
	}
	setup := time.Since(start)

	type timing struct {
		id string
		d  time.Duration
	}
	var timings []timing
	worksFailed := ""
	for _, r := range runs {
		t.Run(r.c.ID, func(t *testing.T) {
			s := time.Now()
			fails := m.runCase(t, r)
			timings = append(timings, timing{r.c.ID, time.Since(s)})
			if len(fails) == 0 {
				return
			}
			fails = append(fails, frontDoorFooter)
			for _, msg := range fails {
				t.Errorf("%s case %s (%s): %s", r.command, r.c.ID, r.c.Class, msg)
			}
			if worksFailed != "" {
				t.Logf("an earlier works case in this part (%s) failed; the served workspace and its twin may have diverged since, so fix that one first", worksFailed)
			}
			if r.c.Class == classWorks && !r.c.Fresh && worksFailed == "" {
				worksFailed = r.c.ID
			}
		})
	}
	sort.Slice(timings, func(i, j int) bool { return timings[i].d > timings[j].d })
	var slowest []string
	for i, tm := range timings {
		if i == 8 {
			break
		}
		slowest = append(slowest, fmt.Sprintf("%s=%s", tm.id, tm.d.Round(100*time.Millisecond)))
	}
	t.Logf("part %d: setup %s; %d cases; total %s; slowest: %s", part, setup.Round(100*time.Millisecond), len(runs),
		time.Since(start).Round(100*time.Millisecond), strings.Join(slowest, " "))
}

// frontDoorStoreState is what the no-write check compares on the server's
// store: its HEAD (commit and root hashes), its working-set status, and the
// contents of two places those miss. The config table (where kv and config
// writes land) stays dirty for the whole part, since the server's own setup
// and seed_kv never commit, so status alone cannot see a further uncommitted
// write to it; and the wisp tables are dolt_ignore'd (migration 0019), so a
// wisp write reaches neither HEAD nor status. Of those tables only the wisps
// and their labels are compared. The other dolt_ignore'd tables (the wisps'
// dependencies, comments and events, events, leases, bd_events_journal and
// bd_events_seq; internal/storage/schema's doltIgnorePatterns) are not, so a
// refusal that wrote only to them would pass.
type frontDoorStoreState struct{ head, status, config, wisps string }

func frontDoorStoreStateOf(store *embeddeddolt.EmbeddedDoltStore) (frontDoorStoreState, error) {
	ctx := context.Background()
	commit, root, err := store.GetHeadHashes(ctx)
	if err != nil {
		return frontDoorStoreState{}, err
	}
	st, err := store.Status(ctx)
	if err != nil {
		return frontDoorStoreState{}, err
	}
	status, err := json.Marshal(st)
	if err != nil {
		return frontDoorStoreState{}, err
	}
	cfg, err := store.GetAllConfig(ctx)
	if err != nil {
		return frontDoorStoreState{}, err
	}
	config, err := json.Marshal(cfg) // map keys marshal sorted
	if err != nil {
		return frontDoorStoreState{}, err
	}
	wisps, err := store.ListWisps(ctx, types.WispFilter{IncludeClosed: true})
	if err != nil {
		return frontDoorStoreState{}, err
	}
	ids := make([]string, 0, len(wisps))
	for _, w := range wisps {
		ids = append(ids, w.ID)
	}
	labels, err := store.GetLabelsForIssues(ctx, ids)
	if err != nil {
		return frontDoorStoreState{}, err
	}
	sort.Slice(wisps, func(i, j int) bool { return wisps[i].ID < wisps[j].ID })
	wispState, err := json.Marshal(struct {
		Issues []*types.Issue
		Labels map[string][]string
	}{wisps, labels})
	if err != nil {
		return frontDoorStoreState{}, err
	}
	return frontDoorStoreState{head: commit + "/" + root, status: string(status), config: string(config), wisps: string(wispState)}, nil
}

// frontDoorStoreDiff names what changed between two states; "" when nothing.
func frontDoorStoreDiff(before, after frontDoorStoreState) string {
	var out []string
	for _, f := range []struct{ name, a, b string }{
		{"HEAD", before.head, after.head},
		{"status", before.status, after.status},
		{"config", before.config, after.config},
		{"wisps", before.wisps, after.wisps},
	} {
		if f.a != f.b {
			out = append(out, fmt.Sprintf("%s %s -> %s", f.name, frontDoorClip(f.a, 1500), frontDoorClip(f.b, 1500)))
		}
	}
	return strings.Join(out, "; ")
}

// frontDoorLocalVolatile is what a command may rewrite in .beads without
// writing anything: last-touched (the id --current resolves), the local
// version stamp, and lock files.
func frontDoorLocalVolatile(rel string) bool {
	base := filepath.Base(rel)
	return base == "last-touched" || base == ".local_version" || strings.HasSuffix(base, ".lock") || base == "LOCK"
}

// frontDoorLocalSnapshot hashes every non-volatile file under dir/.beads.
func frontDoorLocalSnapshot(dir string) map[string]string {
	out := map[string]string{}
	_ = filepath.WalkDir(filepath.Join(dir, ".beads"), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil || frontDoorLocalVolatile(rel) {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		out[rel] = fmt.Sprintf("%x", sha256.Sum256(b))
		return nil
	})
	return out
}

func frontDoorSnapshotDiff(a, b map[string]string) []string {
	var out []string
	for k, v := range a {
		if w, ok := b[k]; !ok {
			out = append(out, k+" (removed)")
		} else if w != v {
			out = append(out, k+" (changed)")
		}
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			out = append(out, k+" (new)")
		}
	}
	sort.Strings(out)
	return out
}

// freshPair is a fresh case's own unseeded served workspace and an embedded
// workspace that is both its twin and its oracle. A fresh works case also gets
// its own server, so the ids it mints never reach the part's server.
func (m *frontDoorMatrix) freshPair(t *testing.T, c frontDoorCase) (served, twin *frontDoorSide, store *embeddeddolt.EmbeddedDoltStore, counter *requestCounter, err error) {
	served, twin = newFrontDoorSide(t, "fresh served"), newFrontDoorSide(t, "fresh embedded")
	addr, store, counter := m.proxy, m.store, m.counter
	if c.Class == classWorks {
		addr, store, counter = startFrontDoorServer(t)
	}
	ctx := context.Background()
	if err := frontDoorPrepare(ctx, m.bin, m.file, served, frontDoorConnectArgs(addr), false, false); err != nil {
		return nil, nil, nil, nil, fmt.Errorf("fresh served workspace: %w", err)
	}
	if err := frontDoorPrepare(ctx, m.bin, m.file, twin, frontDoorInitArgs, false, true); err != nil {
		return nil, nil, nil, nil, fmt.Errorf("fresh embedded workspace: %w", err)
	}
	return served, twin, store, counter, nil
}

func (m *frontDoorMatrix) runCase(t *testing.T, r frontDoorRun) []string {
	c := r.c
	var fails []string
	failf := func(format string, a ...any) { fails = append(fails, fmt.Sprintf(format, a...)) }
	served, twin, oracle := m.served, m.twin, m.oracle
	store, counter := m.store, m.counter
	if c.Fresh {
		var err error
		if served, twin, store, counter, err = m.freshPair(t, c); err != nil {
			return []string{"MATRIX BUG: " + err.Error()}
		}
		oracle = twin
	}
	run := func(sd *frontDoorSide, args []string) frontDoorResult {
		t.Helper()
		res, err := frontDoorExec(context.Background(), m.bin, sd, c.Stdin, frontDoorExpand(sd, c.Env), frontDoorExpand(sd, args)...)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	sides := []*frontDoorSide{served, twin}
	if oracle != twin {
		sides = append(sides, oracle)
	}
	for _, args := range c.Setup {
		for _, sd := range sides {
			if res := run(sd, args); res.code != 0 {
				return []string{fmt.Sprintf("MATRIX BUG: setup %q failed on %s: %s", args, sd.name, res)}
			}
		}
	}

	checkStore := c.Class == classRefuses || c.Class == classPendingRuling
	checkLocal := c.Class == classRefuses || (c.Class == classPendingRuling && c.Observed.Writes == "none")
	var before frontDoorStoreState
	var beforeLocal map[string]string
	if checkStore {
		var err error
		if before, err = frontDoorStoreStateOf(store); err != nil {
			t.Fatal(err)
		}
		beforeLocal = frontDoorLocalSnapshot(served.dir)
	}
	counter.reset()
	var sr frontDoorResult
	listened := false
	if c.Class == classPendingRuling && c.Observed.Listens {
		sr, listened = m.runListening(t, served, c)
	} else {
		sr = run(served, c.Args)
	}
	requests := counter.snapshot()
	t.Logf("bd %s\nserved (%d requests: %s): %s", strings.Join(c.Args, " "), len(requests), strings.Join(requests, ", "), sr)
	if checkStore {
		after, err := frontDoorStoreStateOf(store)
		if err != nil {
			t.Fatal(err)
		}
		if d := frontDoorStoreDiff(before, after); d != "" {
			failf("the served run wrote the server's store: %s", d)
		}
		if d := frontDoorSnapshotDiff(beforeLocal, frontDoorLocalSnapshot(served.dir)); checkLocal && len(d) > 0 {
			failf("the served run wrote the workspace's .beads: %v", d)
		}
	}

	switch c.Class {
	case classWorks:
		lr := run(twin, c.Args)
		if sr.code != c.wantExit() || lr.code != c.wantExit() {
			failf("exit: served %d, embedded %d, want %d on both\nserved: %s\nembedded: %s", sr.code, lr.code, c.wantExit(), sr, lr)
			return fails
		}
		if c.ExpectStdout != "" && !regexp.MustCompile(c.ExpectStdout).MatchString(sr.stdout) {
			failf("served stdout does not match expect_stdout %q: %s", c.ExpectStdout, sr)
		}
		if d := frontDoorCompare(c, sr.stdout, lr.stdout); d != "" {
			failf("output parity with the embedded twin (%s): %s", c.compareMode(), d)
		}
		for _, args := range c.Verify {
			vs, vl := run(served, args), run(twin, args)
			if vs.code != vl.code {
				failf("verify %q: exit served %d, embedded %d\nserved: %s\nembedded: %s", args, vs.code, vl.code, vs, vl)
				continue
			}
			if vl.code == 0 {
				if d := frontDoorCompareJSON(vs.stdout, vl.stdout, c.IgnoreFields); d != "" {
					failf("verify %q diverged after the write: %s", args, d)
				}
			}
		}
	case classRefuses:
		if sr.code != *c.Exit {
			failf("served exit %d, want %d (refuses): %s", sr.code, *c.Exit, sr)
		}
		if !regexp.MustCompile(c.Words).MatchString(frontDoorRefusalText(sr)) {
			failf("the served refusal does not match words %q: %s", c.Words, sr)
		}
		if c.Code != "" && !frontDoorHasCode(sr, c.Code) {
			failf("the served refusal carries no JSON code %q: %s", c.Code, sr)
		}
	case classPendingRuling:
		o := c.Observed
		if o.Listens {
			if !listened {
				failf("observed listens, but bd printed no listening line within %s: %s", frontDoorListenTimeout, sr)
			}
		} else if sr.code != *o.Exit {
			failf("served exit %d, observed %d: %s", sr.code, *o.Exit, sr)
		}
		for _, chk := range []struct{ name, re, text string }{{"stdout", o.Stdout, sr.stdout}, {"stderr", o.Stderr, sr.stderr}} {
			if chk.re != "" && !regexp.MustCompile(chk.re).MatchString(chk.text) {
				failf("served %s no longer matches the observed %q: %s", chk.name, chk.re, sr)
			}
		}
		if o.StdoutAbsent != "" && regexp.MustCompile(o.StdoutAbsent).MatchString(sr.stdout) {
			failf("served stdout now matches stdout_absent %q: %s", o.StdoutAbsent, sr)
		}
	}
	if (c.Class == classRefuses || c.Class == classPendingRuling) && !c.SkipEmbedded {
		if lr := run(oracle, c.Args); lr.code != c.wantEmbeddedExit() {
			failf("MATRIX BUG: the embedded oracle exited %d, want %d (embedded_exit), so the argv itself may be broken: %s", lr.code, c.wantEmbeddedExit(), lr)
		}
	}
	return fails
}

var (
	frontDoorListening    = regexp.MustCompile(`^bd serve: listening on http://`)
	frontDoorListeningAny = regexp.MustCompile(`(?m)^bd serve: listening on http://`)
)

// runListening starts bd, waits for its listening line, then stops it with
// an interrupt (a kill if it does not exit).
func (m *frontDoorMatrix) runListening(t *testing.T, sd *frontDoorSide, c frontDoorCase) (frontDoorResult, bool) {
	t.Helper()
	cmd := exec.Command(m.bin, frontDoorExpand(sd, c.Args)...)
	cmd.Dir = sd.dir
	cmd.Env = frontDoorEnv(sd, frontDoorExpand(sd, c.Env))
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var errb strings.Builder
	cmd.Stderr = &errb
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var outb strings.Builder
	lines := make(chan string, 64)
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		defer close(lines)
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			mu.Lock()
			outb.WriteString(sc.Text() + "\n")
			mu.Unlock()
			select {
			case lines <- sc.Text():
			default:
			}
		}
		_, _ = io.Copy(io.Discard, stdout)
	}()
	listened := false
	deadline := time.After(frontDoorListenTimeout)
wait:
	for {
		select {
		case l, ok := <-lines:
			if !ok {
				break wait
			}
			if frontDoorListening.MatchString(l) {
				listened = true
				break wait
			}
		case <-deadline:
			break wait
		}
	}
	_ = cmd.Process.Signal(os.Interrupt)
	stopped := make(chan struct{})
	go func() {
		select {
		case <-stopped:
		case <-time.After(frontDoorStopTimeout):
			_ = cmd.Process.Kill()
		}
	}()
	<-readDone
	_ = cmd.Wait()
	close(stopped)
	mu.Lock()
	defer mu.Unlock()
	// A listening line the channel dropped (its buffer full) is still in outb.
	listened = listened || frontDoorListeningAny.MatchString(outb.String())
	return frontDoorResult{stdout: outb.String(), stderr: errb.String(), code: cmd.ProcessState.ExitCode()}, listened
}

// frontDoorJSONObjects decodes every JSON object that starts a line of s.
func frontDoorJSONObjects(s string) []map[string]any {
	var out []map[string]any
	for i := 0; i < len(s); i++ {
		if s[i] != '{' || (i > 0 && s[i-1] != '\n') {
			continue
		}
		var m map[string]any
		if err := json.NewDecoder(strings.NewReader(s[i:])).Decode(&m); err == nil {
			out = append(out, m)
		}
	}
	return out
}

// frontDoorRefusalText is what words match: both streams, plus every string
// member of each JSON object either stream carries, decoded, so a --json
// refusal matches the same words as its text form (escaped quotes would
// otherwise break them).
func frontDoorRefusalText(r frontDoorResult) string {
	var b strings.Builder
	b.WriteString(r.stderr + "\n" + r.stdout)
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case string:
			b.WriteString("\n" + x)
		case map[string]any:
			keys := make([]string, 0, len(x))
			for k := range x {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				walk(x[k])
			}
		case []any:
			for _, e := range x {
				walk(e)
			}
		}
	}
	for _, s := range []string{r.stderr, r.stdout} {
		for _, m := range frontDoorJSONObjects(s) {
			walk(m)
		}
	}
	return b.String()
}

func frontDoorHasCode(r frontDoorResult, code string) bool {
	for _, s := range []string{r.stderr, r.stdout} {
		for _, m := range frontDoorJSONObjects(s) {
			if m["code"] == code {
				return true
			}
		}
	}
	return false
}

// frontDoorClock is a timestamp's optional time of day. It is optional
// because text renderings print bare dates ("Created: 2026-11-09"), and the
// sides are seeded seconds apart, possibly across midnight.
const frontDoorClock = `([T ]\d{2}:\d{2}(:\d{2}(\.\d+)?)?(Z|[+-]\d{2}:?\d{2})?)?`

var (
	// frontDoorVolatileKey is a value each store keeps for itself: dropped.
	frontDoorVolatileKey = regexp.MustCompile(`^(revision|row_version|expected_revision|current_revision)$`)
	// frontDoorStampKey is a time bd stamps when it writes (created_at,
	// closed_at, ...). Its value is scrubbed like any string's, so when each
	// side wrote does not count, but whether the stamp is set, or zeroed,
	// does. A null counts as not set.
	frontDoorStampKey      = regexp.MustCompile(`(^|_)at$`)
	frontDoorTimestamp     = regexp.MustCompile(`\d{4}-\d{2}-\d{2}` + frontDoorClock)
	frontDoorZeroTimestamp = regexp.MustCompile(`0001-01-01` + frontDoorClock)
)

// frontDoorDeadlineKeys are the times a user sets (bd defer --until, bd
// update --due): compared as they are, never scrubbed, so a case that sets one
// gives an absolute date (a relative one resolves seconds apart on the sides).
var frontDoorDeadlineKeys = map[string]bool{"defer_until": true, "due_at": true}

// frontDoorScrubTimes replaces each timestamp in s with <ts>, or with
// <zero-ts> for the zero time a shallow row carries instead of a real stamp.
func frontDoorScrubTimes(s string) string {
	return frontDoorTimestamp.ReplaceAllString(frontDoorZeroTimestamp.ReplaceAllString(s, "<zero-ts>"), "<ts>")
}

// frontDoorCompareErr falsifies the works comparator: parity must see a
// deadline's value and a zeroed or missing stamp, and must not see when each
// side ran. Every part runs it before the engine gate, as it does
// frontDoorPartsDeclaredErr, so it holds in every lane without a top-level
// test of its own (which would spend one of http_served_test's
// unmeasured-test allowances).
func frontDoorCompareErr() error {
	for _, p := range []struct {
		compare, served, embedded string
		equal                     bool
	}{
		{"json", `{"defer_until":"2099-01-01T00:00:00Z"}`, `{"defer_until":"2099-02-01T00:00:00Z"}`, false},
		{"json", `{"due_at":"2099-01-01T00:00:00Z"}`, `{"due_at":"2099-01-01"}`, false},
		{"json", `{"created_at":"0001-01-01T00:00:00Z"}`, `{"created_at":"2026-11-09T04:20:13Z"}`, false},
		{"json", `{"closed_at":"2026-11-09T04:20:13Z"}`, `{"closed_at":null}`, false},
		{"lines", `{"reason":"since 0001-01-01T00:00:00Z"}`, `{"reason":"since 2026-11-09T04:20:13Z"}`, false},
		{"text", "Created: 0001-01-01", "Created: 2026-11-09", false},
		{"json", `[{"created_at":"2026-11-09T04:20:13Z","started_at":null,"revision":7,"note":"by 2026-11-09"}]`,
			`[{"created_at":"2026-11-10T00:00:00.5-08:00","revision":9,"note":"by 2026-11-10"}]`, true},
		{"text", "Created: 2026-11-09 23:59", "Created: 2026-11-10 00:00", true},
	} {
		if equal := frontDoorCompare(frontDoorCase{Compare: p.compare}, p.served, p.embedded) == ""; equal != p.equal {
			return fmt.Errorf("the works comparator (compare: %s) finds served %s and embedded %s equal=%v, want %v: frontDoorNormalize and frontDoorScrubTimes must hide when each side ran, but not a deadline or a zeroed or missing stamp", p.compare, p.served, p.embedded, equal, p.equal)
		}
	}
	return nil
}

// frontDoorCompare is a works case's output parity under its compare mode;
// "" when the outputs agree.
func frontDoorCompare(c frontDoorCase, served, local string) string {
	switch c.compareMode() {
	case "exit":
		return ""
	case "text":
		a := strings.TrimSpace(frontDoorScrubTimes(served))
		b := strings.TrimSpace(frontDoorScrubTimes(local))
		if a != b {
			return fmt.Sprintf("text differs\nserved:\n%s\nembedded:\n%s", frontDoorClip(a, 2500), frontDoorClip(b, 2500))
		}
		return ""
	case "lines":
		drop := frontDoorDrop(c.IgnoreFields)
		norm := func(s string) ([]string, error) {
			var out []string
			for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
				if strings.TrimSpace(l) == "" {
					continue
				}
				var v any
				if err := json.Unmarshal([]byte(l), &v); err != nil {
					return nil, err
				}
				raw, err := json.Marshal(frontDoorNormalize(v, drop))
				if err != nil {
					return nil, err
				}
				out = append(out, string(raw))
			}
			sort.Strings(out)
			return out, nil
		}
		a, ea := norm(served)
		b, eb := norm(local)
		if ea != nil || eb != nil {
			return fmt.Sprintf("lines are not JSON: served err=%v, embedded err=%v", ea, eb)
		}
		if !reflect.DeepEqual(a, b) {
			return fmt.Sprintf("lines differ: served %d, embedded %d\nserved:\n%s\nembedded:\n%s", len(a), len(b),
				frontDoorClip(strings.Join(a, "\n"), 2500), frontDoorClip(strings.Join(b, "\n"), 2500))
		}
		return ""
	default:
		return frontDoorCompareJSON(served, local, c.IgnoreFields)
	}
}

func frontDoorDrop(fields []string) map[string]bool {
	drop := map[string]bool{}
	for _, k := range fields {
		drop[k] = true
	}
	return drop
}

// frontDoorCompareJSON, frontDoorNormalize and frontDoorFirstDiff follow
// backend/http/corpus's compareJSON/normalize/firstDiff (corpus_test.go),
// unordered, with string timestamps scrubbed (frontDoorScrubTimes) and
// deadlines compared as they are.
func frontDoorCompareJSON(served, local string, ignore []string) string {
	var a, b any
	ea := json.Unmarshal([]byte(strings.TrimSpace(served)), &a)
	eb := json.Unmarshal([]byte(strings.TrimSpace(local)), &b)
	if ea != nil || eb != nil {
		return fmt.Sprintf("not JSON: served err=%v, embedded err=%v\nserved:\n%s\nembedded:\n%s", ea, eb, frontDoorClip(served, 1500), frontDoorClip(local, 1500))
	}
	drop := frontDoorDrop(ignore)
	a, b = frontDoorNormalize(a, drop), frontDoorNormalize(b, drop)
	if path, x, y, same := frontDoorFirstDiff("$", a, b); !same {
		rx, _ := json.Marshal(x)
		ry, _ := json.Marshal(y)
		return fmt.Sprintf("first difference at %s: served=%s embedded=%s", path, frontDoorClip(string(rx), 600), frontDoorClip(string(ry), 600))
	}
	return ""
}

func frontDoorNormalize(v any, drop map[string]bool) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, val := range x {
			switch {
			case drop[k] || frontDoorVolatileKey.MatchString(k):
			case frontDoorDeadlineKeys[k]:
				out[k] = val
			case val == nil && frontDoorStampKey.MatchString(k):
			default:
				out[k] = frontDoorNormalize(val, drop)
			}
		}
		return out
	case []any:
		out := make([]any, len(x))
		keys := make([]string, len(x))
		for i, e := range x {
			out[i] = frontDoorNormalize(e, drop)
			raw, _ := json.Marshal(out[i])
			keys[i] = string(raw)
		}
		idx := make([]int, len(x))
		for i := range idx {
			idx[i] = i
		}
		sort.SliceStable(idx, func(i, j int) bool { return keys[idx[i]] < keys[idx[j]] })
		sorted := make([]any, len(x))
		for i, j := range idx {
			sorted[i] = out[j]
		}
		return sorted
	case string:
		return frontDoorScrubTimes(x)
	default:
		return v
	}
}

func frontDoorFirstDiff(path string, a, b any) (string, any, any, bool) {
	switch x := a.(type) {
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok {
			return path, a, b, false
		}
		keys := map[string]bool{}
		for k := range x {
			keys[k] = true
		}
		for k := range y {
			keys[k] = true
		}
		sorted := make([]string, 0, len(keys))
		for k := range keys {
			sorted = append(sorted, k)
		}
		sort.Strings(sorted)
		for _, k := range sorted {
			xv, xok := x[k]
			yv, yok := y[k]
			if !xok || !yok {
				return path + "." + k, xv, yv, false
			}
			if p, l, r, same := frontDoorFirstDiff(path+"."+k, xv, yv); !same {
				return p, l, r, false
			}
		}
		return "", nil, nil, true
	case []any:
		y, ok := b.([]any)
		if !ok {
			return path, a, b, false
		}
		if len(x) != len(y) {
			return path + ".length", len(x), len(y), false
		}
		for i := range x {
			if p, l, r, same := frontDoorFirstDiff(fmt.Sprintf("%s[%d]", path, i), x[i], y[i]); !same {
				return p, l, r, false
			}
		}
		return "", nil, nil, true
	default:
		if reflect.DeepEqual(a, b) {
			return "", nil, nil, true
		}
		return path, a, b, false
	}
}
