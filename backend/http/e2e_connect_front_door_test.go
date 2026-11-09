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
// write it (checked: the server's HEAD and working set, and S's .beads), and
// run on O only to prove the argv is not broken. So S and T stay identical by
// construction and no case's outcome depends on its order in its part.
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
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

func TestE2E_ConnectFrontDoorMatrixPart1(t *testing.T) { testConnectFrontDoorMatrix(t, 1) }
func TestE2E_ConnectFrontDoorMatrixPart2(t *testing.T) { testConnectFrontDoorMatrix(t, 2) }
func TestE2E_ConnectFrontDoorMatrixPart3(t *testing.T) { testConnectFrontDoorMatrix(t, 3) }
func TestE2E_ConnectFrontDoorMatrixPart4(t *testing.T) { testConnectFrontDoorMatrix(t, 4) }

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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	errs := make([]error, 3)
	for i, sd := range []*frontDoorSide{m.twin, m.oracle} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if errs[i] = frontDoorPrepare(ctx, bin, f, sd, frontDoorInitArgs, true, true); errs[i] != nil {
				cancel()
			}
		}()
	}
	errs[2] = frontDoorPrepare(ctx, bin, f, m.served, frontDoorConnectArgs(m.proxy), true, false)
	for _, kv := range f.SeedKV {
		if errs[2] != nil {
			break
		}
		errs[2] = m.store.SetConfig(context.Background(), "kv."+kv.Key, kv.Value)
	}
	if errs[2] != nil {
		cancel()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("MATRIX BUG: part %d setup: %v", part, err)
		}
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

// frontDoorStoreState is the server store's HEAD (commit and root hashes)
// and working-set status: what a write changes.
type frontDoorStoreState struct{ head, status string }

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
	raw, err := json.Marshal(st)
	if err != nil {
		return frontDoorStoreState{}, err
	}
	return frontDoorStoreState{head: commit + "/" + root, status: string(raw)}, nil
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
		if after != before {
			failf("the served run wrote the server's store (HEAD %s -> %s; status %s -> %s)", before.head, after.head, before.status, after.status)
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

var (
	frontDoorVolatileKey = regexp.MustCompile(`(^|_)at$|^revision$|^row_version$|^expected_revision$|^current_revision$`)
	frontDoorTimestamp   = regexp.MustCompile(`\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}(:\d{2}(\.\d+)?)?(Z|[+-]\d{2}:?\d{2})?`)
)

// frontDoorCompare is a works case's output parity under its compare mode;
// "" when the outputs agree.
func frontDoorCompare(c frontDoorCase, served, local string) string {
	switch c.compareMode() {
	case "exit":
		return ""
	case "text":
		a := strings.TrimSpace(frontDoorTimestamp.ReplaceAllString(served, "<ts>"))
		b := strings.TrimSpace(frontDoorTimestamp.ReplaceAllString(local, "<ts>"))
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
// unordered, with string timestamps scrubbed.
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
			if drop[k] || (frontDoorVolatileKey.MatchString(k) && k != "defer_until" && k != "due_at") {
				continue
			}
			out[k] = frontDoorNormalize(val, drop)
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
		return frontDoorTimestamp.ReplaceAllString(x, "<ts>")
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
