package main

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// The verb x route template-guard table: what ten write verbs do TODAY to an
// is_template issue, on the direct route (TestEmbeddedTemplateGuardAcrossWriteVerbs)
// and the proxied-server route (TestProxiedServerTemplateGuardAcrossWriteVerbs).
//
// The risk it covers is CLI wiring, which no role contract can show: which
// guard, if any, each verb's route runs. Today the role's close guards refuse
// close on both routes; the CLI pre-read (validateIssueUpdatable) refuses
// priority and note on both, and tag directly, where the proxied tag is
// refused by issueops.Lifecycle.Update; defer, undefer, relate and reopen
// reach writes with no template guard on both routes, and duplicate and
// supersede do directly, while the proxied front door refuses them for every
// target (proxy.transform.unsupported). bd update, assign,
// label and set-state are pinned elsewhere (the
// template_label_edits_pass_update_and_assign_refuse subtests of
// TestEmbeddedLabelEditReportsListAndTemplate and TestProxiedServerLabelC)
// and are not repeated here.
//
// The want column records behavior, not a ruling. Until the B8 ruling
// (templates refused on every route, or allowed) every "writes" cell is a pin:
// the native-fix PR that changes one flips exactly that cell and cites its
// decision record. TestTemplateGuardJudgeCatchesWrongOutcomes proves the
// table can fail.

// templateGuardRoute names a front door the table drives.
type templateGuardRoute string

const (
	templateGuardDirect  templateGuardRoute = "direct"  // embedded Dolt, the command's own route
	templateGuardProxied templateGuardRoute = "proxied" // --proxied-server, the unit-of-work route
)

// templateGuardVerdict is what one verb does to a template on one route.
type templateGuardVerdict string

const (
	// templateGuardWrites: exit 0, and the verb's own write lands on the
	// template (the route has no template guard for the verb).
	templateGuardWrites templateGuardVerdict = "writes"
	// templateGuardRefuses: exit 1, the TemplateReadOnlyError sentence, and
	// the template exactly as it was.
	templateGuardRefuses templateGuardVerdict = "refuses"
	// templateGuardUnsupported: exit 1 with the proxied front door's
	// capability-gate refusal (proxy.transform.unsupported), which every
	// target gets; the template exactly as it was.
	templateGuardUnsupported templateGuardVerdict = "unsupported"
)

// templateGuardVerb is one row of the table.
type templateGuardVerb struct {
	name string
	// prep puts the target in the state the verb acts on (deferred for
	// undefer, closed for reopen) BEFORE it is made a template, so no row
	// depends on another row's template behavior. "{target}" is replaced.
	prep []string
	// args is the verb's command line; peer is an ordinary open issue.
	args func(target, peer string) []string
	// landed reports whether the verb's own write is visible on the target.
	landed func(after templateGuardShown, peer string) bool
	// unsupported is the capability gate's message, for a templateGuardUnsupported cell.
	unsupported string
	// want is today's verdict per route (recorded on origin/main 473aa09fa1).
	want map[templateGuardRoute]templateGuardVerdict
}

