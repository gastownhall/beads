package doctor

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/viper"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/remedy"
)

// MacOSLegacyUserConfigCheckName names the check; its suppression slug is
// macos-legacy-user-config.
const MacOSLegacyUserConfigCheckName = "macOS Legacy User Config"

// macOSLegacyKeyListCap bounds how many differing setting names are listed.
const macOSLegacyKeyListCap = 8

// MacOSLegacyUserConfigInputs is everything CheckMacOSLegacyUserConfig needs,
// filled by the caller so the check itself stays pure and testable on any
// host.
type MacOSLegacyUserConfigInputs struct {
	GOOS    string
	Home    string
	HomeErr error
	// ActiveConfigPaths are the user config.yaml paths bd merges, lowest
	// precedence first (config.UserConfigYamlSearchPaths).
	ActiveConfigPaths []string
	// ConfigTarget is where bd config set writes the user config
	// (config.UserConfigYamlPath), "" when unresolvable.
	ConfigTarget string
	// PrimeTarget is the global PRIME.md bd prime reads, "" when
	// unresolvable.
	PrimeTarget string
	// MetricsEnabled is the resolved metrics consent for this run.
	MetricsEnabled bool
	// NodeID is the node_id bd resolves today.
	NodeID string
	// Stat is os.Stat; tests inject errors through it.
	Stat func(string) (fs.FileInfo, error)
}

// macOSLegacyFinding is what the check found for one legacy file.
type macOSLegacyFinding struct {
	short     string // "bd/config.yaml" or "beads/PRIME.md"
	statErr   bool   // the file could not be checked at all
	detail    string
	fixLines  []string
	statusMsg string // the single-finding message
}

// CheckMacOSLegacyUserConfig reports bd files left in
// ~/Library/Application Support, which bd no longer reads on macOS (GH#8113):
// the user config.yaml and the global PRIME.md. It lists, by name only, the
// settings in the old config.yaml that differ from the user config bd reads
// now and prints the commands to move them. It returns false off darwin and
// ios, before touching the filesystem. Warning-class: it never reports an
// error.
func CheckMacOSLegacyUserConfig(in MacOSLegacyUserConfigInputs) (DoctorCheck, bool) {
	if !config.IsMacOSFamily(in.GOOS) {
		return DoctorCheck{}, false
	}
	check := DoctorCheck{Name: MacOSLegacyUserConfigCheckName, Status: StatusOK, Category: CategoryData}

	homeErr := in.HomeErr
	if homeErr == nil && !filepath.IsAbs(in.Home) {
		homeErr = fmt.Errorf("home directory %q is not absolute", in.Home)
	}
	if homeErr != nil {
		check.Message = "Not checked: " + oneLine(homeErr.Error())
		return check, true
	}
	stat := in.Stat
	if stat == nil {
		stat = os.Stat
	}

	legacyConfig := config.LegacyDarwinUserConfigYamlPathFor(in.Home)
	legacyPrime := filepath.Join(in.Home, "Library", "Application Support", "beads", "PRIME.md")

	var findings []macOSLegacyFinding
	if f, ok := checkLegacyConfigYaml(in, stat, legacyConfig); ok {
		findings = append(findings, f)
	}
	var primeComparators []string
	if in.PrimeTarget != "" {
		primeComparators = []string{in.PrimeTarget}
	}
	if f, ok := checkLegacyPrime(in, stat, legacyPrime, primeComparators); ok {
		findings = append(findings, f)
	}

	switch len(findings) {
	case 0:
		check.Message = "No legacy bd files in ~/Library/Application Support"
		return check, true
	case 1:
		check.Status = StatusWarning
		check.Message = findings[0].statusMsg
		check.Detail = findings[0].detail
		check.Fix = strings.Join(findings[0].fixLines, "\n")
		return check, true
	}
	check.Status = StatusWarning
	check.Message = fmt.Sprintf("bd no longer reads %d files in ~/Library/Application Support", len(findings))
	allStatErr := true
	details := make([]string, 0, len(findings))
	var fixLines []string
	for _, f := range findings {
		allStatErr = allStatErr && f.statErr
		details = append(details, f.short+": "+f.detail)
		fixLines = append(fixLines, f.fixLines...)
	}
	if allStatErr {
		check.Message = fmt.Sprintf("Could not check %d files in ~/Library/Application Support", len(findings))
	}
	check.Detail = strings.Join(details, "; ")
	check.Fix = strings.Join(fixLines, "\n")
	return check, true
}

