package bdhttp_test

// The bd connect front door (G3): testdata/connect_front_door.yaml gives every
// runnable command in cmd/bd/testdata/command_tree/tree.golden a row saying
// what it does on a workspace attached with `bd connect`, as it is on main.
// This file reads that YAML and the tree golden as data and holds the YAML to
// its rules (test lane, no build tag); e2e_connect_front_door_test.go runs
// its cases (embedded lane).
//
// The pattern is internal/httpclient's legitimatelyUnsupported and cmd/bd's
// TestProxiedServerHistoryRemoteRefusalFrontDoorMatrix: a table that must
// name every member of a population read from a pinned source, so a new
// command cannot reach the CLI without someone deciding what it does over the
// wire.

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/httpclient/encode"
	"gopkg.in/yaml.v3"
)

const (
	connectFrontDoorPath = "testdata/connect_front_door.yaml"
	// commandTreeGoldenPath is cmd/bd's pinned command tree, read as data
	// (//cmd/bd:testdata/command_tree/tree.golden under Bazel).
	commandTreeGoldenPath = "../../cmd/bd/testdata/command_tree/tree.golden"
	// frontDoorParts is how many TestE2E_ConnectFrontDoorMatrixPart<k> tests
	// split the cases. Changing it means declaring or deleting Part tests in
	// e2e_connect_front_door_test.go and listing them in frontDoorPartTests
	// (frontDoorPartNameErr and frontDoorPartsDeclaredErr check).
	frontDoorParts = 4
	// maxUnrunPendingRulings is the number of pending-ruling rows that run no
	// case (a tracker's network, a TTY). It may only shrink: to give such a
	// row cases, lower it by one.
	maxUnrunPendingRulings = 27
	// frontDoorRemedy ends every problem the classifier reports.
	frontDoorRemedy = "see the schema in the header of backend/http/" + connectFrontDoorPath
)

// frontDoorFile is connect_front_door.yaml.
type frontDoorFile struct {
	Files    []frontDoorAsset   `yaml:"files"`
	Seed     [][]string         `yaml:"seed"`
	SeedKV   []frontDoorKV      `yaml:"seed_kv"`
	Commands []frontDoorCommand `yaml:"commands"`
}

// frontDoorAsset is a file every workspace gets a copy of ({file:NAME}).
type frontDoorAsset struct {
	Name      string `yaml:"name"`
	Content   string `yaml:"content"`
	InstallTo string `yaml:"install_to"`
}

// frontDoorKV is a kv key planted in every store before the cases run.
type frontDoorKV struct {
	Key   string `yaml:"key"`
	Value string `yaml:"value"`
}

// frontDoorCommand is one row: a command path from tree.golden, with cases or
// with a class (local-only, pending-ruling) and a reason.
type frontDoorCommand struct {
	Command string          `yaml:"command"`
	Class   string          `yaml:"class"`
	Reason  string          `yaml:"reason"`
	Cases   []frontDoorCase `yaml:"cases"`
}

// frontDoorCase is one argv with one class: works, refuses or pending-ruling.
type frontDoorCase struct {
	ID          string     `yaml:"id"`
	Class       string     `yaml:"class"`
	Note        string     `yaml:"note"`
	NativeFix   []string   `yaml:"native_fix"`
	Ledger      []string   `yaml:"ledger"`
	LedgerDrift string     `yaml:"ledger_drift"`
	Fresh       bool       `yaml:"fresh"`
	Setup       [][]string `yaml:"setup"`
	Env         []string   `yaml:"env"`
	Stdin       string     `yaml:"stdin"`
	Args        []string   `yaml:"args"`

	// works
	Compare      string     `yaml:"compare"`
	IgnoreFields []string   `yaml:"ignore_fields"`
	ExpectStdout string     `yaml:"expect_stdout"`
	Verify       [][]string `yaml:"verify"`
	// works (both sides) and refuses (served side)
	Exit *int `yaml:"exit"`

	// refuses
	Words string `yaml:"words"`
	Code  string `yaml:"code"`

	// refuses and pending-ruling: the embedded oracle
	EmbeddedExit *int `yaml:"embedded_exit"`
	SkipEmbedded bool `yaml:"skip_embedded"`

	// pending-ruling
	Ruling   string             `yaml:"ruling"`
	Observed *frontDoorObserved `yaml:"observed"`
}

