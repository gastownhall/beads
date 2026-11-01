//go:build cgo

// Package corpus_test is the served command-corpus suite: every distinct bd
// command shape an orchestrator (gc) and its packs/formulas issue, run by the
// real bd binary against an in-process bd serve masked to a reference
// deployment's wire surface, and compared with the same command on an embedded
// workspace. See README.md.
package corpus_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// corpusFile is testdata/corpus.yaml.
type corpusFile struct {
	Files  []corpusAsset `yaml:"files"`
	Seed   [][]string    `yaml:"seed"`
	Shapes []shape       `yaml:"shapes"`
}

// corpusAsset is a file the shapes reference as {file:NAME}. Exactly one of
// Content and Bulk is set.
type corpusAsset struct {
	Name    string     `yaml:"name"`
	Content string     `yaml:"content"`
	Bulk    *bulkGraph `yaml:"bulk"`
}

// bulkGraph generates a `bd create --graph` plan of Count independent tasks
// with ids IDPrefix-001.. so the suite can hold more rows than one server page
// without a 100-line literal in the corpus.
type bulkGraph struct {
	IDPrefix  string            `yaml:"id_prefix"`
	Count     int               `yaml:"count"`
	Priority  int               `yaml:"priority"`
	Ephemeral bool              `yaml:"ephemeral"`
	Metadata  map[string]string `yaml:"metadata"`
}

type shape struct {
	ID     string `yaml:"id"`
	Source string `yaml:"source"`
	// Args is the bd argv. Tokens may carry {file:NAME} (the side's copy of a
	// corpus asset) and {rev:ID} (that side's current revision of ID).
	Args  []string   `yaml:"args"`
	Stdin string     `yaml:"stdin"`
	Setup [][]string `yaml:"setup"`
	// Compare is how the served stdout is held to the embedded stdout:
	// json (default), ids, count, text, or exit (exit code only).
	Compare string `yaml:"compare"`
	// Ordered keeps array order significant under json/ids.
	Ordered bool `yaml:"ordered"`
	// PrefixOf is the unlimited form of a limited listing (compare: ids). A
	// limit cut through a tie set (same priority, created_at within one
	// second) lands at a different row on two workspaces seeded seconds
	// apart, so the limited rows are not compared across sides. Instead each
	// side's limited rows must be, in order, a prefix of that SAME side's
	// PrefixOf listing, both sides must return as many rows, and the two
	// PrefixOf listings must hold the same ids.
	PrefixOf []string `yaml:"prefix_of"`
	// IgnoreFields are extra JSON keys dropped before comparing.
	IgnoreFields []string `yaml:"ignore_fields"`
	// ExpectExit is the exit code both sides must produce (default 0). An
	// allowlisted shape must state it: the embedded run is held to it even
	// though the served run is only held to the allowlisted refusal.
	ExpectExit *int `yaml:"expect_exit"`
	// ExpectStderr, when set, must match both sides' stderr (only the
	// embedded side's on an allowlisted shape).
	ExpectStderr string `yaml:"expect_stderr"`
	// Verify are reads run on both sides after the shape and compared as json:
	// how a write is checked for having done the same thing on both backends.
	Verify [][]string `yaml:"verify"`
	// Requests, when set, is exactly how many requests the served run of Args
	// makes through the reference proxy (not its setup or verify reads). Each
	// is a sequential round trip, so the count is the shape's latency floor
	// over a gateway: a change that adds one fails here, and one that saves
	// one updates the pin.
	Requests *int `yaml:"requests"`
	// Pending marks a known failure and names the slice that fixes it.
	Pending *pending `yaml:"pending"`
}

// wantExit is the exit code the embedded run must produce.
func (s shape) wantExit() int {
	if s.ExpectExit == nil {
		return 0
	}
	return *s.ExpectExit
}

type pending struct {
	Slice  string `yaml:"slice"`
	Reason string `yaml:"reason"`
}

// allowEntry is testdata/allowlist.yaml: a shape that is EXPECTED to be
// refused over http, permanently, for a stated reason. The served run must
// fail and match Refusal; if it ever succeeds the entry is stale and fails.
// The embedded run is still held to the shape's expect_exit and
// expect_stderr, and nothing else of the shape is checked.
type allowEntry struct {
	ID      string `yaml:"id"`
	Reason  string `yaml:"reason"`
	Refusal string `yaml:"refusal"`
}

