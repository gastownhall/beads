// Command unsupportedgen produces a backend's typed-unsupported shell: for each target
// interface (storage.DoltStorage, storage.Transaction) it emits a concrete
// struct whose every method returns *storage.ErrUnsupported. A backend embeds
// the generated shell alongside its real store (e.g. *sqlkit.Store) so any
// method the backend has not really overridden fails loudly and typed, never
// with a nil-pointer panic.
//
// It is stdlib-only by design (go/parser + go/ast + go/printer + go/format):
// golang.org/x/tools is only an indirect dep and go.mod carries a pre-existing
// cosmetic edit that must not churn. This works because every interface
// DoltStorage/Transaction embed is defined in package internal/storage itself,
// so the AST flatten resolves all embeds locally.
//
// Invoked via `//go:generate go run ../unsupportedgen -pkg <backend> -src .. ...`
// from a backend package dir (e.g. internal/storage/postgres), whose CWD makes
// the storage package ".." — hence -src "..".
package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/printer"
	"go/token"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type target struct {
	iface    string // interface name in package storage
	typeName string // generated concrete struct name
	opPrefix string // prefix for the Op string, e.g. "Transaction."
}

var targets = []target{
	{iface: "DoltStorage", typeName: "unsupportedDoltStorage", opPrefix: ""},
	{iface: "Transaction", typeName: "unsupportedTransaction", opPrefix: "Transaction."},
}

const (
	// defaultSrcDir is the storage package dir relative to the go:generate CWD
	// (the backend package dir); defaultOutFile is where the shell is written.
	defaultSrcDir  = ".."
	defaultOutFile = "unsupported_gen.go"
	storagePath    = "github.com/steveyegge/beads/internal/storage"
)

func main() {
	log.SetFlags(0)
	cfg, err := parseFlags(os.Args[1:])
	if err != nil {
		log.Fatalf("gen: %v", err)
	}
	if err := runConfig(cfg); err != nil {
		log.Fatalf("gen: %v", err)
	}
}

// genConfig is the parameterized generation request. Its zero-flag defaults
// (see defaultConfig) reproduce a backend's committed shell byte-for-byte; the
// flags exist so the same generator can emit a partial shell for another
// package (e.g. postgres) that embeds a real store alongside the generated one.
type genConfig struct {
	pkg   string          // -pkg  : package clause of the generated file
	src   string          // -src  : dir of the storage package to parse
	out   string          // -out  : output file path
	types []string        // -type : which target interfaces to emit (table order, not flag order)
	skip  map[string]bool // -skip : Op strings (opPrefix+Method) to omit
}

func defaultConfig() genConfig {
	return genConfig{
		pkg:   "unsupported",
		src:   defaultSrcDir,
		out:   defaultOutFile,
		types: nil, // nil => all targets, in var targets order
		skip:  nil, // nil => skip nothing
	}
}

// parseFlags builds a genConfig from CLI args. Every flag defaults to the
// byte-identity-preserving value, so `go run ./gen` with no flags is exactly
// the historical invocation.
func parseFlags(args []string) (genConfig, error) {
	def := defaultConfig()
	fs := flag.NewFlagSet("gen", flag.ContinueOnError)
	pkg := fs.String("pkg", def.pkg, "package clause of the generated file")
	src := fs.String("src", def.src, "directory of the storage package to parse")
	out := fs.String("out", def.out, "output file path")
	typeList := fs.String("type", "", "comma-separated target interface names to emit (empty = all)")
	skipList := fs.String("skip", "", "comma-separated Op strings (opPrefix+Method) to omit")
	if err := fs.Parse(args); err != nil {
		return genConfig{}, err
	}
	cfg := def
	cfg.pkg = *pkg
	cfg.src = *src
	cfg.out = *out
	cfg.types = splitList(*typeList)
	if entries := splitList(*skipList); len(entries) > 0 {
		cfg.skip = make(map[string]bool, len(entries))
		for _, e := range entries {
			cfg.skip[e] = true
		}
	}
	return cfg, nil
}

// splitList splits a comma-separated flag value, trimming blanks. An empty or
// all-whitespace value yields a nil slice (the "unset" sentinel).
func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

type pkgInfo struct {
	fset       *token.FileSet
	interfaces map[string]*ast.InterfaceType // package-level interface types by name
	typeNames  map[string]bool               // all package-level type names
	imports    map[string]string             // local name -> import path
}

