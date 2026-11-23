package remedy_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The census is the source-keyed half of the remedy-printing invariant: it
// parses every production Go file under cmd/bd and internal/ and fails when a
// value read from a clone-controlled source reaches a print call without the
// remedy printer. The behavioral half is the entry-point tests in each
// package: in cmd/bd TestRemedyRefusalsNameValuesEscaped,
// TestRemedyPrintersNameValuesEscaped, TestRemedyPrintersKeepBdsOwnLines,
// TestRemedyValuesEscapeLineBreaks, TestErrorPrintersSpellMessages,
// TestPasteReadyCommandsQuoteValues and the TestEmbeddedRemedyPrinting_*
// tests; TestDoctorChecksNameConfiguredValuesEscaped (cmd/bd/doctor);
// TestInspectServerMetadataDatabasesNamesTheDataDirectoryEscaped
// (cmd/bd/doctor/fix); TestNewServerModeNamesConfiguredEndpointEscaped and
// TestNewServerModeKeepsBdsOwnLines (internal/storage/dolt);
// TestEnsureRunningNamesExternalHostEscaped,
// TestUnresolvedErrorNamesTheHostEscaped and
// TestCorruptJournalRecoveryHintQuotesPaths (internal/doltserver);
// TestCheckSecretGitTracked_QuotesThePathInTheRemedy (internal/config);
// TestWorkspaceMessagesNameBeadsDirEscaped and
// TestInsecureCredentialsWarningQuotesPath (internal/configfile);
// TestOpenReadOnlyNamesMissingDatabaseEscaped (internal/storage/embeddeddolt);
// and TestAssigneeNotStolenPrintsHolderSafely (internal/validation).
//
// It is keyed on where a value comes from, not on what a variable is called:
//
//   - censusSourceCalls: calls that return a clone-controlled value (the
//     committed metadata.json's Dolt endpoint, user and database, a committed
//     config.yaml's sync.remote or routing repos, the routed repo a routing
//     rule picks);
//   - censusSourceFields: struct fields that hold one;
//   - censusSourceKeys: a Get*, get* or resolve* call passed one of these
//     config keys as a string literal, such as config.GetString("sync.remote").
//
// A value derived from one is tracked through its package: assigned from it,
// joined into it, put in a struct literal, passed to any call (whose result,
// such as routing.ExpandPath's or os.Stat's error, can repeat it), passed as
// an argument to a function of the package (whose parameter then carries it),
// returned by a function of the package (whose calls then carry it), or
// passed as a function value (a source passed as resolve, whose calls then
// carry it).
//
// Each tracked value is of one of two kinds. A value (a host, a path) is
// printed safely only through a remedy.* call, which ends the taint, or with
// %q, which escapes every character a terminal acts on. An error that can
// repeat one (the result of a call it was passed to, or of a function of the
// package declared to return an error) is printed safely the same ways, or
// through bd's error printers (HandleError and its family, cobra's error
// line), which spell every message by remedy.PrintedText; wrapping it into
// another error (fmt.Errorf with %w) passes the taint on to that error.
//
// The census does not follow a value through a field of a struct it was
// stored in (other than censusSourceFields), a map, a channel, or another
// package's return value other than a source's: a site like that is pinned by
// its package's entry-point test. Nor is a filesystem path a source here: bd
// prints the workspace's own path everywhere, and only a routed planning or
// target path is clone-controlled. cmd/bd's
// TestRemedyRefusalsDoNotInterpolateRawPaths covers the functions that print
// one. Nor is a database config row (issue_prefix) a source:
// warnHalfIdentifiedSubstrate, which prints one beside its remedy, is pinned
// by TestRemedyValuesEscapeLineBreaks. An issue ID read from the store is a
// source of its own, tracked by TestRemedyCensusStoreIDHints (storeIDSources):
// a field named ID or ending ID (ResolvedID), or the string a partial-ID
// resolver or molecule lookup returns (storeIDSourceCalls: ResolvePartialID,
// ResolvePartialIDs, ResolvePartialIDExact, resolveIDWithRouting,
// MoleculeRoot, MoleculeRoots, ActiveMoleculeIDs, InProgressMoleculeIDs).
// A bd command a hint pastes one into — bd duplicates, bd mol current, bd mol
// stale, bd close --continue, bd find-duplicates, bd mol
// ready --gated, the unserved auto-close note, the show/dep-list hints and bd
// dep add's malformed-target refusal — routes it through remedy.CommandArg
// (a remedy printer that leaves a $(...) as it is, such as PrintedValue, does
// not count, called in place or stored in a variable first, and neither does
// %q, whose double quotes leave a $(...) live), with storeIDHintAllowlist
// excusing the sites whose ID bd mints or the user typed. The same bd-command
// rule holds for a config, metadata or routing value (configSources), tracked
// by TestRemedyCensusConfigHints with configHintAllowlist. Those passes read a
// hint built by a literal format (%s, %v, %q), a + chain or fmt.Println's
// operands; they do not see one a format with numbered arguments (%[1]s),
// strings.Join or a strings.Builder assembles. The census does not see the lines printed beside such a
// command that name the ID: those are spelled by printedIssueID /
// remedy.PrintedWord and pinned behaviorally by
// TestStoreReadIDHintsQuoteIDs, TestStoreReadCommandHintsQuoteIDs,
// TestUnservedMoleculeAutoCloseNoteNamesIDsEscaped,
// TestStoreReadHintsNameTitlesEscaped,
// TestEmbeddedRemedyPrinting_closeContinue and TestProxiedServerClose2B, and
// bd close's partial-ID notice by internal/utils'
// TestEmitPartialResolutionNoticeSpellsResolvedID. One such line still names
// IDs as the store holds them: when a completed molecule's root refuses its
// auto-close, warnMoleculeAutoCloseRefusal prints the refusal's sentence
// above bd close --continue's hint, spelled by remedy.PrintedValue because
// the close result carries only the sentence, so a $(...) in the root or
// blocker IDs it names still runs when the output is pasted;
// TestStoreReadHintsNameTitlesEscaped pins only that it cannot redraw the
// hint. A title, or bd find-duplicates' AI reason, printed beside such a
// command is prose, spelled by remedy.PrintedValue as everywhere bd prints
// one, so a $(...) in it is printed as it is. A remedy value wrapped in
// literal backticks — a quoted word (shellWordSources) or a remedy printer
// called in place that can return a backtick — is tracked by
// TestRemedyCensusBacktickedRemedies; a value that crosses a struct field
// into another package, such as a doctor check's Fix shown by bd doctor
// --agent, is not, and is pinned by cmd/bd's
// TestAgentDiagnosticCommandsKeepBackticksClosed. Nor is a
// .beads/recipes.toml recipe:
// TestRunRecipeNamesRecipesTomlValuesEscaped pins that no key, name,
// description, type or path bd setup prints, nor an OS error that repeats a
// path, can redraw or split its Run: line, that the command on that line
// takes the key as one shell word, and that its special-handling refusal
// names the key and type so that no part of either runs when pasted. A
// $(...) in a name, description or path printed beside that command is
// printed as it is, as a title is. bd doctor's text printers of checks
// spell each check they print (doctor.DoctorCheck.Printed and cmd/bd's
// doctorCheck.printed), so a check's fields are not print calls here;
// TestRemedyPrintersNameValuesEscaped's doctor rows pin several of them.
var (
	censusSourceCalls = map[string]bool{
		"ServerAddr":                  true,
		"serverLocation":              true, // cmd/bd's name for the server a bootstrap or init probe dials
		"GetDoltServerHost":           true,
		"GetDoltServerSocket":         true,
		"GetDoltServerUser":           true,
		"GetDoltDatabase":             true,
		"resolveSyncRemote":           true,
		"resolveSyncRemoteFromDir":    true,
		"getRoutingConfigValue":       true,
		"resolveRoutingConfigValue":   true,
		"gitRemoteURLForLsRemote":     true,
		"DetermineTargetRepo":         true,
		"DetermineTargetRepoWithRule": true,
		"determineAutoRoutedRepoPath": true,
	}
	censusSourceFields = map[string]bool{
		"ServerHost":       true,
		"ServerSocket":     true,
		"SyncRemote":       true,
		"BlockedRemote":    true,
		"ContributorRepo":  true,
		"MaintainerRepo":   true,
		"DoltServerHost":   true,
		"DoltServerSocket": true,
		"DoltServerUser":   true,
		"DoltDatabase":     true,
	}
	censusSourceKeys = map[string]bool{
		"sync.remote":         true,
		"routing.contributor": true,
		"routing.maintainer":  true,
		"routing.default":     true,
		"dolt.host":           true,
	}
	// censusPrintCalls are the calls whose string arguments reach the
	// terminal: the fmt print family, error constructors, and a builder's
	// WriteString.
	censusPrintCalls = map[string]bool{
		"fmt.Errorf": true, "fmt.Sprintf": true, "fmt.Printf": true, "fmt.Fprintf": true,
		"fmt.Println": true, "fmt.Fprintln": true, "fmt.Print": true, "fmt.Fprint": true,
		"fmt.Sprint": true, "fmt.Sprintln": true, "errors.New": true, "WriteString": true,
	}
	// censusTerminalCalls are the print calls that write to the terminal
	// directly: an error printed there must be spelled by the printer.
	censusTerminalCalls = map[string]bool{
		"fmt.Printf": true, "fmt.Fprintf": true, "fmt.Println": true, "fmt.Fprintln": true,
		"fmt.Print": true, "fmt.Fprint": true,
	}
	// censusErrorPrinters are cmd/bd's error printers, which spell the whole
	// message by remedy.PrintedText: an error is printed safely there, a
	// value still needs the printer, which also escapes its line breaks.
	censusErrorPrinters = map[string]bool{
		"HandleError": true, "HandleErrorRespectJSON": true, "HandleErrorWithHint": true,
		"HandleErrorWithHintRespectJSON": true, "WarnError": true,
	}
	// censusPropagatingPkgs are the packages whose functions return a value
	// derived from their string arguments.
	censusPropagatingPkgs = map[string]bool{"filepath": true, "path": true, "strings": true, "ui": true}
	// censusValueFreeCalls compute a value that cannot repeat their
	// argument's text.
	censusValueFreeCalls = map[string]bool{
		"len": true, "cap": true, "errors.Is": true, "errors.As": true,
		// Encoding a value to JSON writes it escaped; its error names a type.
		"json.Marshal": true, "json.MarshalIndent": true, "Encode": true, "outputJSON": true,
	}
	// censusNotPrinted are functions whose print calls build a value that is
	// never printed, with the reason.
	censusNotPrinted = map[string]string{
		"internal/configfile/credentials.go CredentialsSectionKeys": "builds the credentials-file section names a password is looked up under",
	}
	// censusPropagatingCalls return a value derived from their string
	// arguments, as censusPropagatingPkgs' functions do.
	censusPropagatingCalls = map[string]bool{"routing.ExpandPath": true, "redactRemoteURL": true, "scrubURLCredentials": true}
)

