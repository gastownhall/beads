package scripts_test

import (
	"fmt"
	"strings"
	"testing"
)

// This file is shared between F7a (ci/f7a-blacksmith-fold) and F7c
// (ci/f7c-advisory-workflows). F7a lands first; F7c should rebase onto it and
// delete its own copies of the pieces below rather than keep a second
// definition (two independent `sameRepoBlacksmith4vcpu` consts merge fine
// textually since the literal is identical, but two
// `TestSameRepoBlacksmithExpressionSemantics` functions in the same package
// do not - that was review SF-5/F7c-collision on the F7a review).
//
// Public surface F7c (or any later slice) should reuse:
//   - sameRepoBlacksmith2vcpu / sameRepoBlacksmith4vcpu / sameRepoBlacksmith8vcpu
//   - evalGHExpr(expr string, ctx map[string]string) (any, error)
//   - mustEvalGHRunsOn(t *testing.T, expr string, ctx map[string]string) string
//
// F7c's own TestSameRepoBlacksmithExpressionSemantics (ci_f7c_advisory_test.go)
// and TestBlacksmithAdvisoryJobsReadNoSecrets should be folded into (or
// replaced by calls into) this file's test and
// TestBlacksmithJobsReadNoSecrets (ci_workflow_test.go) respectively, rather
// than kept as independent copies.

// F3: the same "same-repo PR, or merge_group" Blacksmith expression used by
// pr.yml's and pr-risk.yml's bazel-coverage/ci-gate/detect-ci-tier jobs - a
// package-level const so every test that needs it (TestSameRepoBlacksmithRunners,
// TestPRRiskBazelCoverageJob, ...) reads the one literal.
const sameRepoBlacksmith2vcpu = "${{ (github.event_name == 'merge_group' || (github.event_name == 'pull_request' && github.event.pull_request.head.repo.full_name == github.repository && github.actor != 'dependabot[bot]')) && 'blacksmith-2vcpu-ubuntu-2404' || 'ubuntu-latest' }}"

// F7a: the same same-repo expression at 4 vCPU and 8 vCPU, for jobs sized
// larger than the 2 vCPU default (check-doc-flags, pr-policy-wrapper,
// pr-risk.yml's test-nix at 4 vCPU; check-release-target-cross-compilation at
// 8 vCPU). F7c's advisory workflows also use the 4 vCPU size.
const sameRepoBlacksmith4vcpu = "${{ (github.event_name == 'merge_group' || (github.event_name == 'pull_request' && github.event.pull_request.head.repo.full_name == github.repository && github.actor != 'dependabot[bot]')) && 'blacksmith-4vcpu-ubuntu-2404' || 'ubuntu-latest' }}"
const sameRepoBlacksmith8vcpu = "${{ (github.event_name == 'merge_group' || (github.event_name == 'pull_request' && github.event.pull_request.head.repo.full_name == github.repository && github.actor != 'dependabot[bot]')) && 'blacksmith-8vcpu-ubuntu-2404' || 'ubuntu-latest' }}"

// --- a minimal GitHub Actions expression evaluator -------------------------
//
// Review SF-3 (2026-10-03) on the F7a review: the original
// TestSameRepoBlacksmithExpressionSemantics ran a hand-written Go
// re-implementation of the same-repo Blacksmith formula against its own
// truth table, then string-compared the pinned consts against a template
// built from the same pieces - a tautology that a change to both the
// template and the const (in the same wrong way) would still pass. This
// evaluator actually parses and evaluates the real `${{ ... }}` expression
// string, for the small subset of the GitHub Actions expression language
// this repo's same-repo Blacksmith ternaries use: string literals, dotted
// identifier lookups (resolved from a caller-supplied context map), ==, !=,
// &&, ||, !, and parentheses. It is not a general-purpose GHA expression
// engine - no functions, no numbers, no object/array literals.
//
// && and || use GitHub's own short-circuit-returns-operand semantics (not
// strict booleans: `false && 'x'` is `false`, not `false`'s boolean negation
// of something), which is exactly what lets a `cond && 'labelA' || 'labelB'`
// ternary evaluate to a string label rather than a bool.

type ghToken struct {
	kind string // "ident", "string", "op", "lparen", "rparen", "bang"
	val  string
}