// frontDoorObserved is a pending-ruling case's pinned served behavior.
type frontDoorObserved struct {
	Exit         *int   `yaml:"exit"`
	Listens      bool   `yaml:"listens"`
	Stdout       string `yaml:"stdout"`
	StdoutAbsent string `yaml:"stdout_absent"`
	Stderr       string `yaml:"stderr"`
	Writes       string `yaml:"writes"`
}

// frontDoorRun is one case with the command row it belongs to.
type frontDoorRun struct {
	command string
	c       frontDoorCase
}

const (
	classWorks         = "works"
	classRefuses       = "refuses"
	classPendingRuling = "pending-ruling"
	classLocalOnly     = "local-only"
	// noShapeRow is the ledger row a raw SearchIssues refusal comes from; the
	// fix that retires it must flip every case that cites it.
	noShapeRow   = "E-IssueFilter.noShape"
	noShapeWords = "raw SearchIssues"
)

func (c frontDoorCase) wantExit() int {
	if c.Exit == nil {
		return 0
	}
	return *c.Exit
}

func (c frontDoorCase) wantEmbeddedExit() int {
	if c.EmbeddedExit == nil {
		return 0
	}
	return *c.EmbeddedExit
}

func (c frontDoorCase) compareMode() string {
	if c.Compare == "" {
		return "json"
	}
	return c.Compare
}

func loadConnectFrontDoor(t *testing.T) *frontDoorFile {
	t.Helper()
	raw, err := os.ReadFile(connectFrontDoorPath)
	if err != nil {
		t.Fatalf("read %s: %v", connectFrontDoorPath, err)
	}
	f, err := parseConnectFrontDoor(raw)
	if err != nil {
		t.Fatalf("parse %s: %v", connectFrontDoorPath, err)
	}
	return f
}

func parseConnectFrontDoor(raw []byte) (*frontDoorFile, error) {
	var f frontDoorFile
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return nil, err
	}
	return &f, nil
}

func loadRunnableCommands(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(commandTreeGoldenPath)
	if err != nil {
		t.Fatalf("read %s: %v", commandTreeGoldenPath, err)
	}
	out := runnableCommands(string(raw))
	if len(out) < 200 {
		t.Fatalf("%s lists %d runnable commands; runnableCommands no longer reads the golden's format", commandTreeGoldenPath, len(out))
	}
	return out
}

// runnableCommands reads tree.golden: every command header ("bd" or "bd ..."
// at column 0) whose block says "  runnable: true".
func runnableCommands(golden string) []string {
	var out []string
	cur := ""
	sc := bufio.NewScanner(strings.NewReader(golden))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "bd" || strings.HasPrefix(line, "bd "):
			cur = line
		case line == "  runnable: true" && cur != "":
			out = append(out, cur)
			cur = ""
		}
	}
	return out
}

// frontDoorCoverageProblems: every runnable command has exactly one row, and
// every row names a runnable command.
func frontDoorCoverageProblems(runnable []string, f *frontDoorFile) []string {
	var out []string
	rows := map[string]int{}
	for _, c := range f.Commands {
		rows[c.Command]++
		if rows[c.Command] == 2 {
			out = append(out, fmt.Sprintf("%s: %q has two rows; merge them into one", connectFrontDoorPath, c.Command))
		}
	}
	want := map[string]bool{}
	for _, cmd := range runnable {
		want[cmd] = true
		if rows[cmd] == 0 {
			out = append(out, fmt.Sprintf("%q is a runnable command in cmd/bd/testdata/command_tree/tree.golden with no row in backend/http/%s: classify what it does on a bd connect workspace as works, refuses, local-only or pending-ruling (%s)", cmd, connectFrontDoorPath, frontDoorRemedy))
		}
	}
	for _, c := range f.Commands {
		if !want[c.Command] {
			out = append(out, fmt.Sprintf("%s: row %q names no runnable command in tree.golden; remove or rename the row", connectFrontDoorPath, c.Command))
		}
	}
	return out
}