// taintKind is what a tracked expression carries: a clone-controlled value,
// an error that can repeat one, or the result of a call passed one.
type taintKind uint8

const (
	taintNone taintKind = iota
	// taintCallResult is the result of another package's call passed a
	// tracked value, which carries it only as that call's error: os.Stat's
	// error names the path, net.Dial's the host.
	taintCallResult
	taintErr
	taintValue
)

func maxKind(a, b taintKind) taintKind {
	if a > b {
		return a
	}
	return b
}

// taintInfo is what an expression carries, and, for a struct, which of its
// fields carry a clone-controlled value.
type taintInfo struct {
	kind   taintKind
	fields map[string]bool
}

func (t taintInfo) merge(o taintInfo) taintInfo {
	t.kind = maxKind(t.kind, o.kind)
	if len(o.fields) > 0 {
		merged := map[string]bool{}
		for f := range t.fields {
			merged[f] = true
		}
		for f := range o.fields {
			merged[f] = true
		}
		t.fields = merged
	}
	return t
}

// censusSources is what one census pass treats as a source of a
// clone-controlled value. configSources is #7837's set (config, metadata and
// routing values); storeIDSources treats an issue ID read from the store as
// one; and shellWordSources treats the shell word a remedy quoting call
// returns as one, to find a remedy wrapped in literal backticks.
type censusSources struct {
	calls, fields, keys map[string]bool
	// storeIDs treats a field named ID, or ending ID after a lower-case
	// letter or digit (ResolvedID, fullID), as a store-read issue ID; the
	// pass's calls (storeIDSourceCalls) return one.
	storeIDs bool
	// shellWords treats remedy.CommandArg's, ShellWord's and ShellQuotedFor's
	// word (their first result) as the value, so a census can find a remedy
	// word a format or + chain wraps in literal backticks.
	shellWords bool
}

// storeIDSourceCalls are the calls whose string results are issue IDs read
// from the store: the partial-ID resolvers, which turn a typed prefix into
// the stored ID it matches, and the molecule lookups that return a root or
// molecule ID.
var storeIDSourceCalls = map[string]bool{
	"ResolvePartialID":      true, // internal/utils
	"ResolvePartialIDs":     true, // internal/utils
	"ResolvePartialIDExact": true, // internal/utils
	"resolveIDWithRouting":  true, // cmd/bd dep.go
	"MoleculeRoot":          true, // issueops
	"MoleculeRoots":         true, // issueops
	"ActiveMoleculeIDs":     true, // issueops
	"InProgressMoleculeIDs": true, // issueops
}

var (
	configSources    = censusSources{calls: censusSourceCalls, fields: censusSourceFields, keys: censusSourceKeys}
	storeIDSources   = censusSources{storeIDs: true, calls: storeIDSourceCalls}
	shellWordSources = censusSources{shellWords: true}
)

func (p *censusPkg) isSourceField(name string) bool {
	if p.src.fields[name] {
		return true
	}
	return p.src.storeIDs && isIDField(name)
}

// isIDField reports whether a field so named holds an ID, as Go names them:
// ID itself, or a name ending ID after a lower-case letter or digit
// (ResolvedID, MoleculeID, fullID), not an acronym such as PID or UUID.
func isIDField(name string) bool {
	if name == "ID" {
		return true
	}
	base, ok := strings.CutSuffix(name, "ID")
	if !ok || base == "" {
		return false
	}
	c := base[len(base)-1]
	return 'a' <= c && c <= 'z' || '0' <= c && c <= '9'
}

// isShellWordCall reports whether call is remedy.CommandArg, ShellWord or
// ShellQuotedFor, whose first result is a value quoted for a shell.
func isShellWordCall(call *ast.CallExpr) bool {
	switch censusCallName(call) {
	case "remedy.CommandArg", "remedy.ShellWord", "remedy.ShellQuotedFor":
		return true
	}
	return false
}

// censusEnv is what the variables of one function carry, keyed by varKey.
type censusEnv map[any]taintInfo

type censusPkg struct {
	src   censusSources
	fset  *token.FileSet
	files map[string]*ast.File
	// funcs maps a function or method name to its declarations.
	funcs map[string][]*ast.FuncDecl
	// taintedParams holds, per declaration, what each parameter a caller
	// passes a tracked value in carries.
	taintedParams map[*ast.FuncDecl]map[string]taintInfo
	// closures are the function literals of package-level declarations,
	// such as a cobra command's RunE, each as a declaration of its own.
	closures []*ast.FuncDecl
	// declFiles is the file of each declaration, closures included.
	declFiles map[*ast.FuncDecl]string
	// returns memoizes callReturns.
	returns map[string]map[int]taintInfo
}

func censusCallName(call *ast.CallExpr) string {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return fn.Name
	case *ast.SelectorExpr:
		if pkg, ok := fn.X.(*ast.Ident); ok {
			return pkg.Name + "." + fn.Sel.Name
		}
		return fn.Sel.Name
	}
	return ""
}

func censusSelName(call *ast.CallExpr) string {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return fn.Name
	case *ast.SelectorExpr:
		return fn.Sel.Name
	}
	return ""
}

// isRemedyCall reports whether call spells its arguments through the remedy
// printer: a remedy.* call, or doltutil.PrintedServerAddr, which wraps one.
func isRemedyCall(call *ast.CallExpr) bool {
	return strings.HasPrefix(censusCallName(call), "remedy.") || censusSelName(call) == "PrintedServerAddr"
}

// isErrorName reports whether a variable so named holds an error, as bd
// names them (err, probeErr, dialErr).
func isErrorName(name string) bool {
	return name == "err" || strings.HasSuffix(name, "Err") || strings.HasSuffix(name, "Error")
}

// returnsError reports whether the i-th result of fn is declared an error.
func returnsError(fn *ast.FuncDecl, i int) bool {
	if fn.Type.Results == nil {
		return false
	}
	n := 0
	for _, field := range fn.Type.Results.List {
		count := max(len(field.Names), 1)
		if i < n+count {
			id, ok := field.Type.(*ast.Ident)
			return ok && id.Name == "error"
		}
		n += count
	}
	return false
}

// varKey is the key a variable is tracked by: the object the parser resolved
// the identifier to, so an err declared in one block is not the err of
// another, or its name when it resolved to none.
func varKey(id *ast.Ident) any {
	if id.Obj != nil {
		return id.Obj
	}
	return id.Name
}

func paramNames(fn *ast.FuncDecl) []string {
	var names []string
	for _, field := range fn.Type.Params.List {
		for _, name := range field.Names {
			names = append(names, name.Name)
		}
		if len(field.Names) == 0 {
			names = append(names, "_")
		}
	}
	return names
}