// refusalMarkers are the texts a refusal of an operation over http carries:
// issueops.ErrUnsupported, encode.RefusedError, the client's capability
// preflight (wire.CapabilityError, which never reaches the proxy), the
// server's loopback-only unlimited-read 400, an unrouted 404, and a strict
// decoder's unknown member.
var refusalMarkers = regexp.MustCompile(`(?i)not supported by the|not expressible on the v0 wire|loopback-only|capability "[^"]+" not advertised|does not advertise capability|no such route on this server|unknown_parameter|unknown query parameter|json: unknown field`)

// volatileKey names JSON members that legitimately differ between two
// workspaces fed the same commands: timestamps and revision tokens.
var volatileKey = regexp.MustCompile(`(^|_)at$|^revision$|^row_version$|^expected_revision$|^current_revision$`)

// callerTimeKey names the timestamps a shape's own arguments set (bd create
// --defer, --due). They are compared as given, neither dropped as volatile
// nor scrubbed to <ts>, so a client that sends the wrong one fails; a shape
// must give them as absolute dates, since a relative one resolves against
// each side's own clock.
var callerTimeKey = map[string]bool{"defer_until": true, "due_at": true}

var timestampText = regexp.MustCompile(`\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}(:\d{2}(\.\d+)?)?(Z|[+-]\d{2}:?\d{2})?`)

func loadCorpus(t *testing.T) (*corpusFile, map[string]allowEntry) {
	t.Helper()
	raw, err := os.ReadFile("testdata/corpus.yaml")
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	var c corpusFile
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		t.Fatalf("parse testdata/corpus.yaml: %v", err)
	}
	rawAllow, err := os.ReadFile("testdata/allowlist.yaml")
	if err != nil {
		t.Fatalf("read allowlist: %v", err)
	}
	var entries []allowEntry
	dec = yaml.NewDecoder(strings.NewReader(string(rawAllow)))
	dec.KnownFields(true)
	if err := dec.Decode(&entries); err != nil {
		t.Fatalf("parse testdata/allowlist.yaml: %v", err)
	}
	allow := map[string]allowEntry{}
	for _, e := range entries {
		if e.ID == "" || strings.TrimSpace(e.Reason) == "" || e.Refusal == "" {
			t.Fatalf("allowlist entry %+v needs id, reason and refusal", e)
		}
		allow[e.ID] = e
	}
	return &c, allow
}

// TestCorpusFileIsWellFormed runs without the embedded engine: it keeps the
// data honest (unique ids, a source and a slice for every pending mark, no
// allowlist entry for a shape that does not exist, no shape both pending and
// allowlisted, an allowlisted shape states its embedded exit code and
// carries no check that the allowlist would skip, every expect_stderr and
// refusal compiles, and the reference profile loads).
func TestCorpusFileIsWellFormed(t *testing.T) {
	c, allow := loadCorpus(t)
	seen := map[string]bool{}
	for _, s := range c.Shapes {
		if s.ID == "" || len(s.Args) == 0 {
			t.Errorf("shape %+v needs an id and args", s)
			continue
		}
		if seen[s.ID] {
			t.Errorf("duplicate shape id %q", s.ID)
		}
		seen[s.ID] = true
		if strings.TrimSpace(s.Source) == "" {
			t.Errorf("shape %s names no source", s.ID)
		}
		if s.Pending != nil && (s.Pending.Slice == "" || strings.TrimSpace(s.Pending.Reason) == "") {
			t.Errorf("shape %s is pending without a slice and a reason", s.ID)
		}
		if _, ok := allow[s.ID]; ok {
			if s.Pending != nil {
				t.Errorf("shape %s is both pending and allowlisted; pick one", s.ID)
			}
			if s.ExpectExit == nil {
				t.Errorf("shape %s is allowlisted without expect_exit; state the exit code the embedded run must produce", s.ID)
			}
			if modeOf(s) != "exit" || len(s.Verify) > 0 {
				t.Errorf("shape %s is allowlisted, so only its exit code and stderr are checked; give it compare: exit and no verify", s.ID)
			}
			if s.Requests != nil {
				t.Errorf("shape %s is allowlisted, so its request count is not checked; drop requests", s.ID)
			}
		}
		switch s.Compare {
		case "", "json", "ids", "count", "text", "exit":
		default:
			t.Errorf("shape %s: unknown compare mode %q", s.ID, s.Compare)
		}
		if len(s.PrefixOf) > 0 && s.Compare != "ids" {
			t.Errorf("shape %s: prefix_of needs compare: ids", s.ID)
		}
		if _, err := regexp.Compile(s.ExpectStderr); err != nil {
			t.Errorf("shape %s: expect_stderr: %v", s.ID, err)
		}
		if s.Requests != nil && *s.Requests < 0 {
			t.Errorf("shape %s: requests %d is negative", s.ID, *s.Requests)
		}
	}
	for id, entry := range allow {
		if !seen[id] {
			t.Errorf("allowlist names %q, which is not a corpus shape", id)
		}
		if _, err := regexp.Compile(entry.Refusal); err != nil {
			t.Errorf("allowlist entry %s: refusal: %v", id, err)
		}
	}
	for _, a := range c.Files {
		if (a.Content == "") == (a.Bulk == nil) {
			t.Errorf("asset %s needs exactly one of content and bulk", a.Name)
		}
	}
	loadReferenceProfile(t)
}