var templateGuardVerbs = []templateGuardVerb{
	{
		name:   "defer",
		args:   func(target, _ string) []string { return []string{"defer", target} },
		landed: func(after templateGuardShown, _ string) bool { return after.Status == types.StatusDeferred },
		want:   map[templateGuardRoute]templateGuardVerdict{templateGuardDirect: templateGuardWrites, templateGuardProxied: templateGuardWrites},
	},
	{
		name:   "undefer",
		prep:   []string{"defer", "{target}"},
		args:   func(target, _ string) []string { return []string{"undefer", target} },
		landed: func(after templateGuardShown, _ string) bool { return after.Status == types.StatusOpen },
		want:   map[templateGuardRoute]templateGuardVerdict{templateGuardDirect: templateGuardWrites, templateGuardProxied: templateGuardWrites},
	},
	{
		name:   "priority",
		args:   func(target, _ string) []string { return []string{"priority", target, "0"} },
		landed: func(after templateGuardShown, _ string) bool { return after.Priority == 0 },
		want:   map[templateGuardRoute]templateGuardVerdict{templateGuardDirect: templateGuardRefuses, templateGuardProxied: templateGuardRefuses},
	},
	{
		name:   "tag",
		args:   func(target, _ string) []string { return []string{"tag", target, "tpl-guard"} },
		landed: func(after templateGuardShown, _ string) bool { return slices.Contains(after.Labels, "tpl-guard") },
		want:   map[templateGuardRoute]templateGuardVerdict{templateGuardDirect: templateGuardRefuses, templateGuardProxied: templateGuardRefuses},
	},
	{
		name:   "note",
		args:   func(target, _ string) []string { return []string{"note", target, "tpl guard note"} },
		landed: func(after templateGuardShown, _ string) bool { return strings.Contains(after.Notes, "tpl guard note") },
		want:   map[templateGuardRoute]templateGuardVerdict{templateGuardDirect: templateGuardRefuses, templateGuardProxied: templateGuardRefuses},
	},
	{
		name: "duplicate",
		args: func(target, peer string) []string { return []string{"duplicate", target, "--of", peer} },
		landed: func(after templateGuardShown, peer string) bool {
			return after.Status == types.StatusClosed && after.hasDep(peer, types.DepDuplicates)
		},
		unsupported: "duplicate --of is not supported in proxied-server mode",
		want:        map[templateGuardRoute]templateGuardVerdict{templateGuardDirect: templateGuardWrites, templateGuardProxied: templateGuardUnsupported},
	},
	{
		name: "supersede",
		args: func(target, peer string) []string { return []string{"supersede", target, "--with", peer} },
		landed: func(after templateGuardShown, peer string) bool {
			return after.Status == types.StatusClosed && after.hasDep(peer, types.DepSupersedes)
		},
		unsupported: "supersede --with is not supported in proxied-server mode",
		want:        map[templateGuardRoute]templateGuardVerdict{templateGuardDirect: templateGuardWrites, templateGuardProxied: templateGuardUnsupported},
	},
	{
		name:   "relate",
		args:   func(target, peer string) []string { return []string{"dep", "relate", target, peer} },
		landed: func(after templateGuardShown, peer string) bool { return after.hasDep(peer, types.DepRelatesTo) },
		want:   map[templateGuardRoute]templateGuardVerdict{templateGuardDirect: templateGuardWrites, templateGuardProxied: templateGuardWrites},
	},
	{
		name:   "close",
		args:   func(target, _ string) []string { return []string{"close", target} },
		landed: func(after templateGuardShown, _ string) bool { return after.Status == types.StatusClosed },
		want:   map[templateGuardRoute]templateGuardVerdict{templateGuardDirect: templateGuardRefuses, templateGuardProxied: templateGuardRefuses},
	},
	{
		name:   "reopen",
		prep:   []string{"close", "{target}"},
		args:   func(target, _ string) []string { return []string{"reopen", target} },
		landed: func(after templateGuardShown, _ string) bool { return after.Status == types.StatusOpen },
		want:   map[templateGuardRoute]templateGuardVerdict{templateGuardDirect: templateGuardWrites, templateGuardProxied: templateGuardWrites},
	},
}

// templateGuardShown is the part of `bd show <id> --json` the table reads.
type templateGuardShown struct {
	Status       types.Status `json:"status"`
	Priority     int          `json:"priority"`
	Labels       []string     `json:"labels"`
	Notes        string       `json:"notes"`
	IsTemplate   bool         `json:"is_template"`
	Dependencies []struct {
		ID             string               `json:"id"`
		DependencyType types.DependencyType `json:"dependency_type"`
	} `json:"dependencies"`
}

func (s templateGuardShown) hasDep(peer string, depType types.DependencyType) bool {
	for _, dep := range s.Dependencies {
		if dep.ID == peer && dep.DependencyType == depType {
			return true
		}
	}
	return false
}

// state is everything a write verb could change, normalized for comparison.
func (s templateGuardShown) state() string {
	labels := slices.Clone(s.Labels)
	slices.Sort(labels)
	deps := make([]string, 0, len(s.Dependencies))
	for _, dep := range s.Dependencies {
		deps = append(deps, string(dep.DependencyType)+":"+dep.ID)
	}
	slices.Sort(deps)
	return fmt.Sprintf("status=%s priority=%d labels=%v notes=%q deps=%v template=%v", s.Status, s.Priority, labels, s.Notes, deps, s.IsTemplate)
}

func templateGuardSentence(id string) string {
	return "cannot modify template " + id + ": templates are read-only; use 'bd mol pour' to create a work item"
}