// callees returns the declarations of the package a call can run: a
// function called by name, or a method called on a local variable. A call
// on a package name (one the parser resolved to no local object) runs
// another package's function.
func (p *censusPkg) callees(call *ast.CallExpr) []*ast.FuncDecl {
	var want func(*ast.FuncDecl) bool
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		want = func(d *ast.FuncDecl) bool { return d.Recv == nil }
	case *ast.SelectorExpr:
		if x, ok := fn.X.(*ast.Ident); ok && x.Obj == nil {
			return nil
		}
		want = func(d *ast.FuncDecl) bool { return d.Recv != nil }
	default:
		return nil
	}
	var decls []*ast.FuncDecl
	for _, d := range p.funcs[censusSelName(call)] {
		if want(d) {
			decls = append(decls, d)
		}
	}
	return decls
}

// argSeeds is what each parameter of callee carries from call's arguments.
func (p *censusPkg) argSeeds(call *ast.CallExpr, callee *ast.FuncDecl, env censusEnv) map[string]taintInfo {
	params := paramNames(callee)
	var seeds map[string]taintInfo
	for i, arg := range call.Args {
		if i >= len(params) || params[i] == "_" {
			continue
		}
		info := p.exprInfo(arg, env)
		if info.kind == taintCallResult {
			// Another call's result passed straight on is its error at most.
			info.kind = taintErr
		}
		if info.kind == taintNone && len(info.fields) == 0 {
			continue
		}
		if seeds == nil {
			seeds = map[string]taintInfo{}
		}
		seeds[params[i]] = info
	}
	return seeds
}

// callResults is what each result of a call of the package carries, given
// its arguments, or nil when it runs no function of the package.
func (p *censusPkg) callResults(call *ast.CallExpr, env censusEnv) map[int]taintInfo {
	decls := p.callees(call)
	if len(decls) == 0 {
		return nil
	}
	results := map[int]taintInfo{}
	for _, callee := range decls {
		for i, info := range p.callReturns(callee, p.argSeeds(call, callee, env)) {
			results[i] = results[i].merge(info)
		}
	}
	return results
}

// callReturns is what each result of fn carries when its parameters carry
// seeds. It is memoized; a call of fn from inside its own evaluation carries
// nothing.
func (p *censusPkg) callReturns(fn *ast.FuncDecl, seeds map[string]taintInfo) map[int]taintInfo {
	keys := make([]string, 0, len(seeds))
	for name, info := range seeds {
		fields := make([]string, 0, len(info.fields))
		for f := range info.fields {
			fields = append(fields, f)
		}
		slices.Sort(fields)
		keys = append(keys, name+"="+strconv.Itoa(int(info.kind))+"."+strings.Join(fields, "."))
	}
	slices.Sort(keys)
	memo := fmt.Sprintf("%p %s", fn, strings.Join(keys, ","))
	if results, ok := p.returns[memo]; ok {
		return results
	}
	p.returns[memo] = nil
	env := p.funcTaintFrom(fn, seeds)
	results := map[int]taintInfo{}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.FuncLit:
			// A closure's return is its own.
			return false
		case *ast.ReturnStmt:
			if len(x.Results) == 1 && fn.Type.Results != nil && fn.Type.Results.NumFields() > 1 {
				// return f(): f's results are fn's.
				if call, ok := x.Results[0].(*ast.CallExpr); ok {
					for i, info := range p.multiResults(call, len(fn.Type.Results.List), env) {
						results[i] = results[i].merge(info)
					}
				}
				return true
			}
			for i, result := range x.Results {
				info := p.exprInfo(result, env)
				switch {
				case info.kind != taintNone && returnsError(fn, i):
					info = taintInfo{kind: taintErr}
				case info.kind == taintCallResult:
					info.kind = taintNone
				}
				results[i] = results[i].merge(info)
			}
		}
		return true
	})
	p.returns[memo] = results
	return results
}

// multiResults is what each of the n results of call carries, where call
// is the one value of a multi-value assignment or return: as its declaration
// returns them for a call of the package, and otherwise its first result as
// the call carries it and each later one, an error at most.
func (p *censusPkg) multiResults(call *ast.CallExpr, n int, env censusEnv) map[int]taintInfo {
	if isRemedyCall(call) {
		if p.src.shellWords && isShellWordCall(call) {
			// The word remedy quotes is the value the backtick census tracks.
			return map[int]taintInfo{0: {kind: taintValue}}
		}
		return nil
	}
	if !p.src.calls[censusSelName(call)] {
		if results := p.callResults(call, env); results != nil {
			return results
		}
	}
	k := p.tainted(call, env)
	if k == taintNone {
		return nil
	}
	results := map[int]taintInfo{0: {kind: k}}
	for i := 1; i < n; i++ {
		results[i] = taintInfo{kind: taintCallResult}
	}
	return results
}

// structFields reports, for a struct literal (or its address), which keyed
// fields carry a clone-controlled value.
func (p *censusPkg) structFields(expr ast.Expr, env censusEnv) (map[string]bool, bool) {
	if u, ok := expr.(*ast.UnaryExpr); ok && u.Op == token.AND {
		expr = u.X
	}
	lit, ok := expr.(*ast.CompositeLit)
	if !ok || len(lit.Elts) == 0 {
		return nil, false
	}
	fields := map[string]bool{}
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			return nil, false
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok {
			return nil, false
		}
		if p.tainted(kv.Value, env) >= taintErr {
			fields[key.Name] = true
		}
	}
	return fields, true
}

// exprInfo is what expr carries, fields included.
func (p *censusPkg) exprInfo(expr ast.Expr, env censusEnv) taintInfo {
	if fields, ok := p.structFields(expr, env); ok {
		return taintInfo{fields: fields}
	}
	if id, ok := expr.(*ast.Ident); ok {
		if info, ok := env[varKey(id)]; ok {
			return info
		}
	}
	if call, ok := expr.(*ast.CallExpr); ok && !isRemedyCall(call) && !p.src.calls[censusSelName(call)] {
		if results := p.callResults(call, env); results != nil {
			return results[0]
		}
	}
	return taintInfo{kind: p.tainted(expr, env)}
}

// tainted reports what expr carries given the tracked variables env.
func (p *censusPkg) tainted(expr ast.Expr, env censusEnv) taintKind {
	kind := taintNone
	ast.Inspect(expr, func(n ast.Node) bool {
		if kind == taintValue {
			return false
		}
		switch x := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.CompositeLit:
			if _, ok := p.structFields(x, env); ok {
				// A struct carries a value only in the fields that hold it.
				return false
			}
		case *ast.CallExpr:
			if isRemedyCall(x) {
				return false
			}
			name := censusSelName(x)
			full := censusCallName(x)
			if p.src.calls[name] {
				kind = taintValue
				return false
			}
			if id, ok := x.Fun.(*ast.Ident); ok && env[varKey(id)].kind != taintNone {
				// A source passed in as a function value.
				kind = maxKind(kind, env[varKey(id)].kind)
				return false
			}
			if strings.HasPrefix(name, "Get") || strings.HasPrefix(name, "get") || strings.HasPrefix(name, "resolve") {
				for _, arg := range x.Args {
					if lit, ok := arg.(*ast.BasicLit); ok && lit.Kind == token.STRING {
						if key, err := strconv.Unquote(lit.Value); err == nil && p.src.keys[key] {
							kind = taintValue
							return false
						}
					}
				}
			}
			if censusValueFreeCalls[full] || censusValueFreeCalls[name] {
				return false
			}
			if results := p.callResults(x, env); results != nil {
				for _, info := range results {
					kind = maxKind(kind, info.kind)
				}
				return false
			}
			if pkg, _, ok := strings.Cut(full, "."); ok && censusPropagatingPkgs[pkg] || censusPropagatingCalls[full] {
				return true
			}
			if full == "fmt.Sprintf" || full == "fmt.Errorf" {
				// What it formats with %q is escaped.
				quoted := quotedArgs(x, full)
				for i, a := range x.Args {
					if !quoted[i] {
						kind = maxKind(kind, p.tainted(a, env))
					}
				}
				return false
			}
			if strings.HasPrefix(full, "fmt.Sprint") || name == "string" || name == "Error" {
				return true
			}
			// Any other call computes a value of its own, but its error can
			// repeat what it was passed. Its receiver can still be a source
			// field (cfg.ServerHost.String()).
			for _, a := range x.Args {
				if p.tainted(a, env) != taintNone {
					kind = maxKind(kind, taintCallResult)
				}
			}
			if sel, ok := x.Fun.(*ast.SelectorExpr); ok {
				ast.Inspect(sel.X, func(m ast.Node) bool {
					if s, ok := m.(*ast.SelectorExpr); ok && p.isSourceField(s.Sel.Name) {
						kind = taintValue
					}
					return kind != taintValue
				})
			}
			return false
		case *ast.SelectorExpr:
			if p.isSourceField(x.Sel.Name) {
				kind = taintValue
				return false
			}
			if id, ok := x.X.(*ast.Ident); ok {
				if info := env[varKey(id)]; info.fields[x.Sel.Name] {
					kind = taintValue
					return false
				}
			}
			return true
		case *ast.Ident:
			if k := env[varKey(x)].kind; k != taintNone {
				kind = maxKind(kind, k)
			} else if p.src.calls[x.Name] {
				// A source passed as a function value.
				kind = taintValue
			}
		}
		return true
	})
	return kind
}