func ghTokenize(s string) ([]ghToken, error) {
	var toks []ghToken
	i := 0
	isIdentByte := func(b byte) bool {
		return b == '.' || b == '_' ||
			(b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
	}
	for i < len(s) {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '(':
			toks = append(toks, ghToken{"lparen", "("})
			i++
		case c == ')':
			toks = append(toks, ghToken{"rparen", ")"})
			i++
		case c == '\'':
			j := i + 1
			for j < len(s) && s[j] != '\'' {
				j++
			}
			if j >= len(s) {
				return nil, fmt.Errorf("unterminated string literal at byte %d in %q", i, s)
			}
			toks = append(toks, ghToken{"string", s[i+1 : j]})
			i = j + 1
		case strings.HasPrefix(s[i:], "=="):
			toks = append(toks, ghToken{"op", "=="})
			i += 2
		case strings.HasPrefix(s[i:], "!="):
			toks = append(toks, ghToken{"op", "!="})
			i += 2
		case strings.HasPrefix(s[i:], "&&"):
			toks = append(toks, ghToken{"op", "&&"})
			i += 2
		case strings.HasPrefix(s[i:], "||"):
			toks = append(toks, ghToken{"op", "||"})
			i += 2
		case c == '!':
			toks = append(toks, ghToken{"bang", "!"})
			i++
		default:
			j := i
			for j < len(s) && isIdentByte(s[j]) {
				j++
			}
			if j == i {
				return nil, fmt.Errorf("unexpected character %q at byte %d in %q", c, i, s)
			}
			toks = append(toks, ghToken{"ident", s[i:j]})
			i = j
		}
	}
	return toks, nil
}

type ghParser struct {
	toks []ghToken
	pos  int
	ctx  map[string]string
}

func (p *ghParser) peek() (ghToken, bool) {
	if p.pos >= len(p.toks) {
		return ghToken{}, false
	}
	return p.toks[p.pos], true
}

func (p *ghParser) next() (ghToken, bool) {
	tok, ok := p.peek()
	if ok {
		p.pos++
	}
	return tok, ok
}

func (p *ghParser) parseExpr() (any, error) { return p.parseOr() }

func (p *ghParser) parseOr() (any, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for {
		tok, ok := p.peek()
		if !ok || tok.kind != "op" || tok.val != "||" {
			return left, nil
		}
		p.next()
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		if !ghTruthy(left) {
			left = right
		}
	}
}

func (p *ghParser) parseAnd() (any, error) {
	left, err := p.parseEquality()
	if err != nil {
		return nil, err
	}
	for {
		tok, ok := p.peek()
		if !ok || tok.kind != "op" || tok.val != "&&" {
			return left, nil
		}
		p.next()
		right, err := p.parseEquality()
		if err != nil {
			return nil, err
		}
		if ghTruthy(left) {
			left = right
		}
	}
}

func (p *ghParser) parseEquality() (any, error) {
	left, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	for {
		tok, ok := p.peek()
		if !ok || tok.kind != "op" || (tok.val != "==" && tok.val != "!=") {
			return left, nil
		}
		p.next()
		right, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		eq := ghEquals(left, right)
		if tok.val == "!=" {
			left = !eq
		} else {
			left = eq
		}
	}
}

func (p *ghParser) parseUnary() (any, error) {
	if tok, ok := p.peek(); ok && tok.kind == "bang" {
		p.next()
		v, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return !ghTruthy(v), nil
	}
	return p.parsePrimary()
}

func (p *ghParser) parsePrimary() (any, error) {
	tok, ok := p.next()
	if !ok {
		return nil, fmt.Errorf("unexpected end of expression")
	}
	switch tok.kind {
	case "lparen":
		v, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		closing, ok := p.next()
		if !ok || closing.kind != "rparen" {
			return nil, fmt.Errorf("expected ) in expression")
		}
		return v, nil
	case "string":
		return tok.val, nil
	case "ident":
		v, ok := p.ctx[tok.val]
		if !ok {
			// An identifier the caller's context does not mention (for
			// example a deleted fork's head.repo.full_name) is null in the
			// real evaluator; treat it as the empty string, which compares
			// unequal to every non-empty literal this repo's expressions use.
			return "", nil
		}
		return v, nil
	default:
		return nil, fmt.Errorf("unexpected token %+v", tok)
	}
}

func ghTruthy(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return x != ""
	case nil:
		return false
	default:
		return true
	}
}