// templateGuardOutcome is what one run of a verb did.
type templateGuardOutcome struct {
	code          int
	output        string // stdout + stderr
	before, after templateGuardShown
}

// judgeTemplateGuard returns "" when outcome matches want for row, else why
// not. It is the table's only judgment, kept pure so
// TestTemplateGuardJudgeCatchesWrongOutcomes can prove it fails.
func judgeTemplateGuard(row templateGuardVerb, want templateGuardVerdict, target, peer string, o templateGuardOutcome) string {
	var problems []string
	if !o.after.IsTemplate {
		problems = append(problems, "is_template was cleared")
	}
	switch want {
	case templateGuardWrites:
		if o.code != 0 {
			problems = append(problems, fmt.Sprintf("exit %d, want 0", o.code))
		}
		if !row.landed(o.after, peer) {
			problems = append(problems, "the write did not land")
		}
	case templateGuardRefuses:
		if o.code != 1 {
			problems = append(problems, fmt.Sprintf("exit %d, want 1", o.code))
		}
		if !strings.Contains(o.output, templateGuardSentence(target)) {
			problems = append(problems, fmt.Sprintf("output lacks %q", templateGuardSentence(target)))
		}
	case templateGuardUnsupported:
		if o.code != 1 {
			problems = append(problems, fmt.Sprintf("exit %d, want 1", o.code))
		}
		if row.unsupported == "" || !strings.Contains(o.output, row.unsupported) {
			problems = append(problems, fmt.Sprintf("output lacks %q", row.unsupported))
		}
	default:
		problems = append(problems, fmt.Sprintf("unknown verdict %q", want))
	}
	if want != templateGuardWrites && o.before.state() != o.after.state() {
		problems = append(problems, fmt.Sprintf("refused, but the template changed: %s -> %s", o.before.state(), o.after.state()))
	}
	return strings.Join(problems, "; ")
}

// templateGuardHarness is what one route lends the table.
type templateGuardHarness struct {
	route templateGuardRoute
	// run runs bd and returns stdout, stderr and the exit code.
	run func(t *testing.T, args ...string) (stdout, stderr string, code int)
	// create makes an ordinary open task and returns its id.
	create func(t *testing.T, title string) string
	// markTemplates sets is_template on ids behind the CLI's back.
	markTemplates func(t *testing.T, ids []string)
}

func (h templateGuardHarness) show(t *testing.T, id string) templateGuardShown {
	t.Helper()
	stdout, stderr, code := h.run(t, "show", id, "--json")
	start := strings.IndexByte(stdout, '{')
	if code != 0 || start < 0 {
		t.Fatalf("bd show %s --json: exit %d\nstdout:\n%s\nstderr:\n%s", id, code, stdout, stderr)
	}
	var shown templateGuardShown
	if err := json.NewDecoder(strings.NewReader(stdout[start:])).Decode(&shown); err != nil {
		t.Fatalf("bd show %s --json: %v\n%s", id, err, stdout)
	}
	return shown
}

// runTemplateGuardTable drives every row on h's route, each on a fresh target.
func runTemplateGuardTable(t *testing.T, h templateGuardHarness) {
	t.Helper()
	peer := h.create(t, "Template guard peer")
	targets := make(map[string]string, len(templateGuardVerbs))
	ids := make([]string, 0, len(templateGuardVerbs))
	for _, row := range templateGuardVerbs {
		target := h.create(t, "Template guard "+row.name)
		if row.prep != nil {
			prep := make([]string, len(row.prep))
			for i, arg := range row.prep {
				prep[i] = strings.ReplaceAll(arg, "{target}", target)
			}
			if stdout, stderr, code := h.run(t, prep...); code != 0 {
				t.Fatalf("%s: prep bd %s: exit %d\nstdout:\n%s\nstderr:\n%s", row.name, strings.Join(prep, " "), code, stdout, stderr)
			}
		}
		targets[row.name] = target
		ids = append(ids, target)
	}
	h.markTemplates(t, ids)

	for _, row := range templateGuardVerbs {
		t.Run(row.name, func(t *testing.T) {
			want, ok := row.want[h.route]
			if !ok {
				t.Fatalf("row %s records no verdict for route %s", row.name, h.route)
			}
			target := targets[row.name]
			before := h.show(t, target)
			if !before.IsTemplate {
				t.Fatalf("%s is not a template before the verb ran", target)
			}
			args := row.args(target, peer)
			stdout, stderr, code := h.run(t, args...)
			outcome := templateGuardOutcome{code: code, output: stdout + stderr, before: before, after: h.show(t, target)}
			if why := judgeTemplateGuard(row, want, target, peer, outcome); why != "" {
				t.Errorf("bd %s on template %s (%s route), want %s: %s\nstdout:\n%s\nstderr:\n%s",
					strings.Join(args, " "), target, h.route, want, why, stdout, stderr)
			}
		})
	}
}