// funcTaint computes what the variables of fn carry, its parameters seeded
// as its callers in the package pass them.
func (p *censusPkg) funcTaint(fn *ast.FuncDecl) censusEnv {
	return p.funcTaintFrom(fn, p.taintedParams[fn])
}

// funcTaintFrom computes what the variables of fn carry, seeded with the
// parameters params names.
func (p *censusPkg) funcTaintFrom(fn *ast.FuncDecl, params map[string]taintInfo) censusEnv {
	env := censusEnv{}
	for _, field := range fn.Type.Params.List {
		for _, name := range field.Names {
			if info, ok := params[name.Name]; ok {
				env[varKey(name)] = info
			}
		}
	}
	for changed := true; changed; {
		changed = false
		mark := func(lhs ast.Expr, info taintInfo) {
			if sel, ok := lhs.(*ast.SelectorExpr); ok {
				// x.f = value: the struct x carries it in f.
				if x, ok := sel.X.(*ast.Ident); ok && info.kind >= taintErr {
					old := env[varKey(x)]
					if !old.fields[sel.Sel.Name] {
						env[varKey(x)] = old.merge(taintInfo{fields: map[string]bool{sel.Sel.Name: true}})
						changed = true
					}
				}
				return
			}
			id, ok := lhs.(*ast.Ident)
			if !ok || id.Name == "_" {
				return
			}
			if info.kind == taintCallResult {
				// Another package's call result carries the value only as
				// its error.
				info.kind = taintNone
				if isErrorName(id.Name) {
					info.kind = taintErr
				}
			}
			if info.kind == taintNone && len(info.fields) == 0 {
				return
			}
			old := env[varKey(id)]
			merged := old.merge(info)
			if merged.kind != old.kind || len(merged.fields) != len(old.fields) {
				env[varKey(id)] = merged
				changed = true
			}
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.AssignStmt:
				if len(x.Rhs) == 1 && len(x.Lhs) > 1 {
					// value, err := call(): an ok after the value carries
					// nothing (mark drops a call result not named an error).
					if call, ok := x.Rhs[0].(*ast.CallExpr); ok {
						results := p.multiResults(call, len(x.Lhs), env)
						for i, lhs := range x.Lhs {
							mark(lhs, results[i])
						}
					}
					return true
				}
				for i, rhs := range x.Rhs {
					if i < len(x.Lhs) {
						mark(x.Lhs[i], p.exprInfo(rhs, env))
					}
				}
			case *ast.ValueSpec:
				for i, rhs := range x.Values {
					if i < len(x.Names) {
						mark(x.Names[i], p.exprInfo(rhs, env))
					}
				}
			case *ast.RangeStmt:
				if k := p.tainted(x.X, env); k != taintNone && x.Value != nil {
					mark(x.Value, taintInfo{kind: k})
				}
			}
			return true
		})
	}
	return env
}

// allFuncs is every declaration of the package, closures included.
func (p *censusPkg) allFuncs() []*ast.FuncDecl {
	all := slices.Clone(p.closures)
	for _, decls := range p.funcs {
		all = append(all, decls...)
	}
	return all
}

// propagate marks the parameters of the package's functions that a tracked
// argument reaches, until nothing changes, so a print call inside one is
// checked as its callers reach it.
func (p *censusPkg) propagate() {
	for changed := true; changed; {
		changed = false
		for _, fn := range p.allFuncs() {
			env := p.funcTaint(fn)
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || isRemedyCall(call) || censusPrintCalls[censusSelName(call)] || censusErrorPrinters[censusSelName(call)] {
					// A print call is checked where it is called.
					return true
				}
				for _, callee := range p.callees(call) {
					for name, info := range p.argSeeds(call, callee, env) {
						if p.taintedParams[callee] == nil {
							p.taintedParams[callee] = map[string]taintInfo{}
						}
						old := p.taintedParams[callee][name]
						if merged := old.merge(info); merged.kind != old.kind || len(merged.fields) != len(old.fields) {
							p.taintedParams[callee][name] = merged
							changed = true
						}
					}
				}
				return true
			})
		}
	}
}

// stringLiteral is the value of a string literal, or of literals joined
// with +.
func stringLiteral(expr ast.Expr) (string, bool) {
	switch x := expr.(type) {
	case *ast.BasicLit:
		if x.Kind != token.STRING {
			return "", false
		}
		value, err := strconv.Unquote(x.Value)
		return value, err == nil
	case *ast.BinaryExpr:
		if x.Op != token.ADD {
			return "", false
		}
		left, ok := stringLiteral(x.X)
		if !ok {
			return "", false
		}
		right, ok := stringLiteral(x.Y)
		return left + right, ok
	case *ast.ParenExpr:
		return stringLiteral(x.X)
	}
	return "", false
}

// quotedArgs returns the indexes of call's arguments that its format string
// spells with %q, which escapes every character a terminal acts on, or nil
// when the format is not a literal or numbers its arguments explicitly.
func quotedArgs(call *ast.CallExpr, name string) map[int]bool {
	formatAt := 0
	switch name {
	case "fmt.Fprintf":
		formatAt = 1
	case "fmt.Errorf", "fmt.Sprintf", "fmt.Printf", "HandleError", "HandleErrorRespectJSON", "WarnError":
	default:
		return nil
	}
	if len(call.Args) <= formatAt {
		return nil
	}
	format, ok := stringLiteral(call.Args[formatAt])
	if !ok || strings.Contains(format, "%[") {
		return nil
	}
	quoted := map[int]bool{}
	arg := formatAt + 1
	for i := 0; i < len(format); i++ {
		if format[i] != '%' {
			continue
		}
		i++
		for i < len(format) && strings.IndexByte("+-# 0123456789.*", format[i]) >= 0 {
			if format[i] == '*' {
				arg++
			}
			i++
		}
		if i == len(format) {
			break
		}
		if format[i] == '%' {
			continue
		}
		if format[i] == 'q' {
			quoted[arg] = true
		}
		arg++
	}
	return quoted
}