var (
	frontDoorCaseIDRe    = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	frontDoorNativeFixRe = regexp.MustCompile(`^[BCDE][0-9]+$`)
)

// frontDoorFormProblems holds the file to its schema and to the divergence
// ledger. maxUnrun is the unrun pending-ruling ratchet.
func frontDoorFormProblems(f *frontDoorFile, ledger []encode.Row, maxUnrun int) []string {
	var out []string
	byID := map[string]encode.Row{}
	for _, r := range ledger {
		byID[r.ID] = r
	}
	ids := map[string]bool{}
	// cited holds each ledger row's citing cases, with the command row each
	// belongs to: an F- row is about one command, and only that command's
	// cases pin it or contradict it.
	cited := map[string][]frontDoorRun{}
	unrun := 0
	for _, row := range f.Commands {
		add := func(id, format string, a ...any) {
			where := row.Command
			if id != "" {
				where += " (case " + id + ")"
			}
			out = append(out, fmt.Sprintf("%s: %s: %s; %s", connectFrontDoorPath, where, fmt.Sprintf(format, a...), frontDoorRemedy))
		}
		if row.Command != "bd" && !strings.HasPrefix(row.Command, "bd ") {
			add("", "a row's command is a tree.golden path starting with \"bd\"")
			continue
		}
		words := strings.Fields(row.Command)[1:]
		if len(row.Cases) == 0 {
			switch row.Class {
			case classLocalOnly:
			case classPendingRuling:
				unrun++
			default:
				add("", "a row without cases must be class local-only or pending-ruling, not %q", row.Class)
			}
			if strings.TrimSpace(row.Reason) == "" {
				add("", "class %s needs a reason", row.Class)
			}
			continue
		}
		if row.Class != "" || row.Reason != "" {
			add("", "a row with cases takes its classes from them; drop the row's class and reason")
		}
		for _, k := range row.Cases {
			if !frontDoorCaseIDRe.MatchString(k.ID) {
				add(k.ID, "case id %q is not kebab-case", k.ID)
			}
			if ids[k.ID] {
				add(k.ID, "case id %q is used twice", k.ID)
			}
			ids[k.ID] = true
			if len(k.Args) < len(words) || strings.Join(k.Args[:len(words)], " ") != strings.Join(words, " ") {
				add(k.ID, "args %q do not run %s", k.Args, row.Command)
			}
			for _, e := range k.Env {
				if !strings.Contains(e, "=") {
					add(k.ID, "env %q is not KEY=VALUE", e)
				}
			}
			for _, n := range k.NativeFix {
				if !frontDoorNativeFixRe.MatchString(n) {
					add(k.ID, "native_fix %q is not a native-fix decision id (B1, D8, ...)", n)
				}
			}
			if k.SkipEmbedded && strings.TrimSpace(k.Note) == "" {
				add(k.ID, "skip_embedded needs a note saying why the oracle cannot run")
			}
			for _, re := range []string{k.Words, k.ExpectStdout} {
				if _, err := regexp.Compile(re); err != nil {
					add(k.ID, "regexp %q: %v", re, err)
				}
			}
			switch k.Class {
			case classWorks:
				switch k.Compare {
				case "", "json", "text", "lines", "exit":
				default:
					add(k.ID, "compare %q is not json, text, lines or exit", k.Compare)
				}
				if k.Words != "" || k.Code != "" || k.Observed != nil || k.Ruling != "" || k.EmbeddedExit != nil || k.SkipEmbedded {
					add(k.ID, "works takes no words, code, observed, ruling, embedded_exit or skip_embedded")
				}
			case classRefuses:
				if k.Exit == nil || *k.Exit == 0 || k.Words == "" {
					add(k.ID, "refuses needs a nonzero exit and words")
				}
				if k.Compare != "" || len(k.IgnoreFields) > 0 || k.ExpectStdout != "" || len(k.Verify) > 0 || k.Observed != nil || k.Ruling != "" {
					add(k.ID, "refuses takes no compare, ignore_fields, expect_stdout, verify, observed or ruling")
				}
				if strings.Contains(k.Words, noShapeWords) && !slices.Contains(k.Ledger, noShapeRow) {
					add(k.ID, "a raw SearchIssues refusal cites ledger row %s", noShapeRow)
				}
			case classPendingRuling:
				if k.Exit != nil || k.Words != "" || k.Code != "" || k.Compare != "" || len(k.IgnoreFields) > 0 || k.ExpectStdout != "" || len(k.Verify) > 0 {
					add(k.ID, "pending-ruling pins its behavior in observed; it takes no exit, words, code, compare, ignore_fields, expect_stdout or verify")
				}
				if strings.TrimSpace(k.Ruling) == "" || k.Observed == nil {
					add(k.ID, "pending-ruling needs ruling and observed")
					break
				}
				o := k.Observed
				if (o.Exit == nil) == !o.Listens {
					add(k.ID, "observed needs exactly one of exit and listens")
				}
				switch o.Writes {
				case "none":
				case "local":
					if !k.Fresh {
						add(k.ID, "observed writes: local needs fresh: true, or the part's served workspace drifts from its twin")
					}
				default:
					add(k.ID, "observed writes must be none or local, not %q", o.Writes)
				}
				for _, re := range []string{o.Stdout, o.Stderr, o.StdoutAbsent} {
					if _, err := regexp.Compile(re); err != nil {
						add(k.ID, "regexp %q: %v", re, err)
					}
				}
			default:
				add(k.ID, "class %q: a case is works, refuses or pending-ruling", k.Class)
			}
			disagrees := false
			for _, id := range k.Ledger {
				r, ok := byID[id]
				if !ok {
					add(k.ID, "cites ledger row %s, which encode.Ledger() does not define", id)
					continue
				}
				if r.Kind == encode.KindRetired {
					add(k.ID, "cites ledger row %s, which is retired; drop the citation", id)
				}
				cited[id] = append(cited[id], frontDoorRun{command: row.Command, c: k})
				if r.Kind == encode.KindRefuse && r.Command != "" && row.Command == "bd "+r.Command && k.Class != classRefuses {
					disagrees = true
					if k.LedgerDrift == "" {
						add(k.ID, "cites %s, which says `bd %s %s` refuses, but the case is %s: fix the ledger row, or record the disagreement in ledger_drift", id, r.Command, r.Flag, k.Class)
					}
				}
			}
			if k.LedgerDrift != "" && !disagrees {
				add(k.ID, "ledger_drift is set, but every row it cites agrees with the case; drop the stale note")
			}
		}
	}
	for _, r := range ledger {
		if !strings.HasPrefix(r.ID, "F-") || r.Kind == encode.KindRetired || r.Command == "" {
			continue
		}
		pinned := false
		for _, run := range cited[r.ID] {
			if run.command == "bd "+r.Command && (r.Flag == "" || slices.Contains(run.c.Args, r.Flag)) {
				pinned = true
			}
		}
		if !pinned {
			out = append(out, fmt.Sprintf("%s: ledger row %s (`bd %s %s`) is pinned by no front-door case: cite it from a case of row \"bd %s\" whose args run it; %s", connectFrontDoorPath, r.ID, r.Command, r.Flag, r.Command, frontDoorRemedy))
		}
	}
	switch {
	case unrun > maxUnrun:
		out = append(out, fmt.Sprintf("%s: %d pending-ruling rows run no case, more than maxUnrunPendingRulings (%d): give the new row cases (a works, refuses or run pending-ruling case) instead", connectFrontDoorPath, unrun, maxUnrun))
	case unrun < maxUnrun:
		out = append(out, fmt.Sprintf("%s: %d pending-ruling rows run no case, fewer than maxUnrunPendingRulings (%d): lower the constant to %d so the count cannot grow back", connectFrontDoorPath, unrun, maxUnrun, unrun))
	}
	return out
}

