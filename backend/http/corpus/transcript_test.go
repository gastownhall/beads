//go:build cgo

package corpus_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/httpclient/wire"
)

// The served run of every shape leaves a wire transcript: each request the bd
// under test made through the proxy, by name and never by value (method,
// operation, path, query parameter names, top-level request members, status).
// testdata/transcripts/<shape>.json holds the reference-profile transcript of
// every shape whose served run happens; testdata/transcripts/current/ holds
// the current-capability ones (current_capability_test.go). A change to what
// bd sends for a shape fails its subtest until the golden is regenerated
// (-count=1 because bd is built in a subprocess, which go test's result cache
// cannot see; a serial run of every part takes several minutes):
//
//	BD_UPDATE_GOLDEN=1 CGO_ENABLED=1 BEADS_TEST_EMBEDDED_DOLT=1 go test -tags gms_pure_go -count=1 -timeout 40m -run 'TestServedCorpus|TestCorpusTranscripts' ./backend/http/corpus/
const (
	transcriptDir        = "testdata/transcripts"
	currentTranscriptDir = "testdata/transcripts/current"
	transcriptRegen      = "BD_UPDATE_GOLDEN=1 CGO_ENABLED=1 BEADS_TEST_EMBEDDED_DOLT=1 go test -tags gms_pure_go -count=1 -timeout 40m -run 'TestServedCorpus|TestCorpusTranscripts' ./backend/http/corpus/"
)

// transcriptRequest is one request of a served run. It carries names, never
// values: no query value, no member value, so no timestamp, revision token or
// cursor can make two runs differ. The path is the one value kept, with ids
// the run minted replaced by placeholders (maskMintedIDs).
type transcriptRequest struct {
	Method string `json:"method"`
	// OperationID is the profile's operation the request matched, or "" when
	// none did (the reference proxy's unrouted 404).
	OperationID string   `json:"operation_id"`
	Path        string   `json:"path"`
	Query       []string `json:"query,omitempty"`
	Body        []string `json:"body,omitempty"`
	Status      int      `json:"status"`
}

// transcript is one golden file.
type transcript struct {
	Shape string `json:"shape"`
	// Exit is the served run's exit code. Only the current-capability
	// transcripts carry it: they have no embedded run to hold the outcome to.
	Exit     *int                `json:"exit,omitempty"`
	Requests []transcriptRequest `json:"requests"`
}

// transcriptRecorder collects the subject's requests between start and stop.
// A request is appended when it arrives and its status is filled in when the
// proxy writes the response header, which is before the client can see the
// answer, so stop never races a request bd has already been answered.
//
// It leaves out a read that only reached the wire because the client's read
// memo had expired (expiredMemoRead), so no transcript depends on how fast
// the host ran the command.
type transcriptRecorder struct {
	mu       sync.Mutex
	on       bool
	requests []*transcriptRequest
	// now is the clock memo ages are measured on; nil is time.Now.
	now func() time.Time
	// answered is when each memoable read, by its full request URI, was last
	// answered 2xx in this window since the subject's last write: what the
	// client's memo would hold (wire/read_memo.go).
	answered map[string]time.Time
	// gen counts the windows and the subject's writes, as the memo's own
	// generation does: an answer to a read that overlapped a write, or that
	// lands after its window, is not stored.
	gen uint64
	// expired counts the reads left out because the memo would have answered
	// them but for its TTL.
	expired int
}

func (rec *transcriptRecorder) clock() time.Time {
	if rec.now != nil {
		return rec.now()
	}
	return time.Now()
}

func (rec *transcriptRecorder) start() {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	rec.on, rec.requests, rec.answered, rec.expired = true, nil, map[string]time.Time{}, 0
	rec.gen++
}

// stop ends the window and returns its requests and how many memo-expired
// reads it left out of them.
func (rec *transcriptRecorder) stop() ([]transcriptRequest, int) {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	out := make([]transcriptRequest, 0, len(rec.requests))
	for _, r := range rec.requests {
		out = append(out, *r)
	}
	expired := rec.expired
	rec.on, rec.requests, rec.answered, rec.expired = false, nil, nil, 0
	rec.gen++
	return out, expired
}

// memoableTranscriptOps are the operations the client's read memo answers
// (wire/read_memo.go memoableOps).
var memoableTranscriptOps = map[string]bool{
	wire.OpGetIssue:     true,
	wire.OpGetSetting:   true,
	wire.OpListSettings: true,
}