// parseCensusPkgs parses every production Go file under roots, grouped by
// package directory, and fails when a root held none. The taint source a pass
// uses (configSources, storeIDSources, shellWordSources) is set on each
// package before it is analyzed, so one parse serves every census.
func parseCensusPkgs(t *testing.T, repoRoot string, roots ...string) map[string]*censusPkg {
	t.Helper()
	pkgs := map[string]*censusPkg{}
	for _, root := range roots {
		err := filepath.WalkDir(filepath.Join(repoRoot, root), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if name := d.Name(); name == "testdata" || name == "node_modules" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, _ := filepath.Rel(repoRoot, path)
			rel = filepath.ToSlash(rel)
			if strings.HasPrefix(rel, "internal/remedy/") {
				return nil
			}
			dir := filepath.Dir(path)
			pkg := pkgs[dir]
			if pkg == nil {
				pkg = &censusPkg{fset: token.NewFileSet(), files: map[string]*ast.File{}, funcs: map[string][]*ast.FuncDecl{}, declFiles: map[*ast.FuncDecl]string{}}
				pkgs[dir] = pkg
			}
			file, err := parser.ParseFile(pkg.fset, path, nil, 0)
			if err != nil {
				return err
			}
			pkg.files[rel] = file
			for _, decl := range file.Decls {
				switch d := decl.(type) {
				case *ast.FuncDecl:
					if d.Body != nil {
						pkg.funcs[d.Name.Name] = append(pkg.funcs[d.Name.Name], d)
						pkg.declFiles[d] = rel
					}
				case *ast.GenDecl:
					// A package-level function literal, such as a cobra
					// command's RunE, is checked as a function of its own,
					// named after the variable it initializes.
					for _, spec := range d.Specs {
						vs, ok := spec.(*ast.ValueSpec)
						if !ok {
							continue
						}
						for i, value := range vs.Values {
							name := "_"
							if i < len(vs.Names) {
								name = vs.Names[i].Name
							}
							ast.Inspect(value, func(n ast.Node) bool {
								lit, ok := n.(*ast.FuncLit)
								if !ok {
									return true
								}
								fn := &ast.FuncDecl{Name: ast.NewIdent(name), Type: lit.Type, Body: lit.Body}
								pkg.closures = append(pkg.closures, fn)
								pkg.declFiles[fn] = rel
								return false
							})
						}
					}
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
	// A census that read nothing would pass: under Bazel the files are the
	// test's data, and one that dropped out must fail here.
	for _, root := range roots {
		parsed := 0
		for _, pkg := range pkgs {
			for rel := range pkg.files {
				if strings.HasPrefix(rel, root+"/") {
					parsed++
				}
			}
		}
		if parsed == 0 {
			t.Fatalf("the census parsed no Go file under %s", root)
		}
	}
	return pkgs
}

// analyze propagates src through every package of pkgs, then calls check for
// each of its functions with that function's taint environment and a report
// callback keyed to the node's line. The returned sites are sorted and
// deduped.
func analyze(pkgs map[string]*censusPkg, src censusSources, check func(pkg *censusPkg, rel string, fn *ast.FuncDecl, env censusEnv, report func(ast.Node))) []string {
	var found []string
	for _, pkg := range pkgs {
		pkg.src = src
		pkg.taintedParams = map[*ast.FuncDecl]map[string]taintInfo{}
		pkg.returns = map[string]map[int]taintInfo{}
		pkg.propagate()
		for _, fn := range pkg.allFuncs() {
			rel := pkg.declFiles[fn]
			if censusNotPrinted[rel+" "+fn.Name.Name] != "" {
				continue
			}
			env := pkg.funcTaint(fn)
			check(pkg, rel, fn, env, func(n ast.Node) {
				found = append(found, rel+":"+strconv.Itoa(pkg.fset.Position(n.Pos()).Line)+" "+fn.Name.Name)
			})
		}
	}
	slices.Sort(found)
	return slices.Compact(found)
}

// censusViolations returns "path:line function" for each print call in the
// packages under roots that formats a clone-controlled value (configSources)
// without the remedy printer.
func censusViolations(t *testing.T, repoRoot string, roots ...string) []string {
	t.Helper()
	return analyze(parseCensusPkgs(t, repoRoot, roots...), configSources, func(pkg *censusPkg, rel string, fn *ast.FuncDecl, env censusEnv, report func(ast.Node)) {
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || isRemedyCall(call) {
				return true
			}
			name, sel := censusCallName(call), censusSelName(call)
			errorPrinter := censusErrorPrinters[sel]
			if !censusPrintCalls[name] && !censusPrintCalls[sel] && !errorPrinter {
				return true
			}
			// An error is printed safely by bd's error printers and
			// passed on when it is formatted into a message; it must be
			// spelled where it goes to the terminal directly.
			errReaches := censusTerminalCalls[name]
			quoted := quotedArgs(call, name)
			if errorPrinter {
				quoted = quotedArgs(call, sel)
			}
			for i, arg := range call.Args {
				if quoted[i] {
					continue
				}
				k := pkg.tainted(arg, env)
				if k == taintValue || k >= taintCallResult && errReaches {
					report(call)
					break
				}
			}
			return true
		})
	})
}

// pastedText is a string a print builds that reaches the terminal: text, with
// an argument expression recorded at each hole where a value is formatted or
// concatenated in. holes[i] is the byte offset of the i-th hole in text, and
// args[i] the expression there.
type pastedText struct {
	text  string
	holes []int
	args  []ast.Expr
}

// formatText is the text a call with a literal format string prints, with a
// hole for each %s, %v or %q. %q is a hole too: it escapes every character a
// terminal acts on, but Go's double quotes leave a $(...) and a backtick live
// in sh, bash and zsh, so a %q value pasted into a command still runs (#7837's
// terminal pass, censusViolations, uses quotedArgs and keeps skipping %q). It
// is false for a format that numbers its arguments (%[1]s), which this does
// not model.
func formatText(call *ast.CallExpr) (pastedText, bool) {
	for fi, a := range call.Args {
		format, ok := stringLiteral(a)
		if !ok || !strings.Contains(format, "%") {
			continue
		}
		if strings.Contains(format, "%[") {
			return pastedText{}, false
		}
		pt := pastedText{text: format}
		arg := fi + 1
		for i := 0; i < len(format); i++ {
			if format[i] != '%' {
				continue
			}
			start := i
			i++
			for i < len(format) && strings.IndexByte("+-# 0123456789.*", format[i]) >= 0 {
				if format[i] == '*' {
					arg++
				}
				i++
			}
			if i == len(format) {
				break
			}
			if format[i] == '%' {
				continue
			}
			if (format[i] == 's' || format[i] == 'v' || format[i] == 'q') && arg < len(call.Args) {
				pt.holes = append(pt.holes, start)
				pt.args = append(pt.args, call.Args[arg])
			}
			arg++
		}
		return pt, true
	}
	return pastedText{}, false
}

// printlnText is the text fmt.Println or fmt.Fprintln prints from several
// operands, which it joins with spaces: its literal operands, with a hole for
// each other one. It is false for any other call.
func printlnText(call *ast.CallExpr) (pastedText, bool) {
	args := call.Args
	switch censusCallName(call) {
	case "fmt.Println":
	case "fmt.Fprintln":
		if len(args) == 0 {
			return pastedText{}, false
		}
		args = args[1:]
	default:
		return pastedText{}, false
	}
	var pt pastedText
	for i, a := range args {
		if i > 0 {
			pt.text += " "
		}
		if lit, ok := stringLiteral(a); ok {
			pt.text += lit
			continue
		}
		pt.holes = append(pt.holes, len(pt.text))
		pt.args = append(pt.args, a)
	}
	return pt, true
}

// concatText is the text of a + chain of strings: its literal parts joined,
// with a hole for each non-literal operand.
func concatText(expr *ast.BinaryExpr) pastedText {
	var pt pastedText
	var walk func(e ast.Expr)
	walk = func(e ast.Expr) {
		switch x := e.(type) {
		case *ast.BinaryExpr:
			if x.Op == token.ADD {
				walk(x.X)
				walk(x.Y)
				return
			}
		case *ast.ParenExpr:
			walk(x.X)
			return
		}
		if lit, ok := stringLiteral(e); ok {
			pt.text += lit
			return
		}
		pt.holes = append(pt.holes, len(pt.text))
		pt.args = append(pt.args, e)
	}
	walk(expr)
	return pt
}

// pastedTexts calls visit with each format-call and each outermost + chain of
// strings in body, so a hint built either way is checked once. A + chain's
// operands may themselves be format calls (fmt.Sprintf inside a concat), so
// those are visited too.
func pastedTexts(body ast.Node, visit func(n ast.Node, pt pastedText)) {
	ast.Inspect(body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.CallExpr:
			if isRemedyCall(x) {
				return true
			}
			if pt, ok := formatText(x); ok && len(pt.holes) > 0 {
				visit(x, pt)
			} else if pt, ok := printlnText(x); ok && len(pt.holes) > 0 && strings.Contains(pt.text, "bd ") {
				visit(x, pt)
			}
		case *ast.BinaryExpr:
			if x.Op != token.ADD {
				return true
			}
			if pt := concatText(x); len(pt.holes) > 0 && strings.Contains(pt.text, "bd ") {
				visit(x, pt)
			}
			// Keep descending into the chain's operands for nested calls, but
			// not back into the chain itself.
			ast.Inspect(x, func(m ast.Node) bool {
				if call, ok := m.(*ast.CallExpr); ok {
					pastedTexts(call, visit)
					return false
				}
				return true
			})
			return false
		}
		return true
	})
}

// inBdCommand reports whether the hole at pos in text falls inside a "bd
// <subcommand>" command: after a "bd " that begins a word and is followed by
// a lower-case subcommand letter, up to the end of its line, or up to the
// quote, backtick or parenthesis that closes one opened right before the "bd".
// It is how the census tells a value pasted into a command (which must be one
// shell word) from a value merely named in prose beside it.
func inBdCommand(text string, pos int) bool {
	lineStart := strings.LastIndexByte(text[:pos], '\n') + 1
	line := text[lineStart:pos]
	for i := 0; i+3 <= len(line); i++ {
		if !strings.HasPrefix(line[i:], "bd ") {
			continue
		}
		if i+3 >= len(line) || line[i+3] < 'a' || line[i+3] > 'z' {
			continue
		}
		if i > 0 && !strings.ContainsRune(" \t('\"`:", rune(line[i-1])) {
			continue
		}
		closer := byte(0)
		if i > 0 {
			switch line[i-1] {
			case '(':
				closer = ')'
			case '\'', '"', '`':
				closer = line[i-1]
			}
		}
		if closer == 0 || strings.IndexByte(line[i+3:], closer) < 0 {
			return true
		}
	}
	return false
}

// inBackticks reports whether the hole at pos in text falls between a pair of
// literal backticks, which sh, bash and zsh run as a command substitution.
func inBackticks(text string, pos int) bool {
	return strings.Count(text[:pos], "`")%2 == 1
}