// frontDoorRuns flattens the cases in file order.
func frontDoorRuns(f *frontDoorFile) []frontDoorRun {
	var out []frontDoorRun
	for _, row := range f.Commands {
		for _, c := range row.Cases {
			out = append(out, frontDoorRun{command: row.Command, c: c})
		}
	}
	return out
}

// frontDoorPart is part k (1-based) of parts: fresh and non-fresh runs are
// each dealt round robin in file order, so the expensive fresh cases spread
// evenly, and a part keeps file order.
func frontDoorPart(runs []frontDoorRun, part, parts int) []frontDoorRun {
	var out []frontDoorRun
	fresh, plain := 0, 0
	for _, r := range runs {
		n := &plain
		if r.c.Fresh {
			n = &fresh
		}
		if *n%parts == part-1 {
			out = append(out, r)
		}
		*n++
	}
	return out
}

var frontDoorPartNameRe = regexp.MustCompile(`^TestE2E_ConnectFrontDoorMatrixPart([1-9][0-9]*)$`)

// frontDoorPartNameErr checks that the test running part k is named for it
// and that k is one of frontDoorParts, so a Part test added without raising
// the constant fails instead of doubling cases. The other direction (the
// constant raised, or a Part test dropped) is frontDoorPartsDeclaredErr's.
func frontDoorPartNameErr(name string, part int) error {
	m := frontDoorPartNameRe.FindStringSubmatch(name)
	if m == nil || m[1] != fmt.Sprint(part) {
		return fmt.Errorf("%s runs front-door part %d; it must be named TestE2E_ConnectFrontDoorMatrixPart%d", name, part, part)
	}
	if part < 1 || part > frontDoorParts {
		return fmt.Errorf("%s runs part %d, but frontDoorParts is %d: raise the constant with the new Part test", name, part, frontDoorParts)
	}
	return nil
}