// expiredMemoRead reports whether r is a read bd's read memo would have
// answered, but for its TTL: a memoable GET of a URI the server answered 2xx
// at least wire.ReadMemoTTL ago in this window, with no write by the subject
// since (every write clears the memo). The memo measures an entry's age from
// when the client stored the answer, which is after the proxy wrote its
// status, so a read the memo let through for age is always caught here, and
// a regression that re-reads within the TTL (milliseconds later, in
// practice) is still recorded and still drifts. The window is one bd
// process, whose memo starts empty, like this map. Caller holds rec.mu.
func (rec *transcriptRecorder) expiredMemoRead(r *http.Request, op *referenceOperation) bool {
	if r.Method != http.MethodGet {
		clear(rec.answered)
		rec.gen++
		return false
	}
	if op == nil || !memoableTranscriptOps[op.OperationID] {
		return false
	}
	at, ok := rec.answered[r.URL.RequestURI()]
	return ok && rec.clock().Sub(at) >= wire.ReadMemoTTL
}

// track records r while recording is on and returns the writer that fills in
// its status; otherwise it returns w unchanged.
func (rec *transcriptRecorder) track(w http.ResponseWriter, r *http.Request, op *referenceOperation, body []byte) http.ResponseWriter {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if !rec.on {
		return w
	}
	key := r.URL.RequestURI()
	memoable := r.Method == http.MethodGet && op != nil && memoableTranscriptOps[op.OperationID]
	entry := &transcriptRequest{Method: r.Method, Path: r.URL.EscapedPath(), Query: queryNames(r.URL), Body: memberNames(body)}
	if op != nil {
		entry.OperationID = op.OperationID
	}
	if rec.expiredMemoRead(r, op) {
		rec.expired++
	} else {
		rec.requests = append(rec.requests, entry)
	}
	gen := rec.gen
	return &statusWriter{ResponseWriter: w, set: func(code int) {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		entry.Status = code
		// The client stores a memoable read's 2xx answer, fresh, on every
		// miss (a hit never reaches the wire), unless a write overlapped it.
		if memoable && code >= 200 && code < 300 && gen == rec.gen {
			rec.answered[key] = rec.clock()
		}
	}}
}

