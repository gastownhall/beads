// Package golangci applies the repository's .golangci.yml to nogo analyzers.
//
// .golangci.yml stays the single description of what the linters check: which
// are enabled, their settings, `run.tests`, generated-file handling and the
// path/text exclusion rules. Bazel copies it into this package (see
// BUILD.bazel), and Wrap gives every golangci-lint linter's nogo analyzer the
// semantics golangci-lint gave it, so editing .golangci.yml changes what the
// nogo build reports.
//
// Decoding is strict: a key this package does not implement fails every
// wrapped analyzer instead of being ignored, so the nogo build can never
// silently drop a setting golangci-lint would have honored.
package golangci

import (
	"bytes"
	_ "embed" // .golangci.yml, copied in by BUILD.bazel
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sync"

	"gopkg.in/yaml.v3"
)

//go:embed golangci.yml
var embedded []byte

// Config is the subset of golangci-lint v2's configuration this repository
// uses. Field names follow golangci-lint's keys.
type Config struct {
	Version string     `yaml:"version"`
	Run     RunSection `yaml:"run"`
	Linters Linters    `yaml:"linters"`
	Issues  Issues     `yaml:"issues"`

	rules []compiledRule
}

// RunSection is golangci-lint's `run` section.
type RunSection struct {
	// Timeout bounds a golangci-lint run; nogo has no equivalent.
	Timeout string `yaml:"timeout"`
	// Tests reports whether test files are analyzed (golangci-lint's default:
	// true).
	Tests *bool `yaml:"tests"`
}

// Issues is golangci-lint's `issues` section. Its keys shape golangci-lint's
// report output only; any finding still fails the nogo build.
type Issues struct {
	UniqByLine *bool `yaml:"uniq-by-line"`
}

// Linters is golangci-lint's `linters` section.
type Linters struct {
	Default    string     `yaml:"default"`
	Enable     []string   `yaml:"enable"`
	Settings   Settings   `yaml:"settings"`
	Exclusions Exclusions `yaml:"exclusions"`
}

// Settings holds the settings of the linters this repository enables.
type Settings struct {
	Depguard  DepguardSettings  `yaml:"depguard"`
	Errcheck  ErrcheckSettings  `yaml:"errcheck"`
	Forbidigo ForbidigoSettings `yaml:"forbidigo"`
	Gosec     GosecSettings     `yaml:"gosec"`
	Misspell  MisspellSettings  `yaml:"misspell"`
	Sloglint  SloglintSettings  `yaml:"sloglint"`
	Unconvert UnconvertSettings `yaml:"unconvert"`
	Unparam   UnparamSettings   `yaml:"unparam"`
}

// DepguardSettings is depguard's settings block.
type DepguardSettings struct {
	Rules map[string]DepguardList `yaml:"rules"`
}

// DepguardList is one named depguard rule.
type DepguardList struct {
	ListMode string         `yaml:"list-mode"`
	Files    []string       `yaml:"files"`
	Allow    []string       `yaml:"allow"`
	Deny     []DepguardDeny `yaml:"deny"`
}

// DepguardDeny is one denied package prefix and the reason shown for it.
type DepguardDeny struct {
	Pkg  string `yaml:"pkg"`
	Desc string `yaml:"desc"`
}

// ErrcheckSettings is errcheck's settings block.
type ErrcheckSettings struct {
	DisableDefaultExclusions bool     `yaml:"disable-default-exclusions"`
	CheckTypeAssertions      bool     `yaml:"check-type-assertions"`
	CheckAssignToBlank       bool     `yaml:"check-blank"`
	ExcludeFunctions         []string `yaml:"exclude-functions"`
	Verbose                  bool     `yaml:"verbose"`
}

// ForbidigoSettings is forbidigo's settings block.
type ForbidigoSettings struct {
	Forbid []ForbidigoPattern `yaml:"forbid"`
	// ExcludeGodocExamples defaults to true, as in golangci-lint.
	ExcludeGodocExamples *bool `yaml:"exclude-godoc-examples"`
	AnalyzeTypes         bool  `yaml:"analyze-types"`
}

// ForbidigoPattern is one forbidden identifier pattern.
type ForbidigoPattern struct {
	Pattern string `yaml:"pattern"`
	Pkg     string `yaml:"pkg,omitempty"`
	Msg     string `yaml:"msg,omitempty"`
}

// GosecSettings is gosec's settings block. Only rule selection is supported;
// gosec's per-rule `config` map is not.
type GosecSettings struct {
	Includes   []string `yaml:"includes"`
	Excludes   []string `yaml:"excludes"`
	Severity   string   `yaml:"severity"`
	Confidence string   `yaml:"confidence"`
}

// MisspellSettings is misspell's settings block.
type MisspellSettings struct {
	Mode        string              `yaml:"mode"`
	Locale      string              `yaml:"locale"`
	ExtraWords  []MisspellExtraWord `yaml:"extra-words"`
	IgnoreRules []string            `yaml:"ignore-rules"`
}

// MisspellExtraWord adds a typo and its correction.
type MisspellExtraWord struct {
	Typo       string `yaml:"typo"`
	Correction string `yaml:"correction"`
}