// frontDoorPartsDeclaredErr checks the declared Part tests (their function
// names, in order) are exactly Part1..frontDoorParts, so raising the constant
// without declaring the new Part, or dropping one, cannot silently leave a
// part's cases unrun.
func frontDoorPartsDeclaredErr(names []string) error {
	var want []string
	for k := 1; k <= frontDoorParts; k++ {
		want = append(want, fmt.Sprintf("TestE2E_ConnectFrontDoorMatrixPart%d", k))
	}
	if !slices.Equal(names, want) {
		return fmt.Errorf("frontDoorPartTests lists %q, but frontDoorParts is %d, which needs %q: declare (or delete) Part tests to match the constant and list them all", names, frontDoorParts, want)
	}
	return nil
}

func TestConnectFrontDoorClassifiesEveryCommand(t *testing.T) {
	f := loadConnectFrontDoor(t)

	t.Run("every_runnable_command_has_a_row", func(t *testing.T) {
		for _, p := range frontDoorCoverageProblems(loadRunnableCommands(t), f) {
			t.Error(p)
		}
	})

	t.Run("the_file_is_well_formed", func(t *testing.T) {
		for _, p := range frontDoorFormProblems(f, encode.Ledger(), maxUnrunPendingRulings) {
			t.Error(p)
		}
		counts := map[string]int{}
		for _, row := range f.Commands {
			if len(row.Cases) == 0 {
				counts[row.Class+" (unrun)"]++
				continue
			}
			for _, k := range row.Cases {
				counts[k.Class]++
			}
		}
		var keys []string
		for k := range counts {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			t.Logf("%s: %d", k, counts[k])
		}
	})

	t.Run("each_rule_fires", testFrontDoorRulesFire)

	t.Run("every_case_runs_in_exactly_one_part", func(t *testing.T) {
		runs := frontDoorRuns(f)
		pos := map[string]int{}
		freshTotal := 0
		for i, r := range runs {
			pos[r.c.ID] = i
			if r.c.Fresh {
				freshTotal++
			}
		}
		seen := map[string]int{}
		for k := 1; k <= frontDoorParts; k++ {
			if err := frontDoorPartNameErr(fmt.Sprintf("TestE2E_ConnectFrontDoorMatrixPart%d", k), k); err != nil {
				t.Error(err)
			}
			last, fresh := -1, 0
			for _, r := range frontDoorPart(runs, k, frontDoorParts) {
				seen[r.c.ID]++
				if pos[r.c.ID] < last {
					t.Errorf("part %d runs %s out of file order", k, r.c.ID)
				}
				last = pos[r.c.ID]
				if r.c.Fresh {
					fresh++
				}
			}
			if limit := (freshTotal + frontDoorParts - 1) / frontDoorParts; fresh > limit {
				t.Errorf("part %d runs %d fresh cases, more than %d", k, fresh, limit)
			}
		}
		for _, r := range runs {
			if seen[r.c.ID] != 1 {
				t.Errorf("case %s runs in %d parts, want exactly 1", r.c.ID, seen[r.c.ID])
			}
		}
		if err := frontDoorPartNameErr(fmt.Sprintf("TestE2E_ConnectFrontDoorMatrixPart%d", frontDoorParts+1), frontDoorParts+1); err == nil {
			t.Errorf("frontDoorPartNameErr accepts a part beyond frontDoorParts")
		}
		if err := frontDoorPartNameErr("TestE2E_ConnectFrontDoorMatrixPart2", 1); err == nil {
			t.Errorf("frontDoorPartNameErr accepts a test named for another part")
		}
		var all []string
		for k := 1; k <= frontDoorParts; k++ {
			all = append(all, fmt.Sprintf("TestE2E_ConnectFrontDoorMatrixPart%d", k))
		}
		if err := frontDoorPartsDeclaredErr(all); err != nil {
			t.Errorf("frontDoorPartsDeclaredErr rejects Part1..frontDoorParts: %v", err)
		}
		if err := frontDoorPartsDeclaredErr(all[:len(all)-1]); err == nil {
			t.Errorf("frontDoorPartsDeclaredErr accepts one Part test fewer than frontDoorParts")
		}
		if err := frontDoorPartsDeclaredErr(append(all[:len(all):len(all)], fmt.Sprintf("TestE2E_ConnectFrontDoorMatrixPart%d", frontDoorParts+1))); err == nil {
			t.Errorf("frontDoorPartsDeclaredErr accepts one Part test more than frontDoorParts")
		}
	})
}