// storeIDHintAllowlist excuses a bd-command hint that names an issue ID the
// store-ID census tracks but that is not a pulled ID a collaborator controls:
// an ID bd mints in the same command (always a plain word), or the user's own
// typed argument echoed back. Each is "file:function", with the reason.
// bd find-duplicates, bd mol ready --gated, the unserved auto-close note, the
// show/dep-list hints and bd dep add's malformed-target refusal are NOT here:
// they paste an ID read from the store (on the direct route dep add's source
// is the ID a partial argument resolved to), and route it through
// remedy.CommandArg.
var storeIDHintAllowlist = map[string]string{
	"cmd/bd/gate.go:renderGateCreated":           "bd gate resolve names the gate ID lifecycle.Create minted in this command; a minted ID is a plain word",
	"cmd/bd/wisp.go:renderWispCreateResult":      "bd close/squash/burn name the wisp root ID CreateIssue minted in this command (#7868)",
	"cmd/bd/delete.go:renderSingleDeletePreview": "bd delete <id> --force echoes the user's argument; bd delete refuses a partial ID, so the resolved ID equals what the user typed",
	"cmd/bd/comments.go:commentsAddCmd":          "bd show <id> names the user's typed argument in the abbreviation refusal, before resolution assigns ResolvedID",
	"cmd/bd/show.go:showNotFoundHint":            "bd history <id> echoes the user's argument, which resolved to no issue",
}

// configHintAllowlist excuses a site the config-value bd-command census
// flags that pastes no clone-controlled value into a bd command. Each is
// "file:function", with the reason. inBdCommand reads any "bd <lower-case
// word>" as a command, so prose that starts "bd <verb>" is one.
var configHintAllowlist = map[string]string{
	"cmd/bd/create.go:refuseUninitializableTarget":        "prose: \"so bd run there does not treat it as a workspace\" names no command; the only command, bd init --from-jsonl, is closed by its backticks before the target path the sentence names",
	"internal/doltserver/servermode.go:Advice":            "prose: \"bd did not start one at %s\" names the endpoint in a sentence, not in a command",
	"internal/storage/dolt/store.go:autoStartRedialError": "prose: \"Check logs: \" names the log path on its own line; the only command, bd dolt stop, takes no value",
}

// backtickRemedyAllowlist excuses a remedy word a format or + chain wraps in
// literal backticks, for a justified exception. Empty today: #7868 and
// its follow-up dropped the wrapper at every site, so a new one is a defect.
var backtickRemedyAllowlist = map[string]string{}

// storeIDCommandViolations returns the bd-command hints that paste an issue ID
// read from the store without the remedy printer (bdCommandViolations with
// storeIDSources).
func storeIDCommandViolations(pkgs map[string]*censusPkg, allow map[string]string) []string {
	return bdCommandViolations(pkgs, storeIDSources, allow)
}

// configCommandViolations returns the bd-command hints that paste a
// clone-controlled config, metadata or routing value (#7837's configSources)
// without a shell-word remedy printer (bdCommandViolations with
// configSources).
func configCommandViolations(pkgs map[string]*censusPkg, allow map[string]string) []string {
	return bdCommandViolations(pkgs, configSources, allow)
}

// bdCommandViolations returns the bd-command hints that paste a value src
// tracks without a shell-word remedy printer: a %s, %v or %q (or a Println
// operand) inside a "bd <subcommand>" fed by a tracked value, raw, through a
// remedy printer that leaves a $(...) as it is (PrintedValue, PrintedText and
// the like rather than shellWordPrinters), or through a variable such a
// printer's result was stored in, outside allow.
func bdCommandViolations(pkgs map[string]*censusPkg, src censusSources, allow map[string]string) []string {
	return analyze(pkgs, src, func(pkg *censusPkg, rel string, fn *ast.FuncDecl, env censusEnv, report func(ast.Node)) {
		if allow[rel+":"+fn.Name.Name] != "" {
			return
		}
		printed := printedVars(fn, func(rhs ast.Expr) bool { return pkg.printedUnquoted(rhs, env) })
		pastedTexts(fn.Body, func(n ast.Node, pt pastedText) {
			for i, hole := range pt.holes {
				arg := pt.args[i]
				if inBdCommand(pt.text, hole) && (pkg.tainted(arg, env) == taintValue || pkg.printedUnquoted(arg, env) || printed.holds(arg)) {
					report(n)
					return
				}
			}
		})
	})
}

// printedSet is the variables of one function that hold a remedy printer's
// result a census pass tracks, keyed by varKey.
type printedSet map[any]bool

func (s printedSet) holds(expr ast.Expr) bool {
	id, ok := ast.Unparen(expr).(*ast.Ident)
	return ok && s[varKey(id)]
}

// printedVars returns the variables of fn assigned a value isPrinted accepts,
// or copied from one that was, so a pass that checks a remedy printer's
// result called in place also sees it stored first (id :=
// remedy.PrintedValue(a.ID), then "bd show %s" with id).
func printedVars(fn *ast.FuncDecl, isPrinted func(ast.Expr) bool) printedSet {
	set := printedSet{}
	for changed := true; changed; {
		changed = false
		mark := func(lhs, rhs ast.Expr) {
			id, ok := lhs.(*ast.Ident)
			if !ok || id.Name == "_" || set[varKey(id)] {
				return
			}
			if isPrinted(rhs) || set.holds(rhs) {
				set[varKey(id)] = true
				changed = true
			}
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.AssignStmt:
				if len(x.Lhs) == len(x.Rhs) {
					for i := range x.Rhs {
						mark(x.Lhs[i], x.Rhs[i])
					}
				}
			case *ast.ValueSpec:
				if len(x.Names) == len(x.Values) {
					for i := range x.Values {
						mark(x.Names[i], x.Values[i])
					}
				}
			}
			return true
		})
	}
	return set
}

// shellWordPrinters are the remedy calls whose result can go into a pasted
// command: a value quoted for a shell (or a placeholder), or one escaped so
// that no part of it is shell syntax.
var shellWordPrinters = map[string]bool{
	"remedy.CommandArg":     true,
	"remedy.ShellWord":      true,
	"remedy.ShellQuotedFor": true,
	"remedy.PrintedWord":    true,
	"remedy.EscapedValue":   true,
}

// printedUnquoted reports whether expr is a call to a remedy printer other
// than shellWordPrinters (PrintedValue, PrintedText, ...) given a tracked
// value: such a printer leaves a $(...) or a backtick as it is, so it does not
// make the value safe to paste.
func (p *censusPkg) printedUnquoted(expr ast.Expr, env censusEnv) bool {
	call, ok := ast.Unparen(expr).(*ast.CallExpr)
	if !ok || !isRemedyCall(call) || shellWordPrinters[censusCallName(call)] {
		return false
	}
	for _, a := range call.Args {
		if p.tainted(a, env) == taintValue {
			return true
		}
	}
	return false
}

// backtickedRemedyViolations returns the remedy words a format or + chain
// wraps in literal backticks (shellWordSources), outside allow.
func backtickedRemedyViolations(pkgs map[string]*censusPkg, allow map[string]string) []string {
	return analyze(pkgs, shellWordSources, func(pkg *censusPkg, rel string, fn *ast.FuncDecl, env censusEnv, report func(ast.Node)) {
		if allow[rel+":"+fn.Name.Name] != "" {
			return
		}
		printed := printedVars(fn, isRemedyPrinted)
		pastedTexts(fn.Body, func(n ast.Node, pt pastedText) {
			for i, hole := range pt.holes {
				arg := pt.args[i]
				if inBackticks(pt.text, hole) && (pkg.tainted(arg, env) == taintValue || isRemedyPrinted(arg) || printed.holds(arg)) {
					report(n)
					return
				}
			}
		})
	})
}

// isRemedyPrinted reports whether expr is a call in place to a remedy printer
// that can return a backtick (PrintedValue, PrintedText, CommandArg, ...):
// only EscapedValue and PrintedWord escape one, so any other's result closes
// a literal backtick pair it is printed between.
func isRemedyPrinted(expr ast.Expr) bool {
	call, ok := ast.Unparen(expr).(*ast.CallExpr)
	if !ok {
		return false
	}
	name := censusCallName(call)
	return strings.HasPrefix(name, "remedy.") && name != "remedy.EscapedValue" && name != "remedy.PrintedWord"
}

// requireAllowlistLive fails for each entry of allow ("file:function") that
// the pass, run with no allowlist, no longer flags: the code it excused is
// gone or fixed, so the entry must go before it can excuse something new.
func requireAllowlistLive(t *testing.T, name string, allow map[string]string, unfiltered []string) {
	t.Helper()
	hit := map[string]bool{}
	for _, v := range unfiltered {
		loc, fn, _ := strings.Cut(v, " ")
		file, _, _ := strings.Cut(loc, ":")
		hit[file+":"+fn] = true
	}
	for site := range allow {
		if !hit[site] {
			t.Errorf("%s: allowlisted site is no longer flagged; drop it from %s", site, name)
		}
	}
}