// TestTemplateGuardJudgeCatchesWrongOutcomes is the table's falsification
// check: every way a verb can drift from its recorded verdict (a guard added,
// a guard lost, a refusal that wrote anyway, a refusal with the wrong exit
// code or sentence) is a judgment failure, and the matching outcome is not.
func TestTemplateGuardJudgeCatchesWrongOutcomes(t *testing.T) {
	row := func(name string) templateGuardVerb {
		for _, r := range templateGuardVerbs {
			if r.name == name {
				return r
			}
		}
		t.Fatalf("no row %q", name)
		return templateGuardVerb{}
	}
	const target, peer = "tg-1", "tg-peer"
	open := templateGuardShown{Status: types.StatusOpen, Priority: 2, IsTemplate: true}
	deferred := open
	deferred.Status = types.StatusDeferred
	reprioritized := open
	reprioritized.Priority = 0
	refusal := "Error: " + templateGuardSentence(target)

	for _, tc := range []struct {
		name    string
		row     string
		want    templateGuardVerdict
		outcome templateGuardOutcome
		fails   bool
	}{
		{name: "defer writes, as recorded", row: "defer", want: templateGuardWrites,
			outcome: templateGuardOutcome{code: 0, before: open, after: deferred}},
		{name: "defer gains a per-id refusal (B8 without C1)", row: "defer", want: templateGuardWrites, fails: true,
			outcome: templateGuardOutcome{code: 0, output: "Error deferring " + target + ": " + templateGuardSentence(target), before: open, after: open}},
		{name: "defer gains a refusal that exits 1 (B8 with C1)", row: "defer", want: templateGuardWrites, fails: true,
			outcome: templateGuardOutcome{code: 1, output: refusal, before: open, after: open}},
		{name: "priority refuses, as recorded", row: "priority", want: templateGuardRefuses,
			outcome: templateGuardOutcome{code: 1, output: refusal, before: open, after: open}},
		{name: "priority loses its guard", row: "priority", want: templateGuardRefuses, fails: true,
			outcome: templateGuardOutcome{code: 0, before: open, after: reprioritized}},
		{name: "priority refuses but wrote", row: "priority", want: templateGuardRefuses, fails: true,
			outcome: templateGuardOutcome{code: 1, output: refusal, before: open, after: reprioritized}},
		{name: "priority refuses with exit 0", row: "priority", want: templateGuardRefuses, fails: true,
			outcome: templateGuardOutcome{code: 0, output: refusal, before: open, after: open}},
		{name: "priority refuses in other words", row: "priority", want: templateGuardRefuses, fails: true,
			outcome: templateGuardOutcome{code: 1, output: "Error: something else", before: open, after: open}},
		{name: "duplicate unsupported, as recorded", row: "duplicate", want: templateGuardUnsupported,
			outcome: templateGuardOutcome{code: 1, output: "Error: duplicate --of is not supported in proxied-server mode", before: open, after: open}},
		{name: "duplicate becomes supported", row: "duplicate", want: templateGuardUnsupported, fails: true,
			outcome: templateGuardOutcome{code: 0, before: open, after: open}},
		{name: "a verb clears is_template", row: "defer", want: templateGuardWrites, fails: true,
			outcome: templateGuardOutcome{code: 0, before: open, after: templateGuardShown{Status: types.StatusDeferred, Priority: 2}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			why := judgeTemplateGuard(row(tc.row), tc.want, target, peer, tc.outcome)
			if tc.fails && why == "" {
				t.Errorf("the judge passed an outcome that should fail the table")
			}
			if !tc.fails && why != "" {
				t.Errorf("the judge failed the recorded outcome: %s", why)
			}
		})
	}
}