// testFrontDoorRulesFire feeds each checker a minimal file with one rule
// broken and expects that rule's problem: the falsification of the
// classifier, as TestTheCompositionCatchesAWrongParameter does elsewhere.
func testFrontDoorRulesFire(t *testing.T) {
	one, zero := 1, 0
	golden := "# header\n\nbd\n  use: \"bd\"\n  runnable: true\n\nbd admin\n  runnable: false\n\nbd admin cleanup\n  runnable: true\n\nbd newcmd\n  runnable: true\n"
	if got := strings.Join(runnableCommands(golden), ","); got != "bd,bd admin cleanup,bd newcmd" {
		t.Fatalf("runnableCommands = %q", got)
	}
	base := func() *frontDoorFile {
		return &frontDoorFile{Commands: []frontDoorCommand{
			{Command: "bd", Class: classLocalOnly, Reason: "help"},
			{Command: "bd admin cleanup", Cases: []frontDoorCase{{
				ID: "c", Class: classRefuses, Args: []string{"admin", "cleanup"}, Exit: &one, Words: "x", Ledger: []string{"F-x"},
			}}},
		}}
	}
	ledger := []encode.Row{
		{ID: "F-x", Kind: encode.KindRefuse, Command: "admin cleanup"},
		{ID: "F-gone", Kind: encode.KindRetired, Command: "admin cleanup"},
		{ID: noShapeRow, Kind: encode.KindRefuse},
		{ID: "L1", Kind: encode.KindDegrade},
	}
	twoRows := []string{"bd", "bd admin cleanup"}
	caseOf := func(f *frontDoorFile) *frontDoorCase { return &f.Commands[1].Cases[0] }
	pending := func(f *frontDoorFile) *frontDoorCase {
		k := caseOf(f)
		k.Class, k.Exit, k.Words, k.LedgerDrift = classPendingRuling, nil, "", "x"
		k.Ruling, k.Observed = "D9", &frontDoorObserved{Exit: &zero, Writes: "none"}
		return k
	}
	for _, tc := range []struct {
		name string
		// mut breaks f and returns the runnable list for a coverage check, or
		// nil for a form check.
		mut  func(f *frontDoorFile) []string
		want string
	}{
		{"the base passes", func(f *frontDoorFile) []string { return nil }, ""},
		{"a new runnable command has no row", func(f *frontDoorFile) []string { return runnableCommands(golden) }, `"bd newcmd" is a runnable command`},
		{"a stale row", func(f *frontDoorFile) []string {
			f.Commands = append(f.Commands, frontDoorCommand{Command: "bd gone", Class: classLocalOnly, Reason: "x"})
			return twoRows
		}, `row "bd gone" names no runnable command`},
		{"a duplicate row", func(f *frontDoorFile) []string {
			f.Commands = append(f.Commands, frontDoorCommand{Command: "bd", Class: classLocalOnly, Reason: "x"})
			return twoRows
		}, `"bd" has two rows`},
		{"args run another command", func(f *frontDoorFile) []string { caseOf(f).Args = []string{"cleanup"}; return nil }, "do not run bd admin cleanup"},
		{"a refusal that exits 0", func(f *frontDoorFile) []string { caseOf(f).Exit = &zero; return nil }, "refuses needs a nonzero exit"},
		{"a duplicate case id", func(f *frontDoorFile) []string {
			f.Commands[1].Cases = append(f.Commands[1].Cases, *caseOf(f))
			return nil
		}, `case id "c" is used twice`},
		{"an uncited F- row", func(f *frontDoorFile) []string { caseOf(f).Ledger = nil; return nil }, "ledger row F-x (`bd admin cleanup `) is pinned by no front-door case"},
		{"an F- row cited only from another command's case", func(f *frontDoorFile) []string {
			caseOf(f).Ledger = nil
			f.Commands = append(f.Commands, frontDoorCommand{Command: "bd other", Cases: []frontDoorCase{{
				ID: "o", Class: classRefuses, Args: []string{"other"}, Exit: &one, Words: "x", Ledger: []string{"F-x"},
			}}})
			return nil
		}, "ledger row F-x (`bd admin cleanup `) is pinned by no front-door case"},
		{"another command's works case citing a refuse row is no disagreement", func(f *frontDoorFile) []string {
			f.Commands = append(f.Commands, frontDoorCommand{Command: "bd other", Cases: []frontDoorCase{{
				ID: "o", Class: classWorks, Args: []string{"other"}, Ledger: []string{"F-x"},
			}}})
			return nil
		}, ""},
		{"a retired citation", func(f *frontDoorFile) []string { caseOf(f).Ledger = []string{"F-x", "F-gone"}; return nil }, "F-gone, which is retired"},
		{"an unknown citation", func(f *frontDoorFile) []string { caseOf(f).Ledger = []string{"F-x", "F-nope"}; return nil }, "F-nope, which encode.Ledger() does not define"},
		{"a raw SearchIssues refusal without the noShape citation", func(f *frontDoorFile) []string {
			caseOf(f).Words = "a raw SearchIssues call"
			return nil
		}, "a raw SearchIssues refusal cites ledger row " + noShapeRow},
		{"a disagreement without drift", func(f *frontDoorFile) []string {
			k := caseOf(f)
			k.Class, k.Exit, k.Words = classWorks, nil, ""
			return nil
		}, "cites F-x, which says `bd admin cleanup ` refuses, but the case is works"},
		{"a stale drift note", func(f *frontDoorFile) []string { caseOf(f).LedgerDrift = "says it refuses"; return nil }, "drop the stale note"},
		{"skip_embedded without a note", func(f *frontDoorFile) []string { caseOf(f).SkipEmbedded = true; return nil }, "skip_embedded needs a note"},
		{"local writes outside a fresh pair", func(f *frontDoorFile) []string {
			k := caseOf(f)
			k.Class, k.Exit, k.Words, k.LedgerDrift = classPendingRuling, nil, "", "x"
			k.Ruling, k.Observed = "D9", &frontDoorObserved{Exit: &zero, Writes: "local"}
			return nil
		}, "writes: local needs fresh"},
		{"the unrun ratchet grows", func(f *frontDoorFile) []string {
			f.Commands = append(f.Commands, frontDoorCommand{Command: "bd z", Class: classPendingRuling, Reason: "tty"})
			return nil
		}, "1 pending-ruling rows run no case, more than maxUnrunPendingRulings (0)"},
		{"a pending ruling that passes", func(f *frontDoorFile) []string { pending(f); return nil }, ""},
		{"observed with neither exit nor listens", func(f *frontDoorFile) []string { pending(f).Observed.Exit = nil; return nil }, "exactly one of exit and listens"},
		{"observed with both exit and listens", func(f *frontDoorFile) []string { pending(f).Observed.Listens = true; return nil }, "exactly one of exit and listens"},
		{"observed writes something else", func(f *frontDoorFile) []string { pending(f).Observed.Writes = "remote"; return nil }, `observed writes must be none or local, not "remote"`},
		{"a pending ruling without its ruling", func(f *frontDoorFile) []string { pending(f).Ruling = ""; return nil }, "pending-ruling needs ruling and observed"},
		{"a pending ruling with an exit", func(f *frontDoorFile) []string { pending(f).Exit = &one; return nil }, "pending-ruling pins its behavior in observed"},
		{"a works case with words", func(f *frontDoorFile) []string {
			k := caseOf(f)
			k.Class, k.Exit, k.LedgerDrift = classWorks, nil, "x"
			return nil
		}, "works takes no words"},
		{"a refusal with a compare mode", func(f *frontDoorFile) []string { caseOf(f).Compare = "text"; return nil }, "refuses takes no compare"},
		{"an unknown compare mode", func(f *frontDoorFile) []string {
			k := caseOf(f)
			k.Class, k.Exit, k.Words, k.LedgerDrift, k.Compare = classWorks, nil, "", "x", "yaml"
			return nil
		}, `compare "yaml" is not json, text, lines or exit`},
		{"an unknown case class", func(f *frontDoorFile) []string { caseOf(f).Class = "maybe"; return nil }, `class "maybe": a case is works, refuses or pending-ruling`},
		{"a malformed native_fix", func(f *frontDoorFile) []string { caseOf(f).NativeFix = []string{"fix-1"}; return nil }, `native_fix "fix-1" is not a native-fix decision id`},
		{"env without a value", func(f *frontDoorFile) []string { caseOf(f).Env = []string{"BD_X"}; return nil }, `env "BD_X" is not KEY=VALUE`},
		{"a case id that is not kebab-case", func(f *frontDoorFile) []string { caseOf(f).ID = "Case_1"; return nil }, `case id "Case_1" is not kebab-case`},
		{"a bad regexp", func(f *frontDoorFile) []string { caseOf(f).Words = "("; return nil }, `regexp "(":`},
		{"a row command not under bd", func(f *frontDoorFile) []string { f.Commands[0].Command = "git"; return nil }, `a row's command is a tree.golden path starting with "bd"`},
		{"a row without cases or a valid class", func(f *frontDoorFile) []string { f.Commands[0].Class = classWorks; return nil }, `a row without cases must be class local-only or pending-ruling, not "works"`},
		{"a row without cases or a reason", func(f *frontDoorFile) []string { f.Commands[0].Reason = " "; return nil }, "class local-only needs a reason"},
		{"a row with cases and a class", func(f *frontDoorFile) []string { f.Commands[1].Class = classRefuses; return nil }, "a row with cases takes its classes from them"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := base()
			var got []string
			if runnable := tc.mut(f); runnable != nil {
				got = frontDoorCoverageProblems(runnable, f)
			} else {
				got = frontDoorFormProblems(f, ledger, 0)
			}
			if tc.want == "" {
				if len(got) > 0 {
					t.Fatalf("the unbroken file reports problems: %q", got)
				}
				return
			}
			for _, p := range got {
				if strings.Contains(p, tc.want) {
					return
				}
			}
			t.Errorf("the rule did not fire: want a problem containing %q, got %q", tc.want, got)
		})
	}

	// The ratchet's other direction: fewer unrun rows than the constant.
	if got, want := strings.Join(frontDoorFormProblems(base(), ledger, 1), "\n"), "0 pending-ruling rows run no case, fewer than maxUnrunPendingRulings (1)"; !strings.Contains(got, want) {
		t.Errorf("the shrunk unrun ratchet did not fire: want a problem containing %q, got %q", want, got)
	}

	// The schema itself: an unknown field fails the load.
	if _, err := parseConnectFrontDoor([]byte("commands:\n  - command: bd\n    clas: local-only\n")); err == nil {
		t.Errorf("parseConnectFrontDoor accepts an unknown field")
	}
}