// TestRemedyCensusStoreIDHints is the source-keyed guard on the second remedy
// class #7868 opened: a bd command a hint prints with an issue ID read from
// the store, which holds a collaborator's issues after bd dolt pull. Such an
// ID goes into the command through remedy.CommandArg (bare when it is a plain
// word, quoted otherwise, or a placeholder named escaped after it), as bd
// duplicates, bd mol current, bd find-duplicates, bd mol ready
// --gated, the unserved auto-close note, the show/dep-list hints and bd dep
// add's malformed-target refusal do. A hint that pastes one raw, or through a
// remedy printer that leaves a $(...) as it is, fails here;
// storeIDHintAllowlist excuses the sites whose ID bd mints or the user typed,
// each with a reason, as dolt_host_policy_test.go does, and an entry the pass
// no longer flags fails as stale. The behavioral half is cmd/bd's
// TestStoreReadIDHintsQuoteIDs, TestStoreReadCommandHintsQuoteIDs and the
// TestEmbeddedRemedyPrinting_* end-to-end tests.
func TestRemedyCensusStoreIDHints(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	pkgs := parseCensusPkgs(t, repoRoot, "cmd/bd", "internal")
	for _, v := range storeIDCommandViolations(pkgs, storeIDHintAllowlist) {
		t.Errorf("%s pastes a store-read issue ID into a bd command without remedy.CommandArg (internal/remedy); route it through remedy.CommandArg, or allowlist it in storeIDHintAllowlist with a reason", v)
	}
	// An allowlisted site the pass no longer flags has grown stale; drop it
	// so the list cannot outlive the code it excuses.
	requireAllowlistLive(t, "storeIDHintAllowlist", storeIDHintAllowlist, storeIDCommandViolations(pkgs, nil))
}

// TestRemedyCensusConfigHints is the bd-command guard for #7837's sources: a
// bd command a hint prints with a clone-controlled config, metadata or
// routing value (configSources) puts it in through a shell-word remedy
// printer (remedy.CommandArg and shellWordPrinters). #7837's terminal pass
// (TestRemedyCensus) accepts remedy.PrintedValue and %q, which keep a value
// from redrawing the terminal but leave a $(...) or backtick live in a pasted
// command; this pass does not. configHintAllowlist excuses prose sites, each
// with a reason, and an entry the pass no longer flags fails as stale.
func TestRemedyCensusConfigHints(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	pkgs := parseCensusPkgs(t, repoRoot, "cmd/bd", "internal")
	for _, v := range configCommandViolations(pkgs, configHintAllowlist) {
		t.Errorf("%s pastes a config, metadata or routing value into a bd command without a shell-word remedy printer (internal/remedy); route it through remedy.CommandArg, or allowlist it in configHintAllowlist with a reason", v)
	}
	requireAllowlistLive(t, "configHintAllowlist", configHintAllowlist, configCommandViolations(pkgs, nil))
}

// TestRemedyCensusBacktickedRemedies is the source-keyed guard on the first
// remedy class #7868 opened: a remedy value quoted for a shell
// (remedy.CommandArg, ShellWord or ShellQuotedFor), or one a remedy printer
// called in place returns with any backtick it holds (PrintedValue,
// PrintedText), printed between literal backticks. sh, bash and zsh run a pasted `...` as a command substitution,
// and remedy single-quotes a value holding a backtick on POSIX, so the value's
// own backtick ends the substitution and the rest of it runs. #7868 (bd mol
// current) and its follow-up (bd doctor routing repos, bd create --repo dolt.host,
// migrate-personal, federation add-peer, bd ship, bd backup init, the
// installation, dolt reset and memory remedies) dropped the wrapper; a new one
// fails here. backtickRemedyAllowlist is empty; an entry the pass no longer
// flags would fail as stale.
func TestRemedyCensusBacktickedRemedies(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	pkgs := parseCensusPkgs(t, repoRoot, "cmd/bd", "internal")
	for _, v := range backtickedRemedyViolations(pkgs, backtickRemedyAllowlist) {
		t.Errorf("%s wraps a remedy shell word in literal backticks; drop the backticks (a pasted `...` is a command substitution), or allowlist it in backtickRemedyAllowlist with a reason", v)
	}
	requireAllowlistLive(t, "backtickRemedyAllowlist", backtickRemedyAllowlist, backtickedRemedyViolations(pkgs, nil))
}

// TestRemedyHintCensusFlagsItsClasses pins the three hint censuses on a
// package of their own: a store-read ID or a config value pasted into a bd
// command raw, with %q, as a Println operand, or through remedy.PrintedValue
// (in place or stored in a variable first), and a remedy word or a
// PrintedValue in backticks (in place or stored), are each flagged; the same
// value named with remedy.PrintedWord, put in a command through
// remedy.CommandArg, or named only in prose beside the command, is not; and
// neither is a remedy word outside backticks or an EscapedValue inside them.
func TestRemedyHintCensusFlagsItsClasses(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	src := `package probe

import (
	"fmt"
	"runtime"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/remedy"
	"github.com/steveyegge/beads/internal/utils"
	"github.com/steveyegge/beads/issueops"
)

type issue struct{ ID, Title string }

func readIssue() *issue { return &issue{} }

func rawHint() {
	a := readIssue()
	fmt.Printf("Compare: bd show %s\n", a.ID) // storeid
}

func rawConcatHint(resolvedID string) {
	println("run: bd dep list " + resolvedID) // not-a-field
}

func quotedHint() {
	a := readIssue()
	arg, note := remedy.CommandArg(runtime.GOOS, a.ID, "<id>")
	fmt.Printf("Compare: bd show %s%s\n", arg, note)
}

func printedValueHint() {
	a := readIssue()
	fmt.Printf("Compare: bd show %s\n", remedy.PrintedValue(a.ID)) // printedvalue
}

func namedBesideHint() {
	a := readIssue()
	fmt.Printf("  %s is a duplicate\n", remedy.PrintedWord(a.ID))
}

func proseOnly() {
	a := readIssue()
	fmt.Printf("issue %s was closed\n", a.ID)
}

func backtickRemedy(host string) {
	arg, note := remedy.CommandArg(runtime.GOOS, host, "<host>")
	fmt.Printf("Run ` + "`" + `bd repo add %s` + "`" + `%s\n", arg, note) // backtick
}

func backtickPrinted(host string) {
	fmt.Printf("Run ` + "`" + `bd repo add %s` + "`" + `\n", remedy.PrintedValue(host)) // backtick-printed
}

func backtickEscaped(host string) {
	fmt.Printf("host ` + "`" + `%s` + "`" + `\n", remedy.EscapedValue(host))
}

func plainRemedy(host string) {
	arg, note := remedy.CommandArg(runtime.GOOS, host, "<host>")
	fmt.Printf("Run bd repo add %s%s\n", arg, note)
}

func quotedVerbHint() {
	a := readIssue()
	fmt.Printf("Compare: bd show %q\n", a.ID) // storeid-q
}

func storedPrintedHint() {
	a := readIssue()
	id := remedy.PrintedValue(a.ID)
	fmt.Printf("Compare: bd show %s\n", id) // storeid-stored
}

func printlnHint() {
	a := readIssue()
	fmt.Println("  Compare: bd show", a.ID) // storeid-println
}

func printlnProse() {
	a := readIssue()
	fmt.Println("closed", a.ID)
}

func storedBacktick(host string) {
	shown := remedy.PrintedValue(host)
	fmt.Printf("Run ` + "`" + `bd repo add %s` + "`" + `\n", shown) // backtick-stored
}

func resolvedHint(arg string) {
	id, _ := utils.ResolvePartialID(ctx, store, arg)
	fmt.Printf("Run: bd show %s\n", id) // storeid-resolved
}

func resolvedRootsHint(ids []string) {
	roots, _ := issueops.MoleculeRoots(ctx, store, ids)
	for _, root := range roots {
		fmt.Println("  bd mol show", root) // storeid-roots
	}
}

func resolvedQuotedHint(arg string) {
	id, _ := utils.ResolvePartialID(ctx, store, arg)
	shown, note := remedy.CommandArg(runtime.GOOS, id, "<id>")
	fmt.Printf("Run: bd show %s%s\n", shown, note)
}

func configHint() {
	fmt.Printf("Run: bd repo add %q\n", config.GetString("routing.contributor")) // config-q
}

func configPrintedHint() {
	repo := remedy.PrintedValue(config.GetString("routing.contributor"))
	fmt.Printf("Run: bd repo add %s\n", repo) // config-printed
}

func configQuotedHint() {
	arg, note := remedy.CommandArg(runtime.GOOS, config.GetString("routing.contributor"), "<repo>")
	fmt.Printf("Run: bd repo add %s%s\n", arg, note)
}
`
	dir := filepath.Join(root, "cmd", "bd")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "probe.go"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	pkgs := parseCensusPkgs(t, root, "cmd/bd")
	wantLine := func(marker string) int {
		for i, line := range strings.Split(src, "\n") {
			if strings.HasSuffix(line, "// "+marker) {
				return i + 1
			}
		}
		t.Fatalf("no line marked %q", marker)
		return 0
	}
	site := func(fn, marker string) string { return fmt.Sprintf("cmd/bd/probe.go:%d %s", wantLine(marker), fn) }
	sorted := func(v ...string) []string { slices.Sort(v); return v }
	if got, want := storeIDCommandViolations(pkgs, nil), sorted(
		site("rawHint", "storeid"), site("printedValueHint", "printedvalue"),
		site("quotedVerbHint", "storeid-q"), site("storedPrintedHint", "storeid-stored"),
		site("printlnHint", "storeid-println"),
		site("resolvedHint", "storeid-resolved"), site("resolvedRootsHint", "storeid-roots"),
	); !slices.Equal(got, want) {
		t.Errorf("storeIDCommandViolations = %q, want %q", got, want)
	}
	if got, want := backtickedRemedyViolations(pkgs, nil), sorted(
		site("backtickRemedy", "backtick"), site("backtickPrinted", "backtick-printed"),
		site("storedBacktick", "backtick-stored"),
	); !slices.Equal(got, want) {
		t.Errorf("backtickedRemedyViolations = %q, want %q", got, want)
	}
	if got, want := configCommandViolations(pkgs, nil), sorted(
		site("configHint", "config-q"), site("configPrintedHint", "config-printed"),
	); !slices.Equal(got, want) {
		t.Errorf("configCommandViolations = %q, want %q", got, want)
	}
}