// statusWriter reports the first final (>= 200) status written through it.
type statusWriter struct {
	http.ResponseWriter
	set   func(int)
	wrote bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.wrote && code >= http.StatusOK {
		w.wrote = true
		w.set(code)
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController (the reverse proxy's flushes for
// events:watch) reach the connection's own writer.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func queryNames(u *url.URL) []string {
	return slices.Sorted(maps.Keys(u.Query()))
}

// memberNames is the sorted top-level member names of a JSON object body.
func memberNames(raw []byte) []string {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(raw, &members); err != nil {
		return []string{"(not a JSON object)"}
	}
	return slices.Sorted(maps.Keys(members))
}

// corpusIDToken is an issue id under the corpus's prefix as it appears in a
// request path: it stops at the "/" and ":" that end a path segment.
var corpusIDToken = regexp.MustCompile(regexp.QuoteMeta(corpusPrefix) + `-[A-Za-z0-9][A-Za-z0-9._-]*`)

// knownCorpusIDs is every id the corpus names itself, anywhere in
// testdata/corpus.yaml (the seed assets and every shape), plus the ids its
// bulk graphs generate. Any other id in a request path was minted during the
// run (a gate's hash id, say) and differs run to run.
func knownCorpusIDs(t *testing.T, c *corpusFile) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile("testdata/corpus.yaml")
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	known := map[string]bool{}
	for _, id := range corpusIDToken.FindAllString(string(raw), -1) {
		known[strings.TrimRight(id, ".-_")] = true
	}
	for _, a := range c.Files {
		if a.Bulk != nil {
			for _, id := range bulkIDs(*a.Bulk) {
				known[id] = true
			}
		}
	}
	return known
}

// maskMintedIDs replaces each id in a request path that the corpus does not
// name with <minted-N>, numbered in the order the run first sent it, so the
// same minted id keeps one placeholder across the transcript.
func maskMintedIDs(reqs []transcriptRequest, known map[string]bool) []transcriptRequest {
	out := slices.Clone(reqs)
	minted := map[string]string{}
	for i := range out {
		out[i].Path = corpusIDToken.ReplaceAllStringFunc(out[i].Path, func(id string) string {
			if known[id] {
				return id
			}
			if p, ok := minted[id]; ok {
				return p
			}
			p := fmt.Sprintf("<minted-%d>", len(minted)+1)
			minted[id] = p
			return p
		})
	}
	return out
}

// renderTranscript is a golden's exact bytes: one request per line, so a
// changed request is a one-line diff.
func renderTranscript(tr transcript) []byte {
	var b bytes.Buffer
	line := func(v any) string {
		var e bytes.Buffer
		enc := json.NewEncoder(&e)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(v)
		return strings.TrimSuffix(e.String(), "\n")
	}
	b.WriteString("{\n")
	fmt.Fprintf(&b, "  \"shape\": %s,\n", line(tr.Shape))
	if tr.Exit != nil {
		fmt.Fprintf(&b, "  \"exit\": %d,\n", *tr.Exit)
	}
	if len(tr.Requests) == 0 {
		b.WriteString("  \"requests\": []\n}\n")
		return b.Bytes()
	}
	b.WriteString("  \"requests\": [\n")
	for i, r := range tr.Requests {
		b.WriteString("    " + line(r))
		if i < len(tr.Requests)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	b.WriteString("  ]\n}\n")
	return b.Bytes()
}

func parseTranscript(raw []byte) (transcript, error) {
	var tr transcript
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&tr); err != nil {
		return transcript{}, err
	}
	if tr.Requests == nil {
		return transcript{}, errors.New(`no "requests" member`)
	}
	return tr, nil
}

// checkTranscript holds a shape's served run to its golden in dir, or
// rewrites the golden under BD_UPDATE_GOLDEN=1. ran is false when the served
// run never happened (a setup step failed), and then a golden is stale. It
// returns what is wrong, "" when nothing is.
func checkTranscript(t *testing.T, dir string, got transcript, ran bool) string {
	t.Helper()
	path := filepath.Join(dir, got.Shape+".json")
	update := os.Getenv("BD_UPDATE_GOLDEN") == "1"
	want, err := os.ReadFile(path)
	exists := err == nil
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Sprintf("read %s: %v", path, err)
	}
	if !ran {
		switch {
		case !exists:
			return ""
		case update:
			if err := os.Remove(path); err != nil {
				return fmt.Sprintf("remove stale %s: %v", path, err)
			}
			t.Logf("removed %s: the served run of %s did not happen", path, got.Shape)
			return ""
		default:
			return fmt.Sprintf("the served run of %s did not happen (a setup step failed), but %s exists; regenerate with\n  %s", got.Shape, path, transcriptRegen)
		}
	}
	rendered := renderTranscript(got)
	if update {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Sprintf("mkdir %s: %v", dir, err)
		}
		if err := os.WriteFile(path, rendered, 0o644); err != nil {
			return fmt.Sprintf("write %s: %v", path, err)
		}
		return ""
	}
	if !exists {
		return fmt.Sprintf("%s has no wire transcript %s; regenerate with\n  %s\nand review it", got.Shape, path, transcriptRegen)
	}
	if bytes.Equal(want, rendered) {
		return ""
	}
	return fmt.Sprintf("the requests bd sent for %s drifted from %s. If the change is intended, regenerate with\n  %s\nand review the testdata diff.\n%s", got.Shape, path, transcriptRegen, transcriptDiff(want, rendered))
}

// transcriptDiff shows the lines between the common head and tail of two
// renderings, as -want / +got.
func transcriptDiff(want, got []byte) string {
	w := strings.Split(strings.TrimSuffix(string(want), "\n"), "\n")
	g := strings.Split(strings.TrimSuffix(string(got), "\n"), "\n")
	head := 0
	for head < len(w) && head < len(g) && w[head] == g[head] {
		head++
	}
	tail := 0
	for tail < len(w)-head && tail < len(g)-head && w[len(w)-1-tail] == g[len(g)-1-tail] {
		tail++
	}
	var b strings.Builder
	for _, l := range w[head : len(w)-tail] {
		b.WriteString("- " + l + "\n")
	}
	for _, l := range g[head : len(g)-tail] {
		b.WriteString("+ " + l + "\n")
	}
	return b.String()
}

