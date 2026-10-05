// Command unsupportedgen regenerates the typed-unsupported shell a client
// package embeds to satisfy a storage capability interface it only partially
// implements by hand.
//
// It does NOT take a hand-maintained -skip list. The whole point of this tool
// is to make the "which methods does the shell need to cover" question
// self-answering: it parses the target package's own .go sources (every file
// except _test.go files and the file it is about to overwrite) for methods
// already declared on the given -receiver, and generates a stub for every
// OTHER method of -type storage interface is not already covered. A
// hand-written method and a generated stub can therefore never collide on the
// same selector — there is nothing to keep in sync by hand, and nothing to
// drift.
//
// Written from scratch for the S3 native-program reconciliation: this is NOT
// a port of bd-enterprise's generator (which this client's lift did not carry
// over) — only the shape of the file it must reproduce (unsupported_gen.go,
// lifted with bd-enterprise's generator before this tool existed) was read to
// match its style.
//
// Usage (see internal/httpclient/unsupported.go's //go:generate line):
//
//	go run ../../storage/unsupportedgen -type DoltStorage -pkg httpclient -receiver Store -out unsupported_gen.go
package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
)

// registry maps a -type flag value to the reflected interface it names. Adding
// a type this tool can generate a shell for is one line here; there is no
// string-based reflection trick that gets around naming it, because Go cannot
// look up a type by its source name at runtime.
var registry = map[string]reflect.Type{
	"DoltStorage": reflect.TypeOf((*storage.DoltStorage)(nil)).Elem(),
}

func main() {
	typeName := flag.String("type", "", "name of the interface to shell, from the registry in this file (e.g. DoltStorage)")
	pkgName := flag.String("pkg", "", "package clause for the generated file")
	receiver := flag.String("receiver", "", "base receiver type name (without *) whose existing methods this tool must not shadow")
	scanDir := flag.String("scandir", ".", "directory holding the hand-written sources to scan for existing methods")
	out := flag.String("out", "", "output file path, relative to scandir")
	extraSkip := flag.String("skip", "", "comma-separated extra method names to exclude, beyond what the scan finds (rarely needed)")
	flag.Parse()

	if *typeName == "" || *pkgName == "" || *receiver == "" || *out == "" {
		log.Fatal("unsupportedgen: -type, -pkg, -receiver and -out are all required")
	}
	ifaceType, ok := registry[*typeName]
	if !ok {
		log.Fatalf("unsupportedgen: %q is not in this tool's registry (add it in main.go)", *typeName)
	}

	outPath := filepath.Join(*scanDir, *out)
	handWritten, err := scanHandWrittenMethods(*scanDir, *receiver, outPath)
	if err != nil {
		log.Fatalf("unsupportedgen: scanning %s: %v", *scanDir, err)
	}
	for _, name := range strings.Split(*extraSkip, ",") {
		if name = strings.TrimSpace(name); name != "" {
			handWritten[name] = true
		}
	}

	src, skipped, err := generate(*pkgName, *typeName, ifaceType, handWritten)
	if err != nil {
		log.Fatalf("unsupportedgen: %v", err)
	}
	if err := os.WriteFile(outPath, src, 0o600); err != nil {
		log.Fatalf("unsupportedgen: writing %s: %v", outPath, err)
	}
	fmt.Fprintf(os.Stderr, "unsupportedgen: wrote %s (%d methods stubbed, %d left to the hand-written set)\n",
		outPath, ifaceType.NumMethod()-skipped, skipped)
}

// scanHandWrittenMethods parses every .go file in dir except _test.go files
// and the generator's own output file, and returns the set of method names
// declared with a receiver of type recv or *recv anywhere in the package. That
// set is exactly the method names this tool must NOT shell: generating a stub
// for one would make its selector ambiguous against the hand-written one.
func scanHandWrittenMethods(dir, recv, outPath string) (map[string]bool, error) {
	absOut, err := filepath.Abs(outPath)
	if err != nil {
		return nil, err
	}
	found := map[string]bool{}
	fset := token.NewFileSet()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(dir, name)
		if absPath, err := filepath.Abs(path); err == nil && absPath == absOut {
			continue // this is the file we are about to overwrite
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || len(fn.Recv.List) == 0 {
				continue
			}
			if receiverBaseName(fn.Recv.List[0].Type) == recv {
				found[fn.Name.Name] = true
			}
		}
	}
	return found, nil
}

// receiverBaseName strips a leading "*" from a receiver type expression and
// returns its identifier, e.g. "*Store" and "Store" both yield "Store".
func receiverBaseName(expr ast.Expr) string {
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	if ident, ok := expr.(*ast.Ident); ok {
		return ident.Name
	}
	return ""
}