// SloglintSettings is sloglint's settings block. NoMixedArgs defaults to
// true, as in golangci-lint.
type SloglintSettings struct {
	NoMixedArgs    *bool    `yaml:"no-mixed-args"`
	KVOnly         bool     `yaml:"kv-only"`
	AttrOnly       bool     `yaml:"attr-only"`
	NoGlobal       string   `yaml:"no-global"`
	Context        string   `yaml:"context"`
	StaticMsg      bool     `yaml:"static-msg"`
	MsgStyle       string   `yaml:"msg-style"`
	NoRawKeys      bool     `yaml:"no-raw-keys"`
	KeyNamingCase  string   `yaml:"key-naming-case"`
	ForbiddenKeys  []string `yaml:"forbidden-keys"`
	ArgsOnSepLines bool     `yaml:"args-on-sep-lines"`
}

// UnconvertSettings is unconvert's settings block.
type UnconvertSettings struct {
	FastMath bool `yaml:"fast-math"`
	Safe     bool `yaml:"safe"`
}

// UnparamSettings is unparam's settings block.
type UnparamSettings struct {
	CheckExported bool `yaml:"check-exported"`
}

// Exclusions is golangci-lint v2's `linters.exclusions` section.
type Exclusions struct {
	// Generated is "lax" (the default), "strict" or "disable".
	Generated string          `yaml:"generated"`
	Rules     []ExclusionRule `yaml:"rules"`
}

// ExclusionRule drops the findings that match every condition it sets.
type ExclusionRule struct {
	Path       string   `yaml:"path"`
	PathExcept string   `yaml:"path-except"`
	Text       string   `yaml:"text"`
	Linters    []string `yaml:"linters"`
}

// Generated-file modes of linters.exclusions.generated.
const (
	GeneratedLax     = "lax"
	GeneratedStrict  = "strict"
	GeneratedDisable = "disable"
)

// Parse strictly decodes a golangci-lint v2 configuration and validates it.
func Parse(data []byte) (*Config, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var c Config
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("decoding .golangci.yml: %w", err)
	}
	if c.Version != "2" {
		return nil, fmt.Errorf(".golangci.yml version %q: want \"2\"", c.Version)
	}
	if c.Linters.Default != "none" {
		return nil, fmt.Errorf(".golangci.yml linters.default %q: only \"none\" (an explicit enable list) is supported", c.Linters.Default)
	}
	switch c.Linters.Exclusions.Generated {
	case "", GeneratedLax, GeneratedStrict, GeneratedDisable:
	default:
		return nil, fmt.Errorf(".golangci.yml linters.exclusions.generated %q: want lax, strict or disable", c.Linters.Exclusions.Generated)
	}
	for i, r := range c.Linters.Exclusions.Rules {
		cr, err := r.compile()
		if err != nil {
			return nil, fmt.Errorf(".golangci.yml linters.exclusions.rules[%d]: %w", i, err)
		}
		c.rules = append(c.rules, cr)
	}
	return &c, nil
}

// TestsAnalyzed reports run.tests (golangci-lint's default: true).
func (c *Config) TestsAnalyzed() bool {
	return c.Run.Tests == nil || *c.Run.Tests
}

// Enabled reports whether linters.enable lists the linter.
func (c *Config) Enabled(linter string) bool {
	return slices.Contains(c.Linters.Enable, linter)
}

// GeneratedMode returns linters.exclusions.generated, defaulted to lax.
func (c *Config) GeneratedMode() string {
	if c.Linters.Exclusions.Generated == "" {
		return GeneratedLax
	}
	return c.Linters.Exclusions.Generated
}

var (
	loadOnce sync.Once
	loaded   *Config
	loadErr  error
)

// Load returns the repository's .golangci.yml, parsed once.
func Load() (*Config, error) {
	loadOnce.Do(func() {
		loaded, loadErr = Parse(embedded)
	})
	return loaded, loadErr
}

type compiledRule struct {
	path, pathExcept, text *regexp.Regexp
	linters                []string
}

func (r ExclusionRule) compile() (compiledRule, error) {
	var out compiledRule
	var err error
	if r.Path == "" && r.PathExcept == "" && r.Text == "" && len(r.Linters) == 0 {
		return out, errors.New("empty rule")
	}
	compile := func(field, expr string) *regexp.Regexp {
		if expr == "" || err != nil {
			return nil
		}
		re, cerr := regexp.Compile(expr)
		if cerr != nil {
			err = fmt.Errorf("%s %q: %w", field, expr, cerr)
		}
		return re
	}
	out.path = compile("path", r.Path)
	out.pathExcept = compile("path-except", r.PathExcept)
	out.text = compile("text", r.Text)
	out.linters = r.Linters
	return out, err
}

// match is golangci-lint's baseRule.match: every condition the rule sets
// must hold. path is relative to the repository root, with / separators.
func (r compiledRule) match(linter, path, text string) bool {
	if r.text != nil && !r.text.MatchString(text) {
		return false
	}
	if r.path != nil && !r.path.MatchString(path) {
		return false
	}
	if r.pathExcept != nil && r.pathExcept.MatchString(path) {
		return false
	}
	if len(r.linters) != 0 && !slices.Contains(r.linters, linter) {
		return false
	}
	return true
}

// Excluded reports whether a finding of linter at path (repository-relative,
// / separators) with the given text matches any exclusion rule.
func (c *Config) Excluded(linter, path, text string) bool {
	for _, r := range c.rules {
		if r.match(linter, path, text) {
			return true
		}
	}
	return false
}