// loadTranscripts reads every golden in dir by shape. The only subdirectory
// allowed anywhere is the current-capability one, which it does not descend
// into; any other directory is an error, so no golden can hide in one.
func loadTranscripts(t *testing.T, dir string) map[string]transcript {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	out := map[string]transcript{}
	for _, e := range entries {
		if e.IsDir() {
			if sub := filepath.ToSlash(filepath.Join(dir, e.Name())); sub != currentTranscriptDir {
				t.Errorf("%s is a directory; %s is the only one allowed under %s", sub, currentTranscriptDir, transcriptDir)
			}
			continue
		}
		path := filepath.Join(dir, e.Name())
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok {
			t.Errorf("%s is not a .json transcript", path)
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		tr, err := parseTranscript(raw)
		if err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		if tr.Shape != id {
			t.Errorf("%s names shape %q; a transcript is named for its shape", path, tr.Shape)
		}
		if !bytes.Equal(raw, renderTranscript(tr)) {
			t.Errorf("%s is not in its canonical form; regenerate it rather than editing it:\n  %s", path, transcriptRegen)
		}
		out[id] = tr
	}
	return out
}

// TestCorpusTranscriptsAreWellFormed runs without the embedded engine. Every
// reference transcript names a corpus shape and only operations of the
// reference profile, by their own methods; every shape that is not pending
// has one (a pending shape's served run may not happen: its setup can fail on
// the reference profile); the current-capability goldens are exactly
// currentCapabilityShapes'; and every golden is in canonical form.
func TestCorpusTranscriptsAreWellFormed(t *testing.T) {
	c, _ := loadCorpus(t)
	byID := map[string]shape{}
	for _, s := range c.Shapes {
		byID[s.ID] = s
	}
	update := os.Getenv("BD_UPDATE_GOLDEN") == "1"
	check := func(dir string, profile *referenceProfile, wantExit bool, member func(string) bool) map[string]transcript {
		got := loadTranscripts(t, dir)
		methods := map[string]string{}
		for _, op := range profile.Operations {
			methods[op.OperationID] = op.Method
		}
		for id, tr := range got {
			if !member(id) {
				path := filepath.Join(dir, id+".json")
				if update {
					_ = os.Remove(path)
					t.Logf("removed stale %s", path)
					continue
				}
				t.Errorf("%s names no shape this directory records; delete it or regenerate with\n  %s", path, transcriptRegen)
			}
			if (tr.Exit != nil) != wantExit {
				t.Errorf("%s/%s.json: exit present = %v, want %v", dir, id, tr.Exit != nil, wantExit)
			}
			for i, r := range tr.Requests {
				where := fmt.Sprintf("%s/%s.json request %d", dir, id, i+1)
				if want, ok := methods[r.OperationID]; r.OperationID != "" && (!ok || want != r.Method) {
					t.Errorf("%s: %s %s is not an operation of the profile", where, r.Method, r.OperationID)
				}
				if !strings.HasPrefix(r.Path, "/") || r.Status < 100 || r.Status > 599 {
					t.Errorf("%s: malformed request %+v", where, r)
				}
				if !slices.IsSorted(r.Query) || !slices.IsSorted(r.Body) {
					t.Errorf("%s: query and body names must be sorted: %+v", where, r)
				}
			}
		}
		return got
	}
	reference := check(transcriptDir, loadReferenceProfile(t), false, func(id string) bool { _, ok := byID[id]; return ok })
	for _, s := range c.Shapes {
		if _, ok := reference[s.ID]; !ok && s.Pending == nil {
			t.Errorf("shape %s has no transcript in %s; regenerate with\n  %s", s.ID, transcriptDir, transcriptRegen)
		}
	}
	current := check(currentTranscriptDir, loadCurrentProfile(t), true, func(id string) bool { return slices.Contains(currentCapabilityShapes, id) })
	for _, id := range currentCapabilityShapes {
		if _, ok := byID[id]; !ok {
			t.Errorf("currentCapabilityShapes names %s, which is not a corpus shape", id)
		}
		if _, ok := current[id]; !ok {
			t.Errorf("current-capability shape %s has no transcript in %s; regenerate with\n  %s", id, currentTranscriptDir, transcriptRegen)
		}
	}
}

// requestPinMismatches holds every `requests:` pin to its shape's reference
// transcript: the pin is the reviewed number, the transcript the regenerated
// record, and they must agree. A pending shape's pin is held here too, since
// its served run still makes its requests (runShape's own count check is one
// of the failures a pending mark turns into a skip).
func requestPinMismatches(shapes []shape, transcripts map[string]transcript) []string {
	var out []string
	for _, s := range shapes {
		if s.Requests == nil {
			continue
		}
		tr, ok := transcripts[s.ID]
		switch {
		case !ok:
			out = append(out, fmt.Sprintf("%s pins requests: %d but has no transcript", s.ID, *s.Requests))
		case len(tr.Requests) != *s.Requests:
			out = append(out, fmt.Sprintf("%s pins requests: %d but its transcript holds %d; update the pin with the transcript, in the same review", s.ID, *s.Requests, len(tr.Requests)))
		}
	}
	return out
}

func TestCorpusRequestPinsMatchTranscripts(t *testing.T) {
	c, _ := loadCorpus(t)
	for _, m := range requestPinMismatches(c.Shapes, loadTranscripts(t, transcriptDir)) {
		t.Error(m)
	}
}

func TestRequestPinRuleFires(t *testing.T) {
	two, three := 2, 3
	tr := map[string]transcript{"a": {Shape: "a", Requests: make([]transcriptRequest, 2)}}
	if got := requestPinMismatches([]shape{{ID: "a", Requests: &two}}, tr); len(got) != 0 {
		t.Errorf("a matching pin reported %v", got)
	}
	if got := requestPinMismatches([]shape{{ID: "a", Requests: &three}}, tr); len(got) != 1 || !strings.Contains(got[0], "holds 2") {
		t.Errorf("a pin of 3 over a 2-request transcript reported %v, want one mismatch", got)
	}
	if got := requestPinMismatches([]shape{{ID: "b", Requests: &two}}, tr); len(got) != 1 || !strings.Contains(got[0], "no transcript") {
		t.Errorf("a pin with no transcript reported %v, want one", got)
	}
	if got := requestPinMismatches([]shape{{ID: "b"}}, tr); len(got) != 0 {
		t.Errorf("an unpinned shape reported %v", got)
	}
}

// uncoveredReferenceOperations are the reference operations no shape's
// served run gets a 2xx from, each with why. The list only shrinks: a shape
// that starts covering one fails TestCorpusTranscriptsCoverTheReferenceOperations
// until its entry is removed.
var uncoveredReferenceOperations = map[string]string{
	"health":                  "no bd command dials it: it is the unauthenticated liveness probe a deployment's load balancer uses (wire.Client.Health has no CLI caller)",
	"unsetSetting":            "its CLI caller is bd config unset, which no gc or pack shape issues against a work store",
	"listBlockingAnnotations": "its CLI caller is bd blocked --flags, which needs dependencies.blocking.isBlocked; the reference server does not advertise it, so gc-blocked-flags and gc-blocked-flags-raw refuse before dialing (pending SRV-1)",
	"countDependencyEdges":    "no bd command dials it: cmd/bd wires the GraphCounter role it serves only into bd serve",
	"listEvents":              "its CLI caller is bd events, which no gc or pack shape runs against a served workspace",
	"watchEvents":             "its CLI caller is bd events, which no gc or pack shape runs against a served workspace",
	"compareAndSetMetadata":   "no bd command dials it: cmd/bd wires the MetadataCAS role only into bd serve, and gc's conditional metadata write reaches bd as update --if-revision, an updateIssue (gc-update-if-revision)",
	"releaseIssue":            "no bd command dials it: cmd/bd wires the Releaser role only into bd serve, and gc's release reaches bd as a guarded update, an updateIssue (gc-update-release-if-current)",
	"batchCreateIssues":       "its CLI caller is bd create --file, which no gc or pack shape issues; gc's graph plans go through applyBatch (gc-create-graph)",
	"claimNextIssue":          "its CLI caller is bd ready --claim, which no gc or pack shape issues; bd close --claim-next refuses before dialing (gc-close-claim-next, pending B10)",
	"sweepIssues":             "its CLI caller is bd purge, which needs sweep tokens the reference server does not advertise (gc-purge-dry-run, pending SRV-1)",
	"rememberMemory":          "its CLI caller is bd remember, which no gc or pack shape issues (mc-prime and gc-export-all cover listMemories)",
	"forgetMemory":            "its CLI caller is bd forget, which no gc or pack shape issues (mc-prime and gc-export-all cover listMemories)",
	"getMemory":               "its CLI caller is bd recall, which no gc or pack shape issues (mc-prime and gc-export-all cover listMemories)",
}

// referenceCoverage reports the operations of profile that no transcript
// answered 2xx and that waived does not excuse, the waived ones some
// transcript does cover (stale), and the waived names that are not reference
// operations at all.
func referenceCoverage(profile *referenceProfile, transcripts map[string]transcript, waived map[string]string) (uncovered, stale, unknown []string) {
	covered := map[string]bool{}
	for _, tr := range transcripts {
		for _, r := range tr.Requests {
			if r.Status >= 200 && r.Status < 300 && r.OperationID != "" {
				covered[r.OperationID] = true
			}
		}
	}
	ops := map[string]bool{}
	for _, op := range profile.Operations {
		ops[op.OperationID] = true
		_, ok := waived[op.OperationID]
		switch {
		case !covered[op.OperationID] && !ok:
			uncovered = append(uncovered, op.OperationID)
		case covered[op.OperationID] && ok:
			stale = append(stale, op.OperationID)
		}
	}
	for op := range waived {
		if !ops[op] {
			unknown = append(unknown, op)
		}
	}
	sort.Strings(uncovered)
	sort.Strings(stale)
	sort.Strings(unknown)
	return uncovered, stale, unknown
}

// TestCorpusTranscriptsCoverTheReferenceOperations is the corpus's coverage
// floor, read from the goldens (the parts run in separate shards, so no one
// process sees every transcript; the goldens are what each part is held to).
func TestCorpusTranscriptsCoverTheReferenceOperations(t *testing.T) {
	uncovered, stale, unknown := referenceCoverage(loadReferenceProfile(t), loadTranscripts(t, transcriptDir), uncoveredReferenceOperations)
	for _, op := range uncovered {
		t.Errorf("no shape's served run gets a 2xx from reference operation %s; add a shape that does, or an entry to uncoveredReferenceOperations saying why none can", op)
	}
	for _, op := range stale {
		t.Errorf("reference operation %s is covered now; remove it from uncoveredReferenceOperations", op)
	}
	for _, op := range unknown {
		t.Errorf("uncoveredReferenceOperations names %s, which is not a reference operation", op)
	}
}

func TestReferenceCoverageRuleFires(t *testing.T) {
	profile := &referenceProfile{Operations: []referenceOperation{{OperationID: "a"}, {OperationID: "b"}, {OperationID: "c"}}}
	trs := map[string]transcript{
		"s1": {Requests: []transcriptRequest{{OperationID: "a", Status: 200}}},
		"s2": {Requests: []transcriptRequest{{OperationID: "b", Status: 409}, {OperationID: "", Status: 404}}},
	}
	u, s, k := referenceCoverage(profile, trs, map[string]string{"c": "why"})
	if !slices.Equal(u, []string{"b"}) || len(s) != 0 || len(k) != 0 {
		t.Errorf("a 409-only operation: uncovered %v stale %v unknown %v, want [b] [] []", u, s, k)
	}
	u, s, k = referenceCoverage(profile, trs, map[string]string{"a": "why", "b": "why", "c": "why", "z": "why"})
	if len(u) != 0 || !slices.Equal(s, []string{"a"}) || !slices.Equal(k, []string{"z"}) {
		t.Errorf("waivers over a covered and a foreign op: uncovered %v stale %v unknown %v, want [] [a] [z]", u, s, k)
	}
}

// The proxy records the subject's requests by name only, with the status the
// client saw (its own refusals included), and neither counts nor records a
// request from anything else on the host.
func TestReferenceProxyRecordsOnlyTheSubject(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(upstream.Close)
	proxy := newReferenceProxy(t, loadReferenceProfile(t), strings.TrimPrefix(upstream.URL, "http://"))
	front := httptest.NewServer(proxy)
	t.Cleanup(front.Close)
	const subject = "bd/9.9.9 bd-http-backend/v0"
	send := func(ua, method, path, body string) {
		t.Helper()
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		req, err := http.NewRequest(method, front.URL+path, rd)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("User-Agent", ua)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
	proxy.wire.start()
	send("Go-http-client/1.1", http.MethodGet, "/", "")
	send(subject, http.MethodGet, "/v0/beads/issues/e2e-r1?include_comments=true", "")
	send(subject, http.MethodPatch, "/v0/beads/issues/e2e-r1", `{"actor":"worker","expected_version":"7341","patch":{"due_at":"2031-01-02T03:04:05Z"}}`)
	send(subject, http.MethodGet, "/v0/beads/issues?bogus=2031-01-02", "")
	send(subject, http.MethodGet, "/v0/beads/nonsense", "")
	got, expired := proxy.wire.stop()
	if expired != 0 {
		t.Errorf("expired memo reads = %d, want 0", expired)
	}
	if refused := proxy.takeRefusals(); len(refused) != 2 {
		t.Errorf("refusals noted = %q, want the subject's 400 and 404 only, not the host's GET /", refused)
	}
	send(subject, http.MethodGet, "/v0/beads/issues/e2e-r1", "") // after stop: counted, not recorded
	want := []transcriptRequest{
		{Method: "GET", OperationID: "getIssue", Path: "/v0/beads/issues/e2e-r1", Query: []string{"include_comments"}, Status: 200},
		{Method: "PATCH", OperationID: "updateIssue", Path: "/v0/beads/issues/e2e-r1", Body: []string{"actor", "expected_version", "patch"}, Status: 200},
		{Method: "GET", OperationID: "listIssues", Path: "/v0/beads/issues", Query: []string{"bogus"}, Status: 400},
		{Method: "GET", OperationID: "", Path: "/v0/beads/nonsense", Status: 404},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("recorded\n%v\nwant\n%v", got, want)
	}
	if n := proxy.requests.Load(); n != 5 {
		t.Errorf("requests = %d, want 5: the subject's, and not the host's GET /", n)
	}
	// At least the one sent above: a port prober on the host may add its own.
	if n := proxy.foreign.Load(); n < 1 {
		t.Errorf("foreign = %d, want the host's GET / counted there", n)
	}
	rendered := string(renderTranscript(transcript{Shape: "x", Requests: got}))
	for _, value := range []string{"7341", "2031", "worker", "true"} {
		if strings.Contains(rendered, value) {
			t.Errorf("the transcript carries the value %q; it records names only:\n%s", value, rendered)
		}
	}
}

func TestMintedIDsGetFirstSeenPlaceholders(t *testing.T) {
	known := map[string]bool{"e2e-r1": true, "e2e-ep.3": true}
	in := []transcriptRequest{
		{Path: "/v0/beads/issues/e2e-zq9x:close"},
		{Path: "/v0/beads/issues/e2e-r1"},
		{Path: "/v0/beads/issues/e2e-ep.3/comments"},
		{Path: "/v0/beads/issues/e2e-a7b2"},
		{Path: "/v0/beads/issues/e2e-zq9x"},
		{Path: "/v0/beads/config/types.custom"},
	}
	var got []string
	for _, r := range maskMintedIDs(in, known) {
		got = append(got, r.Path)
	}
	want := []string{
		"/v0/beads/issues/<minted-1>:close",
		"/v0/beads/issues/e2e-r1",
		"/v0/beads/issues/e2e-ep.3/comments",
		"/v0/beads/issues/<minted-2>",
		"/v0/beads/issues/<minted-1>",
		"/v0/beads/config/types.custom",
	}
	if !slices.Equal(got, want) {
		t.Errorf("masked paths\n%q\nwant\n%q", got, want)
	}
	if in[0].Path != "/v0/beads/issues/e2e-zq9x:close" {
		t.Error("maskMintedIDs rewrote its input")
	}
}

// A transcript drift names the requests that changed: here a claim moving
// from updateIssue to claimIssue plus a read-back (the shape of fix D7), and a
// planted duplicate read.
func TestTranscriptDriftNamesTheChangedRequest(t *testing.T) {
	ctx := transcriptRequest{Method: "GET", OperationID: "getContext", Path: "/v0/beads/context", Status: 200}
	get := transcriptRequest{Method: "GET", OperationID: "getIssue", Path: "/v0/beads/issues/e2e-t-claim", Status: 200}
	patch := transcriptRequest{Method: "PATCH", OperationID: "updateIssue", Path: "/v0/beads/issues/e2e-t-claim", Body: []string{"actor", "claim", "patch"}, Status: 200}
	claim := transcriptRequest{Method: "POST", OperationID: "claimIssue", Path: "/v0/beads/issues/e2e-t-claim:claim", Body: []string{"actor"}, Status: 200}
	base := renderTranscript(transcript{Shape: "gc-update-claim", Requests: []transcriptRequest{ctx, get, patch}})
	d7 := renderTranscript(transcript{Shape: "gc-update-claim", Requests: []transcriptRequest{ctx, get, claim, get}})
	diff := transcriptDiff(base, d7)
	if !strings.Contains(diff, `- `) || !strings.Contains(diff, `"updateIssue"`) || !strings.Contains(diff, `"claimIssue"`) || strings.Contains(diff, `"getContext"`) {
		t.Errorf("drift of a moved claim:\n%s", diff)
	}
	dup := renderTranscript(transcript{Shape: "gc-update-claim", Requests: []transcriptRequest{ctx, get, get, patch}})
	if diff := transcriptDiff(base, dup); !strings.Contains(diff, `+     {"method":"GET","operation_id":"getIssue"`) {
		t.Errorf("drift of a duplicate read:\n%s", diff)
	}
	if bytes.Equal(base, d7) || bytes.Equal(base, dup) {
		t.Error("renderings of different transcripts are equal")
	}
}

// TestExpiredMemoReadRuleFires drives the recorder on a fake clock: a re-read
// the read memo let through only because its entry aged out is left out (and
// counted), while every re-read the memo should have answered, as a memo
// regression would send it, is still recorded, as is any re-read the memo
// could not answer (after a write, after a non-2xx answer, of a different
// URI, or of an operation the memo does not serve).
func TestExpiredMemoReadRuleFires(t *testing.T) {
	ops := map[string]*referenceOperation{
		"getIssue":    {Method: http.MethodGet, OperationID: wire.OpGetIssue},
		"getSetting":  {Method: http.MethodGet, OperationID: wire.OpGetSetting},
		"listIssues":  {Method: http.MethodGet, OperationID: "listIssues"},
		"updateIssue": {Method: http.MethodPatch, OperationID: "updateIssue"},
	}
	clock := time.Unix(1_800_000_000, 0)
	rec := &transcriptRecorder{now: func() time.Time { return clock }}
	type step struct {
		advance time.Duration
		op      string
		target  string
		status  int
	}
	run := func(steps []step) (paths []string, expired int) {
		rec.start()
		for _, s := range steps {
			clock = clock.Add(s.advance)
			op := ops[s.op]
			r := httptest.NewRequest(op.Method, s.target, nil)
			w := rec.track(httptest.NewRecorder(), r, op, nil)
			w.WriteHeader(s.status)
		}
		got, expired := rec.stop()
		for _, g := range got {
			paths = append(paths, g.OperationID+" "+g.Path)
		}
		return paths, expired
	}
	const (
		i1 = "/v0/beads/issues/e2e-a"
		i2 = "/v0/beads/issues/e2e-b"
	)
	ttl := wire.ReadMemoTTL
	for _, tc := range []struct {
		name        string
		steps       []step
		wantPaths   int
		wantExpired int
	}{
		{"a re-read within the TTL is a memo regression: recorded", []step{
			{0, "getIssue", i1, 200}, {time.Millisecond, "getIssue", i1, 200},
		}, 2, 0},
		{"a re-read the TTL let through: left out", []step{
			{0, "getIssue", i1, 200}, {ttl + time.Millisecond, "getIssue", i1, 200},
		}, 1, 1},
		{"the age runs from the latest answer, as the memo re-stores each miss", []step{
			{0, "getIssue", i1, 200}, {ttl, "getIssue", i1, 200}, {time.Second, "getIssue", i1, 200},
		}, 2, 1},
		{"settings reads are memoized too", []step{
			{0, "getSetting", "/v0/beads/settings/k", 200}, {2 * ttl, "getSetting", "/v0/beads/settings/k", 200},
		}, 1, 1},
		{"a write in between clears the memo: recorded", []step{
			{0, "getIssue", i1, 200}, {ttl, "updateIssue", i1, 200}, {time.Millisecond, "getIssue", i1, 200},
		}, 3, 0},
		{"a non-2xx answer is never memoized: recorded", []step{
			{0, "getIssue", i1, 404}, {2 * ttl, "getIssue", i1, 200},
		}, 2, 0},
		{"another query is another key: recorded", []step{
			{0, "getIssue", i1, 200}, {2 * ttl, "getIssue", i1 + "?include_comments=true", 200},
		}, 2, 0},
		{"another row: recorded", []step{
			{0, "getIssue", i1, 200}, {2 * ttl, "getIssue", i2, 200},
		}, 2, 0},
		{"listings are not memoized: recorded", []step{
			{0, "listIssues", "/v0/beads/issues", 200}, {2 * ttl, "listIssues", "/v0/beads/issues", 200},
		}, 2, 0},
	} {
		paths, expired := run(tc.steps)
		if len(paths) != tc.wantPaths || expired != tc.wantExpired {
			t.Errorf("%s: recorded %q and left out %d, want %d recorded and %d left out", tc.name, paths, expired, tc.wantPaths, tc.wantExpired)
		}
	}
	// A window starts with the memo empty, as each bd process does.
	run([]step{{0, "getIssue", i1, 200}})
	if paths, expired := run([]step{{2 * ttl, "getIssue", i1, 200}}); len(paths) != 1 || expired != 0 {
		t.Errorf("a read in a new window was left out (%q, %d); the memo does not outlive the process", paths, expired)
	}
}