// legacyFileState classifies a legacy file against the files bd reads now.
// ok is false when there is nothing to report: the file is absent, or it is
// one of the comparators (XDG_CONFIG_HOME pointed at Application Support, or
// a symlink to the file bd reads).
func legacyFileState(stat func(string) (fs.FileInfo, error), legacy string, comparators []string) (info fs.FileInfo, statErr error, ok bool) {
	for _, c := range comparators {
		if filepath.Clean(c) == filepath.Clean(legacy) {
			return nil, nil, false
		}
	}
	info, err := stat(legacy)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, false
	}
	if err != nil {
		return nil, err, true
	}
	for _, c := range comparators {
		if ci, cerr := stat(c); cerr == nil && os.SameFile(info, ci) {
			return nil, nil, false
		}
	}
	return info, nil, true
}

func checkLegacyConfigYaml(in MacOSLegacyUserConfigInputs, stat func(string) (fs.FileInfo, error), legacy string) (macOSLegacyFinding, bool) {
	f := macOSLegacyFinding{short: "bd/config.yaml"}
	info, statErr, ok := legacyFileState(stat, legacy, in.ActiveConfigPaths)
	if !ok {
		return f, false
	}
	target := in.ConfigTarget
	if statErr != nil {
		f.statErr = true
		f.statusMsg = "Could not check ~/Library/Application Support/bd/config.yaml"
		f.detail = statErrDetail(statErr)
		f.fixLines = []string{"If you kept settings there, copy them into " + targetWord(in.GOOS, target, "your bd user config")}
		return f, true
	}
	f.statusMsg = "bd no longer reads ~/Library/Application Support/bd/config.yaml"

	var restoreLines []string
	if !info.Mode().IsRegular() {
		// Never open it: a FIFO would block.
		f.detail = "Could not list its settings: not a regular file"
	} else if data, err := config.ReadLegacyDarwinUserConfigYaml(legacy); err != nil {
		f.detail = "Could not list its settings: " + shortErr(err)
	} else if keys, lines, err := diffLegacyConfig(in, stat, data); err != nil {
		f.detail = "Could not list its settings: " + shortErr(err)
	} else {
		restoreLines = lines
		if len(keys) == 0 {
			f.detail = "Every setting there already matches your current user config"
		} else {
			f.detail = "Settings there that differ from your current user config: " + capKeyList(keys)
		}
	}
	f.fixLines = append(f.fixLines, restoreLines...)

	legacyArg, legacyNote := remedy.CommandArg(in.GOOS, legacy, "<path>")
	if target == "" {
		backupArg, backupNote := remedy.CommandArg(in.GOOS, legacy+".bak", "<backup>")
		f.fixLines = append(f.fixLines, "Copy the settings you still want into your bd user config, then retire the old file: mv "+
			legacyArg+" "+backupArg+legacyNote+backupNote)
		return f, true
	}
	if _, err := stat(target); errors.Is(err, fs.ErrNotExist) {
		dirArg, dirNote := remedy.CommandArg(in.GOOS, filepath.Dir(target), "<dir>")
		targetArg, targetNote := remedy.CommandArg(in.GOOS, target, "<target>")
		f.fixLines = append(f.fixLines, "Move it to where bd reads it: mkdir -p "+dirArg+" && mv "+legacyArg+" "+targetArg+
			dirNote+legacyNote+targetNote)
	} else {
		backupArg, backupNote := remedy.CommandArg(in.GOOS, legacy+".bak", "<backup>")
		f.fixLines = append(f.fixLines, "Copy the settings you still want into "+targetWord(in.GOOS, target, "your bd user config")+
			", then retire the old file: mv "+legacyArg+" "+backupArg+legacyNote+backupNote)
	}
	return f, true
}

// diffLegacyConfig returns the sorted names of the settings in the legacy
// config that the active user config does not set to the same value, and the
// restore lines for the consent and node_id settings bd cannot infer.
func diffLegacyConfig(in MacOSLegacyUserConfigInputs, stat func(string) (fs.FileInfo, error), data []byte) ([]string, []string, error) {
	// The shared decoder rejects anything that is not a YAML mapping, the
	// same verdict the metrics carry-over reaches for this file.
	disabledRaw, disabledSet, err := config.YamlValueFromBytes(data, "metrics.disabled")
	if err != nil {
		return nil, nil, err
	}
	nodeRaw, _, _ := config.YamlValueFromBytes(data, "node_id")

	legacy := viper.New()
	legacy.SetConfigType("yaml")
	if err := legacy.ReadConfig(bytes.NewReader(data)); err != nil {
		return nil, nil, err
	}
	active := activeUserConfigView(stat, in.ActiveConfigPaths)

	var differing []string
	for _, k := range legacy.AllKeys() {
		// Compare formatted values: YAML "true" and true, or "5" and 5, are
		// the same setting to bd's string-typed readers.
		if !active.IsSet(k) || fmt.Sprint(legacy.Get(k)) != fmt.Sprint(active.Get(k)) {
			differing = append(differing, k)
		}
	}
	sort.Strings(differing)

	var lines []string
	if disabledSet && in.MetricsEnabled {
		if disabled, ok := config.ParseUserConfigBool(disabledRaw); ok && disabled {
			lines = append(lines, "If you had turned metrics off, run: bd metrics off")
		}
	}
	if node := strings.TrimSpace(nodeRaw); node != "" && in.NodeID == "" {
		arg, note := remedy.CommandArg(in.GOOS, node, "<node_id>")
		lines = append(lines, "Set this machine's node_id again: bd config set node_id "+arg+note)
	}
	return differing, lines, nil
}