// runConfig is the parameterized generator. At defaultConfig() values it emits
// a backend's committed shell byte-for-byte; -pkg/-type/-skip let it emit a
// partial shell for another package (e.g. postgres) that embeds a real store
// alongside the generated one.
func runConfig(cfg genConfig) error {
	info, err := parsePackage(cfg.src)
	if err != nil {
		return err
	}

	selected, err := selectTargets(cfg.types)
	if err != nil {
		return err
	}

	var body bytes.Buffer
	// Track selector qualifiers actually used across all emitted signatures so
	// we can emit exactly the imports needed.
	usedQualifiers := map[string]bool{}
	// Track which -skip entries actually matched an emitted method, for strict
	// post-emission validation.
	skipMatched := map[string]bool{}

	for _, tg := range selected {
		iface, ok := info.interfaces[tg.iface]
		if !ok {
			return fmt.Errorf("interface %q not found in package storage", tg.iface)
		}
		methods, err := flatten(info, iface, tg.iface)
		if err != nil {
			return err
		}
		if err := emitTarget(&body, info, tg, methods, usedQualifiers, cfg.skip, skipMatched); err != nil {
			return err
		}
	}

	// Strict skip validation: a -skip entry that matched no emitted method is a
	// typo or a drift tripwire (method removed from the interface). Fail loudly.
	if unmatched := unmatchedSkips(cfg.skip, skipMatched); len(unmatched) > 0 {
		return fmt.Errorf("skip entries matched no emitted method: %s", strings.Join(unmatched, ", "))
	}

	var out bytes.Buffer
	fmt.Fprintf(&out, "// Code generated by gen (%s typed-unsupported shell); DO NOT EDIT.\n\n", cfg.pkg) //nolint:gosec // generator emits trusted package names into generated source
	fmt.Fprintf(&out, "package %s\n\n", cfg.pkg)                                                          //nolint:gosec // generator emits trusted package names into generated source
	writeImports(&out, info, usedQualifiers)
	out.Write(body.Bytes())

	formatted, err := format.Source(out.Bytes())
	if err != nil {
		return fmt.Errorf("gofmt generated source: %w\n----\n%s", err, out.String())
	}
	// #nosec G306 -- generated Go source must be world-readable like the rest of the tree
	if err := os.WriteFile(cfg.out, formatted, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", cfg.out, err)
	}
	return nil
}

// selectTargets returns the targets to emit. An empty names slice selects all
// targets; otherwise each name is validated against the targets table and the
// result is filtered in table order (not flag order) so output is deterministic
// regardless of -type ordering. An unknown name is a fatal error.
func selectTargets(names []string) ([]target, error) {
	if len(names) == 0 {
		return targets, nil
	}
	known := map[string]bool{}
	for _, tg := range targets {
		known[tg.iface] = true
	}
	want := map[string]bool{}
	for _, n := range names {
		if !known[n] {
			return nil, fmt.Errorf("unknown -type %q (known: %s)", n, knownTargetNames())
		}
		want[n] = true
	}
	var selected []target
	for _, tg := range targets {
		if want[tg.iface] {
			selected = append(selected, tg)
		}
	}
	return selected, nil
}

func knownTargetNames() string {
	names := make([]string, 0, len(targets))
	for _, tg := range targets {
		names = append(names, tg.iface)
	}
	return strings.Join(names, ", ")
}

// unmatchedSkips returns the sorted -skip entries that never matched an emitted
// method (a nil skip set yields a nil result).
func unmatchedSkips(skip, matched map[string]bool) []string {
	var unmatched []string
	for k := range skip {
		if !matched[k] {
			unmatched = append(unmatched, k)
		}
	}
	sort.Strings(unmatched)
	return unmatched
}