// TestRemedyCensusFlagsABypass pins the census itself on a package of its
// own. A clone-controlled value formatted bare is reported however it gets
// there: read in place, derived from one, passed into a function of the
// package, returned by one (alone or in a struct field), passed as a function
// value, assigned to a struct field, or read in a package-level closure such
// as a cobra RunE. An error that can repeat one is reported where it is
// printed to the terminal directly. The same value spelled through the
// remedy printer or with %q, and an error passed to bd's error printers or
// wrapped into another error, are not.
func TestRemedyCensusFlagsABypass(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	src := `package probe

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/steveyegge/beads/internal/remedy"
	"github.com/steveyegge/beads/internal/routing"
	"github.com/steveyegge/beads/internal/storage/doltutil"
)

func direct(host string, port int) error {
	return fmt.Errorf("unreachable at %s", doltutil.ServerAddr(host, port)) // flagged
}

func derived(cfg *Config) string {
	socket := filepath.Join(cfg.GetDoltServerSocket(), "x")
	return fmt.Sprintf("dolt sql-server --socket %s", socket) // flagged
}

func viaParam(cfg *Config) error {
	return refuse(config.GetString("sync.remote"))
}

func refuse(remote string) error {
	return fmt.Errorf("Run: git ls-remote %s", remote) // flagged
}

func printed(host string, port int, cfg *Config) error {
	addr := doltutil.ServerAddr(host, port)
	remote := remedy.PrintedValue(config.GetString("sync.remote"))
	_, err := store.GetConfig(ctx, "routing.contributor")
	return fmt.Errorf("at %s, %s, %s, %q: %w", remedy.PrintedValue(addr), doltutil.PrintedServerAddr(host, port), remote, addr, err)
}

type conflict struct {
	host   string
	source string
}

func detectConflict(host string) *conflict {
	return &conflict{host: host, source: "config.yaml"}
}

func viaStructReturn() error {
	c := detectConflict(config.GetYamlConfig("dolt.host"))
	return fmt.Errorf("dolt.host (%s) is set via %s", c.host, c.source) // flagged
}

func viaStructReturnPrinted() error {
	c := detectConflict(config.GetYamlConfig("dolt.host"))
	return fmt.Errorf("dolt.host (%s) is set via %s", remedy.PrintedValue(c.host), c.source)
}

func resolveWith(resolve func() string) (string, int) {
	if url := resolve(); url != "" {
		return url, 1
	}
	return "", 0
}

func viaFuncValue() {
	url, _ := resolveWith(resolveSyncRemote)
	fmt.Printf("Bootstrapped from remote: %s\n", url) // flagged
}

func routedPath() string {
	path, _ := routing.DetermineTargetRepoWithRule(cfg, role, ".")
	return routing.ExpandPath(path)
}

func viaReturn() error {
	return fmt.Errorf("failed to open routed store at %s", routedPath()) // flagged
}

type plan struct{ Database string }

func viaFieldAssignment(cfg *Config) {
	var p plan
	p.Database = cfg.GetDoltDatabase()
	fmt.Printf("  Database: %s\n", p.Database) // flagged
}

func errorPrinted(cfg *Config) error {
	_, err := os.Stat(cfg.GetDoltServerSocket())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err) // flagged
		fmt.Fprintf(os.Stderr, "Error: %s\n", remedy.PrintedMessage(err))
		return HandleError("stat socket: %v", err)
	}
	return fmt.Errorf("stat socket: %w", err)
}

var initCmd = &cobra.Command{
	RunE: func(cmd *cobra.Command, args []string) error {
		host := config.GetYamlConfig("dolt.host")
		return fmt.Errorf("dolt.host (%s) needs server mode", host) // flagged
	},
}
`
	dir := filepath.Join(root, "cmd", "bd")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "probe.go"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	var want []string
	for i, line := range strings.Split(src, "\n") {
		if strings.HasSuffix(line, "// flagged") {
			fn := ""
			for j := i; j >= 0; j-- {
				l := strings.Split(src, "\n")[j]
				if strings.HasPrefix(l, "func ") {
					fn = strings.TrimPrefix(l, "func ")
					fn = fn[:strings.IndexAny(fn, "(")]
					break
				}
				if strings.HasPrefix(l, "var ") {
					fn = strings.Fields(l)[1]
					break
				}
			}
			want = append(want, "cmd/bd/probe.go:"+strconv.Itoa(i+1)+" "+fn)
		}
	}
	slices.Sort(want)
	if len(want) != 9 {
		t.Fatalf("the fixture marks %d calls flagged, want 9: %q", len(want), want)
	}
	got := censusViolations(t, root, "cmd/bd")
	if !slices.Equal(got, want) {
		t.Errorf("census reported %q, want %q", got, want)
	}
}

// TestRemedyCensus runs the census over every production file of cmd/bd and
// internal/: a message that formats the Dolt server's host, address or
// socket from metadata.json, or a committed config.yaml's sync.remote or
// routing repo, without the remedy printer fails here.
func TestRemedyCensus(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repoRoot, "cmd", "bd")); err != nil {
		t.Fatalf("the census reads cmd/bd and internal/ from the repository: %v", err)
	}
	for _, file := range []string{"cmd/bd/bootstrap.go", "internal/storage/dolt/store.go", "internal/doltserver/servermode.go"} {
		if _, err := os.Stat(filepath.Join(repoRoot, file)); err != nil {
			t.Fatalf("the census reads %s from the repository: %v", file, err)
		}
	}
	for _, v := range censusViolations(t, repoRoot, "cmd/bd", "internal") {
		t.Errorf("%s formats a clone-controlled value without the remedy printer (internal/remedy)", v)
	}
}

// styledErrorSinks are the calls whose text bd's error printers print and
// spell by remedy.PrintedText: an error's constructors, cmd/bd's error
// printers, and alreadyInitialized, which builds bd init's
// already-initialized refusals.
var styledErrorSinks = map[string]bool{
	"fmt.Errorf": true, "errors.New": true, "alreadyInitialized": true,
	"HandleError": true, "HandleErrorRespectJSON": true, "HandleErrorWithHint": true,
	"HandleErrorWithHintRespectJSON": true, "HandleProxyCapabilityError": true, "WarnError": true,
}

// isStyleCall reports whether call styles text with terminal escapes: a
// ui.Render* helper or a lipgloss style's Render method.
func isStyleCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	if x, ok := sel.X.(*ast.Ident); ok && x.Name == "ui" && strings.HasPrefix(sel.Sel.Name, "Render") {
		return true
	}
	return sel.Sel.Name == "Render"
}

// TestNoStyledTextInErrors fails when a ui.Render* or Style.Render call is an
// argument, directly or inside a nested call such as fmt.Sprintf, of an error
// constructor or one of bd's error printers. Those printers spell every
// terminal escape in the message (remedy.PrintedText), so styled text there
// prints as literal escape text (\x1b[38;2;...m) on a color terminal rather
// than as color. It does not follow text built into a variable or a
// strings.Builder first; the refusals built that way (initGuardServerMessage,
// bootstrapMissingServerDBRefusal) carry no styling.
func TestNoStyledTextInErrors(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	var found []string
	for _, root := range []string{"cmd/bd", "internal"} {
		err := filepath.WalkDir(filepath.Join(repoRoot, root), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if name := d.Name(); name == "testdata" || name == "node_modules" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(repoRoot, path)
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || !styledErrorSinks[censusCallName(call)] {
					return true
				}
				for _, arg := range call.Args {
					ast.Inspect(arg, func(m ast.Node) bool {
						if _, ok := m.(*ast.FuncLit); ok {
							return false
						}
						if inner, ok := m.(*ast.CallExpr); ok && isStyleCall(inner) {
							found = append(found, fmt.Sprintf("%s:%d", filepath.ToSlash(rel), fset.Position(inner.Pos()).Line))
						}
						return true
					})
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, site := range found {
		t.Errorf("%s styles text inside an error's message; bd's error printers would print its escapes as text", site)
	}
}