// activeUserConfigView loads the user config files bd merges, the way
// config.Initialize does: lowest precedence first, each later file merged
// over it. Files that are missing, not regular, or unreadable are skipped;
// the config-values check reports those.
func activeUserConfigView(stat func(string) (fs.FileInfo, error), paths []string) *viper.Viper {
	active := viper.New()
	active.SetConfigType("yaml")
	loaded := false
	for _, p := range paths {
		info, err := stat(p)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		active.SetConfigFile(p)
		if !loaded {
			if active.ReadInConfig() == nil {
				loaded = true
			}
			continue
		}
		_ = active.MergeInConfig()
	}
	return active
}

func checkLegacyPrime(in MacOSLegacyUserConfigInputs, stat func(string) (fs.FileInfo, error), legacy string, comparators []string) (macOSLegacyFinding, bool) {
	f := macOSLegacyFinding{short: "beads/PRIME.md"}
	_, statErr, ok := legacyFileState(stat, legacy, comparators)
	if !ok {
		return f, false
	}
	target := in.PrimeTarget
	if statErr != nil {
		f.statErr = true
		f.statusMsg = "Could not check ~/Library/Application Support/beads/PRIME.md"
		f.detail = statErrDetail(statErr)
		f.fixLines = []string{"If you customized it, copy what you still want into " + targetWord(in.GOOS, target, "your global PRIME.md")}
		return f, true
	}
	f.statusMsg = "bd no longer reads ~/Library/Application Support/beads/PRIME.md"
	legacyArg, legacyNote := remedy.CommandArg(in.GOOS, legacy, "<path>")
	backupArg, backupNote := remedy.CommandArg(in.GOOS, legacy+".bak", "<backup>")
	if target == "" {
		f.detail = "Where a workspace has no PRIME.md of its own, bd prime uses its built-in template instead"
		f.fixLines = []string{"Merge anything you still want into your global PRIME.md, then retire the old file: mv " +
			legacyArg + " " + backupArg + legacyNote + backupNote}
		return f, true
	}
	if _, err := stat(target); errors.Is(err, fs.ErrNotExist) {
		f.detail = "Where a workspace has no PRIME.md of its own, bd prime uses its built-in template until " + target + " exists"
		dirArg, dirNote := remedy.CommandArg(in.GOOS, filepath.Dir(target), "<dir>")
		targetArg, targetNote := remedy.CommandArg(in.GOOS, target, "<target>")
		f.fixLines = []string{"Move it to where bd reads it: mkdir -p " + dirArg + " && mv " + legacyArg + " " + targetArg +
			dirNote + legacyNote + targetNote}
		return f, true
	}
	f.detail = "Where a workspace has no PRIME.md of its own, bd prime uses " + target + " instead"
	f.fixLines = []string{"Merge anything you still want into " + targetWord(in.GOOS, target, "your global PRIME.md") +
		", then retire the old file: mv " + legacyArg + " " + backupArg + legacyNote + backupNote}
	return f, true
}

// targetWord spells a destination named in advice prose: as CommandArg spells
// it when it is safe to paste, otherwise (or when unresolved) fallback.
func targetWord(goos, target, fallback string) string {
	if target == "" {
		return fallback
	}
	if word, ok := remedy.ShellWord(goos, target); ok {
		return word
	}
	return fallback + " (" + remedy.EscapedValue(target) + ")"
}

func statErrDetail(err error) string {
	return shortErr(err) + "; bd no longer reads this file, so check it yourself if you kept settings there"
}

// shortErr is err on one line, without the path a *fs.PathError repeats.
func shortErr(err error) string {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		err = pathErr.Err
	}
	return oneLine(err.Error())
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func capKeyList(keys []string) string {
	if len(keys) <= macOSLegacyKeyListCap {
		return strings.Join(keys, ", ")
	}
	return strings.Join(keys[:macOSLegacyKeyListCap], ", ") + fmt.Sprintf(", and %d more", len(keys)-macOSLegacyKeyListCap)
}