// side is one workspace the corpus runs in.
type side struct {
	name  string
	dir   string
	files string
	env   []string
}

type outcome struct {
	id, status, detail string
	requests           int64
}

// corpusParts is how many parts the shapes are dealt across. Each part is a
// top-level test of its own (TestServedCorpusPart1..N, servedCorpusParts)
// that starts its own server and seeds its own pair of workspaces, so the
// embedded lane runs each part in a Bazel shard of its own
// (served_pinned_shards.txt pins part k to shard k). The parts can be split
// because no shape depends on another's effects: every comparison is the
// served side against the embedded side after the same commands, and each
// part keeps corpus order, so its reads still run on the bare seed before
// any of its writes (see testdata/corpus.yaml's header).
//
// Four parts, from the embedded lane's measured part times (PR #7538): each
// part's server start and concurrent seed take ~62-76s and the 127 shapes
// ~242s together, so four parts ran 114-146s each and three ran 170-180s.
// Three would save one setup (~75 test-seconds a run), but the longest part
// is the embedded lane's served long pole, on the gate's critical path
// (forks' above all), so the ~34s it saves is worth more.
const corpusParts = 4

// servedCorpusParts are the parts' tests, one per part, in part order.
var servedCorpusParts = []func(*testing.T){
	TestServedCorpusPart1, TestServedCorpusPart2, TestServedCorpusPart3, TestServedCorpusPart4,
}

func TestServedCorpusPart1(t *testing.T) { testServedCorpus(t, 1) }
func TestServedCorpusPart2(t *testing.T) { testServedCorpus(t, 2) }
func TestServedCorpusPart3(t *testing.T) { testServedCorpus(t, 3) }
func TestServedCorpusPart4(t *testing.T) { testServedCorpus(t, 4) }

// corpusPartNameErr fails a part test whose name is not its part's: a
// TestServedCorpusPart3 that ran part 2 would run part 2's shapes twice and
// silently drop its own. A part outside 1..corpusParts (a stray
// TestServedCorpusPart5 left after the parts shrink) would run no shapes.
func corpusPartNameErr(name string, part int) error {
	if part < 1 || part > corpusParts {
		return fmt.Errorf("%s runs corpus part %d; there are parts 1..%d (corpusParts)", name, part, corpusParts)
	}
	if want := fmt.Sprintf("TestServedCorpusPart%d", part); name != want {
		return fmt.Errorf("%s runs corpus part %d; only %s may (fix its testServedCorpus call)", name, part, want)
	}
	return nil
}

// corpusPart returns the shapes part (1..corpusParts) runs.
func corpusPart(shapes []shape, part int) []shape {
	return corpusPartOf(shapes, part, corpusParts)
}

// corpusPartOf deals shapes round robin, in corpus order, across parts parts
// and returns part's (1-based) share: shapes part-1, part-1+parts, and so on.
// Round robin keeps each part a mix of the corpus's reads and writes in
// their original order.
func corpusPartOf(shapes []shape, part, parts int) []shape {
	var out []shape
	for i := part - 1; i < len(shapes); i += parts {
		out = append(out, shapes[i])
	}
	return out
}