// qualifiedLeak matches a package-path-qualified identifier the way a generic
// instantiation's reflect.Type.Name() leaks it, e.g.
// "github.com/steveyegge/beads/internal/types.Issue". It requires at least one
// "/" so it never matches an already-short name like "context.Context" or
// "types.IssueFilter".
var qualifiedLeak = regexp.MustCompile(`[A-Za-z0-9_.\-]+(?:/[A-Za-z0-9_.\-]+)+\.[A-Za-z_][A-Za-z0-9_]*`)

// importSet collects the packages a generated file's method bodies reference,
// keyed by import path, so the output needs no hand-maintained import block.
type importSet map[string]string // import path -> local alias (last path segment)

func (s importSet) add(pkgPath string) string {
	if pkgPath == "" {
		return ""
	}
	alias := pkgPath
	if i := strings.LastIndex(pkgPath, "/"); i >= 0 {
		alias = pkgPath[i+1:]
	}
	s[pkgPath] = alias
	return alias
}

// formatType renders t as the Go syntax a stub's signature can use, collecting
// every package it names into imports as it goes.
//
// A plain reflect.Type.String() is NOT enough on its own: for a generic
// instantiation (this package's one case is storage.Iter[T]), reflect leaks
// the type argument's FULL import path into both String() and Name() rather
// than the short package name every other case gets
// ("storage.Iter[github.com/steveyegge/beads/internal/types.Issue]" instead of
// "storage.Iter[types.Issue]"). This function walks the type structurally so
// composite types are built from already-short recursive calls, and for the
// generic-name case specifically, desanitizes the leaked argument with
// qualifiedLeak before recording its package and splicing it back in.
func formatType(t reflect.Type, imports importSet) string {
	// A DEFINED type (Name() != "") with a composite underlying kind — e.g.
	// encoding/json.RawMessage, whose Kind() is Slice over its underlying
	// []byte — must be named by its declaration, not rebuilt structurally from
	// its underlying kind: "[]uint8" is a different type than json.RawMessage
	// as far as the compiler's interface-satisfaction check is concerned, even
	// though they share a representation. Only an UNNAMED composite (a literal
	// "[]string" or "map[string]int" parameter, Name()=="") falls through to
	// the structural cases below.
	if t.Name() != "" && t.PkgPath() != "" {
		return namedType(t, imports)
	}

	switch t.Kind() {
	case reflect.Ptr:
		return "*" + formatType(t.Elem(), imports)
	case reflect.Slice:
		return "[]" + formatType(t.Elem(), imports)
	case reflect.Array:
		return fmt.Sprintf("[%d]%s", t.Len(), formatType(t.Elem(), imports))
	case reflect.Map:
		return fmt.Sprintf("map[%s]%s", formatType(t.Key(), imports), formatType(t.Elem(), imports))
	case reflect.Chan:
		return "chan " + formatType(t.Elem(), imports)
	case reflect.Func:
		in := make([]string, t.NumIn())
		for i := range in {
			if t.IsVariadic() && i == len(in)-1 {
				in[i] = "..." + formatType(t.In(i).Elem(), imports)
				continue
			}
			in[i] = formatType(t.In(i), imports)
		}
		out := make([]string, t.NumOut())
		for i := range out {
			out[i] = formatType(t.Out(i), imports)
		}
		switch len(out) {
		case 0:
			return fmt.Sprintf("func(%s)", strings.Join(in, ", "))
		case 1:
			return fmt.Sprintf("func(%s) %s", strings.Join(in, ", "), out[0])
		default:
			return fmt.Sprintf("func(%s) (%s)", strings.Join(in, ", "), strings.Join(out, ", "))
		}
	}

	if t.Name() != "" {
		return t.Name() // predeclared: string, int, bool, error, ...
	}
	if t.Kind() == reflect.Interface && t.NumMethod() == 0 {
		return "interface{}"
	}
	// Not expected anywhere in storage.DoltStorage today (no bare func types or
	// anonymous structs/interfaces in its method set); fail loudly rather than
	// emit something a human has to notice is wrong later.
	log.Fatalf("unsupportedgen: unhandled anonymous type %s (kind %s) — formatType needs a case for it", t.String(), t.Kind())
	panic("unreachable")
}