func parsePackage(dir string) (*pkgInfo, error) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		return nil, fmt.Errorf("parse dir %s: %w", dir, err)
	}
	pkg, ok := pkgs["storage"]
	if !ok {
		return nil, fmt.Errorf("package storage not found in %s", dir)
	}

	info := &pkgInfo{
		fset:       fset,
		interfaces: map[string]*ast.InterfaceType{},
		typeNames:  map[string]bool{},
		imports:    map[string]string{},
	}

	// Deterministic file order.
	names := make([]string, 0, len(pkg.Files))
	for name := range pkg.Files {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		file := pkg.Files[name]
		for _, imp := range file.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			local := ""
			if imp.Name != nil {
				local = imp.Name.Name
			} else {
				local = filepath.Base(path)
			}
			if local == "_" || local == "." {
				continue
			}
			info.imports[local] = path
		}
		for _, decl := range file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				info.typeNames[ts.Name.Name] = true
				if it, ok := ts.Type.(*ast.InterfaceType); ok {
					info.interfaces[ts.Name.Name] = it
				}
			}
		}
	}
	return info, nil
}

type method struct {
	name    string
	typ     *ast.FuncType
	comment string // optional NOTE comment for no-error methods
}

// flatten walks an interface, recursing into embedded local interfaces, and
// returns the deduped method set in declaration order (first-wins, which is
// correct because Go requires identical signatures for duplicate embedded
// method names).
func flatten(info *pkgInfo, iface *ast.InterfaceType, ifaceName string) ([]method, error) {
	var out []method
	seen := map[string]bool{}
	var walk func(it *ast.InterfaceType, owner string) error
	walk = func(it *ast.InterfaceType, owner string) error {
		if it.Methods == nil {
			return nil
		}
		for _, field := range it.Methods.List {
			if ft, ok := field.Type.(*ast.FuncType); ok {
				// Named method.
				if len(field.Names) != 1 {
					return fmt.Errorf("%s: expected exactly one name per method field, got %d", owner, len(field.Names))
				}
				name := field.Names[0].Name
				if seen[name] {
					continue
				}
				seen[name] = true
				out = append(out, method{name: name, typ: ft})
				continue
			}
			// Embedded interface: must be a local *ast.Ident naming another
			// package-level interface in package storage.
			id, ok := field.Type.(*ast.Ident)
			if !ok {
				return fmt.Errorf("%s: embedded field is not a local identifier (kind %T); the stdlib generator only handles same-package embeds", owner, field.Type)
			}
			embedded, ok := info.interfaces[id.Name]
			if !ok {
				return fmt.Errorf("%s: embedded interface %q not found in package storage", owner, id.Name)
			}
			if err := walk(embedded, id.Name); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(iface, ifaceName); err != nil {
		return nil, err
	}
	return out, nil
}

func emitTarget(buf *bytes.Buffer, info *pkgInfo, tg target, methods []method, usedQualifiers, skip, skipMatched map[string]bool) error {
	fmt.Fprintf(buf, "// %s is the generated typed-unsupported shell for storage.%s.\n", tg.typeName, tg.iface) //nolint:gosec // generator emits trusted type/interface names into generated source
	fmt.Fprintf(buf, "// Every method returns *storage.ErrUnsupported; embed it and override the\n")
	fmt.Fprintf(buf, "// real slice. DO NOT hand-edit — regenerate with `go generate ./...`.\n")
	fmt.Fprintf(buf, "type %s struct{}\n\n", tg.typeName) //nolint:gosec // generator emits trusted type names into generated source

	skipped := 0
	for _, m := range methods {
		op := tg.opPrefix + m.name
		// A skipped method is simply not emitted (the embedding concrete store
		// implements it itself); no placeholder. Skipping happens before rewrite
		// so its qualifiers never enter the import set.
		if skip[op] {
			skipMatched[op] = true
			skipped++
			continue
		}
		rewritten, err := rewriteFuncType(info, m.typ, usedQualifiers)
		if err != nil {
			return fmt.Errorf("%s.%s: %w", tg.iface, m.name, err)
		}
		hasErr := lastResultIsError(rewritten)
		params := renderFieldList(info, rewritten.Params, true /*blankNames*/, false /*named*/)
		var results string
		if rewritten.Results == nil || len(rewritten.Results.List) == 0 {
			results = ""
		} else {
			results = renderFieldList(info, rewritten.Results, false, true /*named results*/)
		}

		fmt.Fprintf(buf, "func (%s) %s(%s)", tg.typeName, m.name, params) //nolint:gosec // generator emits trusted type/method names into generated source
		if results != "" {
			// Named results are always parenthesized (even a single one), which
			// is required because we give it a name (_ or err).
			fmt.Fprintf(buf, " (%s)", results) //nolint:gosec // generator emits trusted result names into generated source
		}
		buf.WriteString(" {\n")
		if hasErr {
			fmt.Fprintf(buf, "\terr = errUnsupported(%q)\n", op) //nolint:gosec // generator emits trusted operation names into generated source
			buf.WriteString("\treturn\n")
		} else {
			buf.WriteString("\t// NOTE: no error channel on this signature; returns the zero value.\n")
			buf.WriteString("\treturn\n")
		}
		buf.WriteString("}\n\n")
	}

	if skipped == 0 {
		// Full shell: the compile-time completeness assertion, exactly as before.
		fmt.Fprintf(buf, "var _ storage.%s = %s{}\n\n", tg.iface, tg.typeName) //nolint:gosec // generator emits trusted interface/type names into generated source
	} else {
		// Partial shell: it cannot satisfy the interface alone, so the assertion
		// moves to the embedding composite's `var _ storage.<iface> = (*Store)(nil)`,
		// which proves union coverage (real methods + these stubs = full interface).
		fmt.Fprintf(buf, "// NOTE: partial shell (%d methods skipped); the embedding composite must assert storage.%s itself.\n\n", skipped, tg.iface) //nolint:gosec // generator emits trusted interface names into generated source
	}
	return nil
}

func lastResultIsError(ft *ast.FuncType) bool {
	if ft.Results == nil || len(ft.Results.List) == 0 {
		return false
	}
	last := ft.Results.List[len(ft.Results.List)-1]
	id, ok := last.Type.(*ast.Ident)
	return ok && id.Name == "error"
}

// renderFieldList renders params or results. For params (blankNames) every
// param name is "_". For named results, each result gets a blanked-or-named
// identifier: the trailing error result is named "err" (so the body can assign
// it); all others are "_".
func renderFieldList(info *pkgInfo, fl *ast.FieldList, blankNames, named bool) string {
	if fl == nil {
		return ""
	}
	var parts []string
	// Flatten grouped fields into one entry per type occurrence.
	type entry struct {
		typ ast.Expr
	}
	var entries []entry
	for _, f := range fl.List {
		count := len(f.Names)
		if count == 0 {
			count = 1
		}
		for i := 0; i < count; i++ {
			entries = append(entries, entry{typ: f.Type})
		}
	}

	if blankNames {
		for _, e := range entries {
			parts = append(parts, "_ "+exprString(info, e.typ))
		}
		return strings.Join(parts, ", ")
	}

	// Named results.
	n := len(entries)
	for i, e := range entries {
		name := "_"
		if named && i == n-1 {
			if id, ok := e.typ.(*ast.Ident); ok && id.Name == "error" {
				name = "err"
			}
		}
		parts = append(parts, name+" "+exprString(info, e.typ))
	}
	return strings.Join(parts, ", ")
}

// rewriteFuncType returns a copy of ft with every local package-level type name
// (in type position) qualified as storage.<Name>. Selector expressions
// (types.Issue) are left untouched.
func rewriteFuncType(info *pkgInfo, ft *ast.FuncType, usedQualifiers map[string]bool) (*ast.FuncType, error) {
	out := &ast.FuncType{}
	var err error
	out.Params, err = rewriteFieldList(info, ft.Params, usedQualifiers)
	if err != nil {
		return nil, err
	}
	out.Results, err = rewriteFieldList(info, ft.Results, usedQualifiers)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func rewriteFieldList(info *pkgInfo, fl *ast.FieldList, usedQualifiers map[string]bool) (*ast.FieldList, error) {
	if fl == nil {
		return nil, nil
	}
	out := &ast.FieldList{}
	for _, f := range fl.List {
		nt, err := rewriteType(info, f.Type, usedQualifiers)
		if err != nil {
			return nil, err
		}
		out.List = append(out.List, &ast.Field{Names: f.Names, Type: nt})
	}
	return out, nil
}

// rewriteType deep-rewrites a type expression, qualifying local type idents and
// recording selector qualifiers actually used.
func rewriteType(info *pkgInfo, e ast.Expr, usedQualifiers map[string]bool) (ast.Expr, error) {
	switch t := e.(type) {
	case *ast.Ident:
		if info.typeNames[t.Name] {
			usedQualifiers["storage"] = true
			return &ast.SelectorExpr{X: ast.NewIdent("storage"), Sel: ast.NewIdent(t.Name)}, nil
		}
		return t, nil // builtin (error, bool, int64, string, ...) or predeclared
	case *ast.SelectorExpr:
		// e.g. types.Issue — do NOT descend into X; record the qualifier.
		if id, ok := t.X.(*ast.Ident); ok {
			usedQualifiers[id.Name] = true
		}
		return t, nil
	case *ast.StarExpr:
		x, err := rewriteType(info, t.X, usedQualifiers)
		if err != nil {
			return nil, err
		}
		return &ast.StarExpr{X: x}, nil
	case *ast.ArrayType:
		elt, err := rewriteType(info, t.Elt, usedQualifiers)
		if err != nil {
			return nil, err
		}
		return &ast.ArrayType{Len: t.Len, Elt: elt}, nil
	case *ast.MapType:
		k, err := rewriteType(info, t.Key, usedQualifiers)
		if err != nil {
			return nil, err
		}
		v, err := rewriteType(info, t.Value, usedQualifiers)
		if err != nil {
			return nil, err
		}
		return &ast.MapType{Key: k, Value: v}, nil
	case *ast.Ellipsis:
		elt, err := rewriteType(info, t.Elt, usedQualifiers)
		if err != nil {
			return nil, err
		}
		return &ast.Ellipsis{Elt: elt}, nil
	case *ast.FuncType:
		return rewriteFuncType(info, t, usedQualifiers)
	case *ast.ChanType:
		val, err := rewriteType(info, t.Value, usedQualifiers)
		if err != nil {
			return nil, err
		}
		return &ast.ChanType{Dir: t.Dir, Value: val}, nil
	case *ast.ParenExpr:
		x, err := rewriteType(info, t.X, usedQualifiers)
		if err != nil {
			return nil, err
		}
		return &ast.ParenExpr{X: x}, nil
	case *ast.IndexExpr:
		// Generic instantiation with one type arg, e.g. Iter[types.Issue].
		x, err := rewriteType(info, t.X, usedQualifiers)
		if err != nil {
			return nil, err
		}
		idx, err := rewriteType(info, t.Index, usedQualifiers)
		if err != nil {
			return nil, err
		}
		return &ast.IndexExpr{X: x, Index: idx}, nil
	case *ast.IndexListExpr:
		x, err := rewriteType(info, t.X, usedQualifiers)
		if err != nil {
			return nil, err
		}
		var indices []ast.Expr
		for _, i := range t.Indices {
			ni, err := rewriteType(info, i, usedQualifiers)
			if err != nil {
				return nil, err
			}
			indices = append(indices, ni)
		}
		return &ast.IndexListExpr{X: x, Indices: indices}, nil
	case *ast.InterfaceType:
		// Anonymous interface (e.g. interface{}); leave as-is.
		return t, nil
	default:
		return nil, fmt.Errorf("unhandled type node %T in signature", e)
	}
}

func exprString(info *pkgInfo, e ast.Expr) string {
	var buf bytes.Buffer
	cfg := printer.Config{Mode: printer.RawFormat}
	if err := cfg.Fprint(&buf, info.fset, e); err != nil {
		return fmt.Sprintf("/*print error: %v*/", err)
	}
	return buf.String()
}

func writeImports(out *bytes.Buffer, info *pkgInfo, usedQualifiers map[string]bool) {
	// Resolve qualifiers to paths: storage is always the seam type's home;
	// others come from the parsed package's import set.
	paths := map[string]string{}
	for q := range usedQualifiers {
		if q == "storage" {
			paths[storagePath] = storagePath
			continue
		}
		p, ok := info.imports[q]
		if !ok {
			// Should never happen: every qualifier came from a selector whose
			// package is imported by the storage package files we parsed.
			log.Fatalf("gen: unknown import qualifier %q used in a signature", q)
		}
		paths[p] = p
	}
	if len(paths) == 0 {
		return
	}
	list := make([]string, 0, len(paths))
	for p := range paths {
		list = append(list, p)
	}
	sort.Strings(list)
	out.WriteString("import (\n")
	for _, p := range list {
		fmt.Fprintf(out, "\t%q\n", p) //nolint:gosec // generator emits trusted import paths into generated source
	}
	out.WriteString(")\n\n")
}