// testServedCorpus is the suite, for one part of the corpus. Each shape is a
// subtest; a pending shape is still RUN and then skipped with what it did, so
// a slice that fixes it sees the subtest fail with "now passes; remove the
// pending mark". BEADS_CORPUS_SKIP_PENDING=1 skips pending shapes without
// running them. BEADS_CORPUS_REPORT=<path> writes a JSON summary of every
// part this process has run.
func testServedCorpus(t *testing.T, part int) {
	// First, before the engine gate: a part test wired to the wrong part
	// fails in every lane, not only where the corpus runs.
	if err := corpusPartNameErr(t.Name(), part); err != nil {
		t.Fatal(err)
	}
	skipUnlessEmbeddedDolt(t)
	c, allow := loadCorpus(t)
	shapes := corpusPart(c.Shapes, part)
	profile := loadReferenceProfile(t)
	bin := buildBD(t)

	common := []string{
		"BEADS_ACTOR=worker",
		"CLAUDE_SESSION_ID=corpus-session-s23",
		"BD_BACKUP_ENABLED=false",
		"BD_EXPORT_AUTO=false",
	}

	// The embedded side needs no server: init and seed it on a goroutine
	// while this one starts the server and seeds the served side. Each side
	// has its own directories, env and bd processes (prepareSide), and each
	// still runs the seed in order, so the seeded data is what it was when
	// the two sides were seeded command by command in turn. The deferred
	// cancel and Wait run on every exit, a t.Fatal included, so the goroutine
	// never outlives the test or its TempDirs.
	setupStart := time.Now()
	ctx, cancel := context.WithCancel(context.Background())
	var seeding sync.WaitGroup
	defer seeding.Wait()
	defer cancel()
	local := &side{name: "embedded", dir: t.TempDir(), files: t.TempDir(), env: common}
	var localErr error
	seeding.Add(1)
	go func() {
		defer seeding.Done()
		localErr = prepareSide(ctx, t, bin, c, local,
			[]string{"init", "-p", corpusPrefix, "--non-interactive", "--skip-hooks", "--skip-agents", "-q"})
		if localErr != nil {
			cancel()
		}
	}()

	serverAddr := startCorpusServer(t)
	proxy := newReferenceProxy(t, profile, serverAddr)
	proxyAddr := startProxy(t, proxy)
	served := &side{name: "served", dir: t.TempDir(), files: t.TempDir(),
		env: append([]string{"BEADS_HTTP_TOKEN=" + proxyAddr + "=" + corpusToken}, common...)}
	servedErr := prepareSide(ctx, t, bin, c, served,
		[]string{"connect", "http://" + proxyAddr, "--expect-project-id", corpusProjectID, "--json"})
	if servedErr != nil {
		cancel()
	}
	seeding.Wait()
	// The side that failed first is the one to report; the other may only
	// have seen the cancel.
	for _, err := range []error{servedErr, localErr} {
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	}
	t.Logf("part %d setup (server start and both sides seeded concurrently): %s", part, time.Since(setupStart).Round(100*time.Millisecond))
	proxy.takeRefusals()

	skipPending := os.Getenv("BEADS_CORPUS_SKIP_PENDING") == "1"
	var outcomes []outcome
	for _, s := range shapes {
		s := s
		t.Run(s.ID, func(t *testing.T) {
			o := outcome{id: s.ID}
			defer func() { outcomes = append(outcomes, o) }()
			if s.Pending != nil && skipPending {
				o.status = "pending:" + s.Pending.Slice
				t.Skipf("pending %s: %s", s.Pending.Slice, s.Pending.Reason)
			}
			before := proxy.requests.Load()
			failures := runShape(t, bin, s, served, local, allow, proxy)
			o.requests = proxy.requests.Load() - before
			entry, allowed := allow[s.ID]
			switch {
			case hasCorpusBug(failures):
				// The embedded side itself disagrees with the corpus: no pending
				// mark or allowlist entry may hide that, or a pending shape that
				// can never pass would sit in the corpus forever.
				o.status = "FAIL(corpus)"
				o.detail = failures[0]
				for _, f := range failures {
					t.Error(f)
				}
			case s.Pending != nil && len(failures) == 0:
				o.status = "FAIL(pending-now-passes)"
				t.Errorf("shape is marked pending %s but now passes on the reference profile; remove the pending mark from testdata/corpus.yaml", s.Pending.Slice)
			case s.Pending != nil:
				o.status = "pending:" + s.Pending.Slice
				o.detail = failures[0]
				t.Skipf("pending %s: %s\nobserved:\n%s", s.Pending.Slice, s.Pending.Reason, strings.Join(failures, "\n"))
			case len(failures) > 0:
				o.status = "FAIL"
				o.detail = failures[0]
				for _, f := range failures {
					t.Error(f)
				}
			case allowed:
				o.status = "allowed"
				o.detail = entry.Reason
			default:
				o.status = "pass"
			}
		})
	}

	summarize(t, part, len(c.Shapes), outcomes)
}