// namedType renders a DEFINED type (t.Name() != "" && t.PkgPath() != "") as
// its package-qualified declaration name, desanitizing a generic
// instantiation's leaked type-argument path the same way formatType's doc
// comment describes.
func namedType(t reflect.Type, imports importSet) string {
	name := t.Name()
	if open := strings.IndexByte(name, '['); open >= 0 && strings.HasSuffix(name, "]") {
		base := name[:open]
		args := qualifiedLeak.ReplaceAllStringFunc(name[open+1:len(name)-1], func(leak string) string {
			dot := strings.LastIndex(leak, ".")
			return imports.add(leak[:dot]) + leak[dot:]
		})
		alias := imports.add(t.PkgPath())
		return fmt.Sprintf("%s.%s[%s]", alias, base, args)
	}
	alias := imports.add(t.PkgPath())
	return alias + "." + name
}

type stubMethod struct {
	name    string
	params  []string
	results []string
}

// generate produces the full source of the typed-unsupported shell: a stub for
// every method of iface not in handWritten. It returns the number of methods
// it left alone (the hand-written complement) alongside the formatted source.
func generate(pkgName, typeName string, iface reflect.Type, handWritten map[string]bool) ([]byte, int, error) {
	imports := importSet{}
	var methods []stubMethod
	skipped := 0
	for i := 0; i < iface.NumMethod(); i++ {
		m := iface.Method(i)
		if handWritten[m.Name] {
			skipped++
			continue
		}
		mt := m.Type
		sm := stubMethod{name: m.Name}
		for p := 0; p < mt.NumIn(); p++ {
			sm.params = append(sm.params, formatType(mt.In(p), imports))
		}
		for r := 0; r < mt.NumOut(); r++ {
			sm.results = append(sm.results, formatType(mt.Out(r), imports))
		}
		// errUnsupported only has an error to hand back, so a stub can only
		// speak for a method whose last (or only) result is literally an
		// error. Every method this tool has ever had to stub satisfies that;
		// a future storage.DoltStorage method that does not needs a
		// hand-written implementation (which puts it in the scanned,
		// hand-written set and this tool never sees it), not a guess here.
		if len(sm.results) == 0 || sm.results[len(sm.results)-1] != "error" {
			log.Fatalf("unsupportedgen: %s's last result is not error (results: %v) — "+
				"it cannot be shelled by errUnsupported; implement it by hand instead", m.Name, sm.results)
		}
		methods = append(methods, sm)
	}
	sort.Slice(methods, func(i, j int) bool { return methods[i].name < methods[j].name })

	var b bytes.Buffer
	fmt.Fprintf(&b, "// Code generated by unsupportedgen (internal/storage/unsupportedgen); DO NOT EDIT.\n")
	fmt.Fprintf(&b, "// Regenerate with `go generate ./...` after a storage.%s interface change.\n\n", typeName)
	fmt.Fprintf(&b, "package %s\n\n", pkgName)

	if len(imports) > 0 {
		paths := make([]string, 0, len(imports))
		for p := range imports {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		b.WriteString("import (\n")
		for _, p := range paths {
			fmt.Fprintf(&b, "\t%q\n", p)
		}
		b.WriteString(")\n\n")
	}

	shellName := "unsupported" + typeName
	fmt.Fprintf(&b, "// %s is the generated typed-unsupported shell for storage.%s.\n", shellName, typeName)
	fmt.Fprintf(&b, "// Every method returns *storage.ErrUnsupported via errUnsupported; embed it and\n")
	fmt.Fprintf(&b, "// override the real slice. DO NOT hand-edit — regenerate with `go generate ./...`.\n")
	fmt.Fprintf(&b, "type %s struct{}\n\n", shellName)

	for _, m := range methods {
		var resultDecl string
		switch len(m.results) {
		case 0:
			resultDecl = ""
		case 1:
			resultDecl = fmt.Sprintf(" (err %s)", m.results[0])
		default:
			named := make([]string, len(m.results))
			for i, r := range m.results[:len(m.results)-1] {
				named[i] = "_ " + r
			}
			named[len(named)-1] = "err " + m.results[len(m.results)-1]
			resultDecl = fmt.Sprintf(" (%s)", strings.Join(named, ", "))
		}
		params := make([]string, len(m.params))
		for i, p := range m.params {
			params[i] = "_ " + p
		}
		fmt.Fprintf(&b, "func (%s) %s(%s)%s {\n", shellName, m.name, strings.Join(params, ", "), resultDecl)
		fmt.Fprintf(&b, "\terr = errUnsupported(%q)\n", m.name)
		b.WriteString("\treturn\n}\n\n")
	}

	fmt.Fprintf(&b, "// NOTE: partial shell (%d of %d methods generated; %d left to this package's hand-written set).\n",
		len(methods), iface.NumMethod(), skipped)

	formatted, err := format.Source(b.Bytes())
	if err != nil {
		return nil, 0, fmt.Errorf("formatting generated source: %w\n--- unformatted ---\n%s", err, b.String())
	}
	return formatted, skipped, nil
}