func ghEquals(a, b any) bool {
	return fmt.Sprint(a) == fmt.Sprint(b)
}

// evalGHExpr evaluates a GitHub Actions `${{ ... }}` expression (the wrapper
// is optional) against ctx, a map from dotted identifier (e.g.
// "github.event_name") to its string value. See the package comment above
// for the supported subset.
func evalGHExpr(expr string, ctx map[string]string) (any, error) {
	expr = strings.TrimSpace(expr)
	expr = strings.TrimPrefix(expr, "${{")
	expr = strings.TrimSuffix(expr, "}}")
	expr = strings.TrimSpace(expr)
	toks, err := ghTokenize(expr)
	if err != nil {
		return nil, err
	}
	p := &ghParser{toks: toks, ctx: ctx}
	v, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	if p.pos != len(p.toks) {
		return nil, fmt.Errorf("trailing tokens after expression %q: %v", expr, p.toks[p.pos:])
	}
	return v, nil
}

// mustEvalGHRunsOn evaluates a `runs-on: ${{ ... }}`-style expression and
// returns the resulting runner-label string, failing the test if the
// expression does not evaluate to a string.
func mustEvalGHRunsOn(t *testing.T, expr string, ctx map[string]string) string {
	t.Helper()
	v, err := evalGHExpr(expr, ctx)
	if err != nil {
		t.Fatalf("evalGHExpr(%q): %v", expr, err)
	}
	s, ok := v.(string)
	if !ok {
		t.Fatalf("evalGHExpr(%q) = %#v (%T), want string", expr, v, v)
	}
	return s
}

// TestSameRepoBlacksmithExpressionSemantics runs the real, pinned
// sameRepoBlacksmith{2,4,8}vcpu expression strings through evalGHExpr (not a
// hand-written mirror of their logic) against every event shape the policy
// cares about: trusted same-repo PRs and merge_group get Blacksmith; forks,
// Dependabot, a deleted fork head, and any other event fall back to
// ubuntu-latest. Because this evaluates the actual expression text, a
// semantic typo in any of the three consts (a dropped `!`, a swapped `&&`/
// `||`, a wrong field name) fails this test even if a hand-rolled Go mirror
// would have been edited to match it.
func TestSameRepoBlacksmithExpressionSemantics(t *testing.T) {
	const ownRepo = "steveyegge/beads"
	type tc struct {
		name       string
		event      string
		headRepo   string // github.event.pull_request.head.repo.full_name; "" = fork or deleted fork head
		actor      string
		wantRunner bool
	}
	cases := []tc{
		{"same-repo PR, human actor", "pull_request", ownRepo, "alice", true},
		{"merge_group always Blacksmith", "merge_group", "", "", true},
		{"fork PR stays ubuntu-latest", "pull_request", "someone-else/beads", "alice", false},
		{"deleted fork head stays ubuntu-latest", "pull_request", "", "alice", false},
		{"same-repo PR, dependabot actor stays ubuntu-latest", "pull_request", ownRepo, "dependabot[bot]", false},
		{"push stays ubuntu-latest", "push", "", "alice", false},
		{"pull_request_target stays ubuntu-latest", "pull_request_target", ownRepo, "alice", false},
		{"schedule stays ubuntu-latest", "schedule", "", "", false},
		{"workflow_dispatch stays ubuntu-latest", "workflow_dispatch", "", "", false},
	}
	consts := map[string]string{
		"blacksmith-2vcpu-ubuntu-2404": sameRepoBlacksmith2vcpu,
		"blacksmith-4vcpu-ubuntu-2404": sameRepoBlacksmith4vcpu,
		"blacksmith-8vcpu-ubuntu-2404": sameRepoBlacksmith8vcpu,
	}
	for _, c := range cases {
		ctx := map[string]string{
			"github.event_name":                             c.event,
			"github.event.pull_request.head.repo.full_name": c.headRepo,
			"github.repository":                             ownRepo,
			"github.actor":                                  c.actor,
		}
		for label, expr := range consts {
			t.Run(c.name+"/"+label, func(t *testing.T) {
				got := mustEvalGHRunsOn(t, expr, ctx)
				want := "ubuntu-latest"
				if c.wantRunner {
					want = label
				}
				if got != want {
					t.Errorf("%s: real evaluator on %q = %q, want %q", c.name, expr, got, want)
				}
			})
		}
	}
}