// runShape runs one shape on both sides and returns every way the served run
// fell short. It never calls t.Error itself, so the caller can turn a pending
// shape's failures into a skip.
func runShape(t *testing.T, bin string, s shape, served, local *side, allow map[string]allowEntry, proxy *referenceProxy) []string {
	t.Helper()
	var fails []string
	failf := func(format string, a ...any) { fails = append(fails, fmt.Sprintf(format, a...)) }
	wantStderr, err := regexp.Compile(s.ExpectStderr)
	if err != nil {
		failf("CORPUS BUG: expect_stderr %q: %v", s.ExpectStderr, err)
		return fails
	}

	for _, args := range s.Setup {
		for _, sd := range []*side{served, local} {
			if r := runBD(t, bin, sd.dir, sd.env, "", expand(context.Background(), t, bin, sd, args)...); r.code != 0 {
				failf("setup %v failed on %s: %s", args, sd.name, r)
				return fails
			}
		}
	}
	proxy.takeRefusals()
	args := expand(context.Background(), t, bin, served, s.Args)
	before := proxy.requests.Load()
	sr := runBD(t, bin, served.dir, served.env, s.Stdin, args...)
	servedRequests := proxy.requests.Load() - before
	wireRefusals := proxy.takeRefusals()
	lr := runBD(t, bin, local.dir, local.env, s.Stdin, expand(context.Background(), t, bin, local, s.Args)...)
	t.Logf("bd %s\nserved: %s\nembedded: exit=%d", strings.Join(s.Args, " "), sr, lr.code)
	if len(wireRefusals) > 0 {
		t.Logf("reference-profile refusals on the wire: %v", wireRefusals)
	}

	// The embedded run is the oracle for every shape, an allowlisted one
	// included: when it no longer does what the shape says, the shape is
	// wrong or bd changed, whatever the served run did.
	if lr.code != s.wantExit() {
		failf("CORPUS BUG: the embedded run exited %d, want %d; the shape itself is wrong or bd changed: %s", lr.code, s.wantExit(), lr)
		return fails
	}
	if s.ExpectStderr != "" && !wantStderr.MatchString(lr.stderr) {
		failf("CORPUS BUG: embedded stderr does not match %q: %s", s.ExpectStderr, lr.stderr)
		return fails
	}

	if entry, ok := allow[s.ID]; ok {
		re, err := regexp.Compile(entry.Refusal)
		if err != nil {
			failf("allowlist refusal regexp for %s: %v", s.ID, err)
			return fails
		}
		if sr.code == 0 {
			failf("allowlisted (%s) but the served run SUCCEEDED; drop it from testdata/allowlist.yaml and give it a comparison", entry.Reason)
		} else if !re.MatchString(sr.stderr + sr.stdout) {
			failf("allowlisted refusal did not match %q (a different failure than the one allowed): %s", entry.Refusal, sr)
		}
		return fails
	}

	if m := refusalMarkers.FindString(sr.stderr + sr.stdout); m != "" {
		failf("served run carries a refusal (%q): %s", m, sr)
	} else if len(wireRefusals) > 0 && sr.code != 0 {
		failf("served run failed on a reference-profile refusal %v: %s", wireRefusals, sr)
	}
	if sr.code != lr.code {
		failf("exit code: served %d, embedded %d: %s", sr.code, lr.code, sr)
	}
	if s.ExpectStderr != "" && !wantStderr.MatchString(sr.stderr) {
		failf("served stderr does not match %q (embedded does): %s", s.ExpectStderr, sr)
	}
	if s.Requests != nil && servedRequests != int64(*s.Requests) {
		failf("the served run made %d requests, want exactly %d (requests); a change that saves one updates the pin", servedRequests, *s.Requests)
	}
	if len(fails) > 0 {
		return fails
	}
	if len(s.PrefixOf) > 0 {
		vs := runBD(t, bin, served.dir, served.env, "", expand(context.Background(), t, bin, served, s.PrefixOf)...)
		vl := runBD(t, bin, local.dir, local.env, "", expand(context.Background(), t, bin, local, s.PrefixOf)...)
		if vs.code != 0 || vl.code != 0 {
			failf("prefix_of %v: exit served %d, embedded %d: served %s", s.PrefixOf, vs.code, vl.code, vs)
		} else if d := comparePrefix(sr.stdout, lr.stdout, vs.stdout, vl.stdout); d != "" {
			failf("output parity (ids, prefix_of): %s", d)
		}
	} else if d := compareOutputs(s, sr.stdout, lr.stdout); d != "" {
		failf("output parity (%s): %s", modeOf(s), d)
	}
	for _, args := range s.Verify {
		vs := runBD(t, bin, served.dir, served.env, "", expand(context.Background(), t, bin, served, args)...)
		vl := runBD(t, bin, local.dir, local.env, "", expand(context.Background(), t, bin, local, args)...)
		if vs.code != vl.code {
			failf("verify %v: exit served %d, embedded %d: served %s", args, vs.code, vl.code, vs)
			continue
		}
		if vl.code != 0 {
			continue // both refused the read (e.g. a deleted id): equal is the check
		}
		if d := compareJSON(vs.stdout, vl.stdout, s.IgnoreFields, false); d != "" {
			failf("verify %v diverged after the write: %s", args, d)
		}
	}
	return fails
}

func hasCorpusBug(failures []string) bool {
	for _, f := range failures {
		if strings.HasPrefix(f, "CORPUS BUG") {
			return true
		}
	}
	return false
}

func modeOf(s shape) string {
	if s.Compare == "" {
		return "json"
	}
	return s.Compare
}

func compareOutputs(s shape, served, local string) string {
	switch modeOf(s) {
	case "exit":
		return ""
	case "text":
		a, b := scrubText(served), scrubText(local)
		if a != b {
			return fmt.Sprintf("text differs\nserved:\n%s\nembedded:\n%s", clip(a, 3000), clip(b, 3000))
		}
		return ""
	case "count":
		a, errA := topLevelLen(served)
		b, errB := topLevelLen(local)
		if errA != nil || errB != nil {
			return fmt.Sprintf("count: served err=%v embedded err=%v\nserved:\n%s", errA, errB, clip(served, 2000))
		}
		if a != b {
			return fmt.Sprintf("row count: served %d, embedded %d", a, b)
		}
		return ""
	case "ids":
		a, errA := idList(served, s.Ordered)
		b, errB := idList(local, s.Ordered)
		if errA != nil || errB != nil {
			return fmt.Sprintf("ids: served err=%v embedded err=%v\nserved:\n%s", errA, errB, clip(served, 2000))
		}
		if !reflect.DeepEqual(a, b) {
			return fmt.Sprintf("ids differ\nserved   (%d): %v\nembedded (%d): %v", len(a), a, len(b), b)
		}
		return ""
	default:
		return compareJSON(served, local, s.IgnoreFields, s.Ordered)
	}
}

// comparePrefix holds a limited listing to its unlimited form on each side:
// see shape.PrefixOf.
func comparePrefix(served, local, servedAll, localAll string) string {
	lists := make([][]string, 4)
	for i, out := range []string{served, local, servedAll, localAll} {
		ids, err := idList(out, true)
		if err != nil {
			return fmt.Sprintf("listing %d: %v\n%s", i, err, clip(out, 2000))
		}
		lists[i] = ids
	}
	s, l, sAll, lAll := lists[0], lists[1], lists[2], lists[3]
	if len(s) != len(l) {
		return fmt.Sprintf("row count: served %d, embedded %d", len(s), len(l))
	}
	for _, c := range []struct {
		name      string
		rows, all []string
	}{{"served", s, sAll}, {"embedded", l, lAll}} {
		if len(c.rows) > len(c.all) || !reflect.DeepEqual(c.rows, c.all[:len(c.rows)]) {
			return fmt.Sprintf("%s limited rows are not a prefix of its own unlimited listing\nlimited: %v\nunlimited: %v", c.name, c.rows, c.all)
		}
	}
	sSet := append([]string(nil), sAll...)
	lSet := append([]string(nil), lAll...)
	sort.Strings(sSet)
	sort.Strings(lSet)
	if !reflect.DeepEqual(sSet, lSet) {
		return fmt.Sprintf("unlimited ids differ\nserved   (%d): %v\nembedded (%d): %v", len(sSet), sSet, len(lSet), lSet)
	}
	return ""
}

func scrubText(s string) string {
	return strings.TrimSpace(timestampText.ReplaceAllString(s, "<ts>"))
}

func decode(s string) (any, error) {
	var v any
	if err := json.Unmarshal([]byte(strings.TrimSpace(s)), &v); err != nil {
		return nil, fmt.Errorf("not JSON: %w", err)
	}
	return v, nil
}

// rows finds the row array in a bd --json payload: the payload itself, or the
// first array member of an envelope object.
func rows(v any) ([]any, bool) {
	switch x := v.(type) {
	case []any:
		return x, true
	case map[string]any:
		for _, k := range []string{"issues", "data", "items", "results"} {
			if arr, ok := x[k].([]any); ok {
				return arr, true
			}
		}
	}
	return nil, false
}

func topLevelLen(s string) (int, error) {
	v, err := decode(s)
	if err != nil {
		return 0, err
	}
	arr, ok := rows(v)
	if !ok {
		return 0, fmt.Errorf("no row array in output")
	}
	return len(arr), nil
}

func idList(s string, ordered bool) ([]string, error) {
	v, err := decode(s)
	if err != nil {
		return nil, err
	}
	arr, ok := rows(v)
	if !ok {
		return nil, fmt.Errorf("no row array in output")
	}
	out := []string{}
	for _, r := range arr {
		if m, ok := r.(map[string]any); ok {
			if id, ok := m["id"].(string); ok {
				out = append(out, id)
			}
		}
	}
	if !ordered {
		sort.Strings(out)
	}
	return out, nil
}

func compareJSON(served, local string, ignore []string, ordered bool) string {
	a, errA := decode(served)
	b, errB := decode(local)
	if errA != nil || errB != nil {
		return fmt.Sprintf("served err=%v embedded err=%v\nserved:\n%s\nembedded:\n%s", errA, errB, clip(served, 2000), clip(local, 2000))
	}
	drop := map[string]bool{}
	for _, k := range ignore {
		drop[k] = true
	}
	a, b = normalize(a, drop, ordered), normalize(b, drop, ordered)
	if path, x, y, same := firstDiff("$", a, b); !same {
		return fmt.Sprintf("first difference at %s: served=%s embedded=%s", path, brief(x), brief(y))
	}
	return ""
}

func normalize(v any, drop map[string]bool, ordered bool) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			if drop[k] {
				continue
			}
			if callerTimeKey[k] {
				out[k] = val
				continue
			}
			if volatileKey.MatchString(k) {
				continue
			}
			out[k] = normalize(val, drop, ordered)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = normalize(e, drop, ordered)
		}
		if !ordered {
			sort.SliceStable(out, func(i, j int) bool { return sortKey(out[i]) < sortKey(out[j]) })
		}
		return out
	case string:
		return timestampText.ReplaceAllString(x, "<ts>")
	default:
		return v
	}
}

func sortKey(v any) string {
	if m, ok := v.(map[string]any); ok {
		for _, k := range []string{"id", "issue_id", "key"} {
			if s, ok := m[k].(string); ok {
				return k + "=" + s
			}
		}
		if inner, ok := m["issue"].(map[string]any); ok {
			if s, ok := inner["id"].(string); ok {
				return "issue.id=" + s
			}
		}
	}
	raw, _ := json.Marshal(v)
	return string(raw)
}

func firstDiff(path string, a, b any) (string, any, any, bool) {
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
			if p, l, r, same := firstDiff(path+"."+k, xv, yv); !same {
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
			if p, l, r, same := firstDiff(path+"["+strconv.Itoa(i)+"]", x[i], y[i]); !same {
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

func brief(v any) string {
	if v == nil {
		return "<absent>"
	}
	raw, _ := json.Marshal(v)
	return clip(string(raw), 400)
}

var placeholder = regexp.MustCompile(`\{(file|rev):([^}]+)\}`)

// expand substitutes {file:NAME} and {rev:ID} in args for side sd. It never
// fails t (a {rev:} it cannot read becomes "0"), so prepareSide may call it
// off the test's goroutine; its {rev:} lookup runs under ctx, so a cancelled
// prepareSide starts no further bd there either.
func expand(ctx context.Context, t *testing.T, bin string, sd *side, args []string) []string {
	t.Helper()
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = placeholder.ReplaceAllStringFunc(a, func(m string) string {
			parts := placeholder.FindStringSubmatch(m)
			switch parts[1] {
			case "file":
				return filepath.Join(sd.files, parts[2])
			default:
				r, err := execBD(ctx, bin, sd.dir, sd.env, "", "show", parts[2], "--json")
				if err != nil {
					t.Logf("{rev:%s} on %s: %v", parts[2], sd.name, err)
					return "0"
				}
				if r.code != 0 {
					t.Logf("{rev:%s} on %s: show failed: %s", parts[2], sd.name, r)
					return "0"
				}
				v, err := decode(r.stdout)
				if err != nil {
					return "0"
				}
				if arr, ok := rows(v); ok && len(arr) > 0 {
					v = arr[0]
				}
				if m, ok := v.(map[string]any); ok {
					if rev, ok := m["revision"].(string); ok {
						return rev
					}
				}
				return "0"
			}
		})
	}
	return out
}

// prepareSide attaches sd to its store with bd attach (connect or init),
// writes the corpus's asset files into sd.files and runs the seed on sd, in
// order. It touches only sd's own directories and bd processes and returns a
// failure as an error instead of failing t, so testServedCorpus runs it for
// the two sides at once; it stops before its next bd once ctx is done.
func prepareSide(ctx context.Context, t *testing.T, bin string, c *corpusFile, sd *side, attach []string) error {
	t.Helper()
	r, err := execBD(ctx, bin, sd.dir, sd.env, "", attach...)
	if err != nil {
		return err
	}
	if r.code != 0 {
		return fmt.Errorf("bd %s on %s: %s", strings.Join(attach, " "), sd.name, r)
	}
	if err := writeAssets(c.Files, sd.files); err != nil {
		return fmt.Errorf("%s: %w", sd.name, err)
	}
	for _, args := range c.Seed {
		if err := ctx.Err(); err != nil {
			return err
		}
		r, err := execBD(ctx, bin, sd.dir, sd.env, "", expand(ctx, t, bin, sd, args)...)
		if err != nil {
			return err
		}
		if r.code != 0 {
			return fmt.Errorf("seed %v on %s failed; the seed must use shapes that work on the reference profile: %s", args, sd.name, r)
		}
	}
	return nil
}

func writeAssets(assets []corpusAsset, dir string) error {
	for _, a := range assets {
		content := a.Content
		if a.Bulk != nil {
			content = bulkPlan(*a.Bulk)
		}
		if err := os.WriteFile(filepath.Join(dir, a.Name), []byte(content), 0o600); err != nil {
			return fmt.Errorf("write asset %s: %w", a.Name, err)
		}
	}
	return nil
}

func bulkPlan(b bulkGraph) string {
	type node struct {
		Key       string            `json:"key"`
		ID        string            `json:"id"`
		Title     string            `json:"title"`
		Type      string            `json:"type"`
		Priority  int               `json:"priority"`
		Ephemeral bool              `json:"ephemeral,omitempty"`
		Metadata  map[string]string `json:"metadata,omitempty"`
	}
	plan := struct {
		CommitMessage string `json:"commit_message"`
		Nodes         []node `json:"nodes"`
	}{CommitMessage: "corpus bulk " + b.IDPrefix}
	for i := 1; i <= b.Count; i++ {
		id := fmt.Sprintf("%s-%03d", b.IDPrefix, i)
		plan.Nodes = append(plan.Nodes, node{Key: id, ID: id, Title: "bulk " + id, Type: "task",
			Priority: b.Priority, Ephemeral: b.Ephemeral, Metadata: b.Metadata})
	}
	raw, _ := json.MarshalIndent(plan, "", " ")
	return string(raw)
}

// reported is every outcome of every part this process has run, in the order
// the parts ran, for BEADS_CORPUS_REPORT.
var reported []outcome

func summarize(t *testing.T, part, total int, outcomes []outcome) {
	t.Helper()
	counts := map[string]int{}
	var lines []string
	for _, o := range outcomes {
		key := o.status
		if strings.HasPrefix(key, "pending:") {
			key = "pending"
		}
		counts[key]++
		lines = append(lines, fmt.Sprintf("%-26s %-48s req=%-3d %s", o.status, o.id, o.requests, firstLine(o.detail)))
	}
	t.Logf("corpus summary, part %d of %d (%d of %d shapes): %v\n%s", part, corpusParts, len(outcomes), total, counts, strings.Join(lines, "\n"))
	reported = append(reported, outcomes...)
	if path := os.Getenv("BEADS_CORPUS_REPORT"); path != "" {
		type row struct {
			ID       string `json:"id"`
			Status   string `json:"status"`
			Requests int64  `json:"requests"`
			Detail   string `json:"detail,omitempty"`
		}
		var out []row
		for _, o := range reported {
			out = append(out, row{o.id, o.status, o.requests, o.detail})
		}
		raw, _ := json.MarshalIndent(out, "", " ")
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Errorf("write BEADS_CORPUS_REPORT: %v", err)
		}
	}
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return clip(s, 160)
}
