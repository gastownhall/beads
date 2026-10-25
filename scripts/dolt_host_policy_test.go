package scripts_test

// Policy: one place interprets a Dolt server host's spelling
// (//scripts:go_sources_test, which declares //:repo_go_srcs).
//
// bd decides whether a Dolt server host is its own (start it, trust its port
// file, count .beads/dolt as its data, clone into it, re-root a migration at
// it) only through configfile.BdRunsServerAt, and whether a host is this
// machine only through configfile.IsLocalHostString. Both derive from
// configfile.ClassifyDoltHost (internal/configfile/dolt_host.go), the only
// production code that reads a host spelling. Before it, four private host
// lists and ad hoc combinations drifted apart, each answering differently for
// LOCALHOST, 0.0.0.0, ::1 or 0:0:0:0:0:0:0:1.
//
// The scan fails on production Go code that
//   - compares a host with a loopback host literal itself: in a switch case,
//     an == or != comparison, or a strings.EqualFold/HasPrefix/HasSuffix/
//     TrimPrefix/TrimSuffix/Contains call with such a literal argument;
//   - lists two or more loopback host literals in a composite literal (a map
//     or slice of hosts);
//   - asks net.IP/netip.Addr IsLoopback() outside the bind-address checks
//     allowlisted below; or
//   - calls IsLocalHostString outside the machine-locality sites allowlisted
//     below. IsLocalHostString counts the IPv6 loopback as local, so a new
//     ownership decision that reached for it would treat a server bd does not
//     run as bd's. Such a site calls BdRunsServerAt; a genuinely new
//     machine-locality site is added here, with its reason.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
)

// doltHostClassifierFile is the one file allowed to interpret host spellings.
const doltHostClassifierFile = "internal/configfile/dolt_host.go"

// isLocalHostStringCallers are the declarations allowed to call
// configfile.IsLocalHostString, by "file:declaration". Each asks whether a
// host is this machine, not whether bd runs a Dolt server there.
var isLocalHostStringCallers = map[string]string{
	"internal/configfile/configfile.go:HostImpliesServerMode":                  "GH#3545 mode inference: a host on another machine implies server mode",
	"internal/httpclient/credential.go:NewBearerProvider":                      "HTTP plaintext-credential guard (not a Dolt host)",
	"internal/httpclient/insecure_credential_guard.go:guardInsecureCredential": "HTTP plaintext-credential guard (not a Dolt host)",
	"cmd/bd/connect.go:connectCmd":                                             "bd connect plaintext guard (not a Dolt host)",
	"cmd/bd/init_policy.go:detectInitRemoteHostConflict":                       "same question as mode inference",
	"cmd/bd/backup_auto.go:clientServerShareFilesystem":                        "whether client and server share a filesystem",
	"cmd/bd/create.go:refuseUninitializableTarget":                             "dolt.mode-empty branch repeats mode inference",
	"cmd/bd/foreign_workspace.go:targetSelectsServerMode":                      "a foreign target's server intent repeats mode inference",
}

// isLoopbackFiles may call IsLoopback(): they check the bind address of a
// listener bd itself opens, not a Dolt server host.
var isLoopbackFiles = map[string]string{
	"internal/httpapi/server.go": "bd serve bind-address policy",
	"cmd/bd/proxied_server.go":   "listener of the proxy child bd launches itself",
}

// hostListFiles may list loopback literals: they are not Dolt hosts.
var hostListFiles = map[string]string{
	"internal/ado/client.go": "Azure DevOps API URL plaintext check",
}

// isLoopbackHostLiteral reports whether e is a string literal naming a
// loopback host or bd's own address in some spelling. A literal that is empty
// once unbracketed, such as "" or the JSON empty array "[]", names no host,
// although ClassifyDoltHost reads an empty host as bd's default.
func isLoopbackHostLiteral(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	v, err := strconv.Unquote(lit.Value)
	if err != nil || strings.TrimSpace(configfile.UnbracketHost(v)) == "" {
		return "", false
	}
	return v, configfile.ClassifyDoltHost(v) != configfile.DoltHostRemote
}

// scanDoltHostPolicy returns the violations in one production Go file, rel
// being its slash-separated repository path.
func scanDoltHostPolicy(rel string, src []byte) ([]string, error) {
	if rel == doltHostClassifierFile {
		return nil, nil
	}
	// Cheap prefilter: skip files that mention no loopback spelling at all.
	// It is case-insensitive and covers every spelling isLoopbackHostLiteral
	// accepts ("127.", "::", "0:0" for the expanded IPv6 forms, "0.0.0.0",
	// "localhost"), so it never hides a file the AST pass would flag.
	s := strings.ToLower(string(src))
	if !strings.Contains(s, "127.") && !strings.Contains(s, "::") && !strings.Contains(s, "0:0") &&
		!strings.Contains(s, "0.0.0.0") && !strings.Contains(s, "localhost") &&
		!strings.Contains(s, "isloopback") && !strings.Contains(s, "islocalhoststring") {
		return nil, nil
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, rel, src, 0)
	if err != nil {
		return nil, err
	}
	var out []string
	report := func(n ast.Node, what string) {
		out = append(out, rel+":"+strconv.Itoa(fset.Position(n.Pos()).Line)+": "+what)
	}
	_, hostListOK := hostListFiles[rel]
	_, loopbackOK := isLoopbackFiles[rel]

	for _, decl := range f.Decls {
		declName := ""
		switch d := decl.(type) {
		case *ast.FuncDecl:
			declName = d.Name.Name
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				if vs, ok := spec.(*ast.ValueSpec); ok && len(vs.Names) > 0 {
					declName = vs.Names[0].Name
					break
				}
			}
		}
		ast.Inspect(decl, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.CaseClause:
				if !hostListOK && countLoopbackLiterals(n.List) >= 1 {
					report(n, "switch case lists loopback hosts; use configfile.BdRunsServerAt or IsLocalHostString")
				}
			case *ast.CompositeLit:
				var elems []ast.Expr
				for _, e := range n.Elts {
					if kv, ok := e.(*ast.KeyValueExpr); ok {
						elems = append(elems, kv.Key)
						continue
					}
					elems = append(elems, e)
				}
				if !hostListOK && countLoopbackLiterals(elems) >= 2 {
					report(n, "composite literal lists loopback hosts; use configfile.BdRunsServerAt or IsLocalHostString")
				}
			case *ast.BinaryExpr:
				if (n.Op == token.EQL || n.Op == token.NEQ) && !hostListOK &&
					countLoopbackLiterals([]ast.Expr{n.X, n.Y}) >= 1 {
					report(n, "==/!= compares a loopback host; use configfile.BdRunsServerAt or IsLocalHostString")
				}
			case *ast.CallExpr:
				switch name := calledName(n.Fun); name {
				case "IsLoopback":
					if !loopbackOK {
						report(n, "IsLoopback() on a host; use configfile.BdRunsServerAt or IsLocalHostString")
					}
				case "EqualFold", "HasPrefix", "HasSuffix", "TrimPrefix", "TrimSuffix", "Contains":
					if !hostListOK && isStringsCall(n.Fun) && hasLoopbackHostPart(n.Args) {
						report(n, "strings."+name+" on a loopback host; use configfile.BdRunsServerAt or IsLocalHostString")
					}
				case "IsLocalHostString":
					if _, ok := isLocalHostStringCallers[rel+":"+declName]; !ok {
						report(n, "IsLocalHostString in "+declName+" is not an allowlisted machine-locality site; an ownership decision uses configfile.BdRunsServerAt")
					}
				}
			}
			return true
		})
	}
	return out, nil
}

func countLoopbackLiterals(exprs []ast.Expr) int {
	seen := map[string]bool{}
	for _, e := range exprs {
		if v, ok := isLoopbackHostLiteral(e); ok {
			seen[v] = true
		}
	}
	return len(seen)
}

// hasLoopbackHostPart reports whether a strings call's arguments include a
// loopback host literal or a piece of one, such as the "127." prefix.
func hasLoopbackHostPart(args []ast.Expr) bool {
	for _, a := range args {
		if _, ok := isLoopbackHostLiteral(a); ok {
			return true
		}
		lit, ok := a.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			continue
		}
		v, err := strconv.Unquote(lit.Value)
		if err != nil {
			continue
		}
		v = strings.ToLower(strings.TrimSpace(v))
		if strings.HasPrefix(v, "127.") || strings.HasPrefix(v, "[::") || strings.HasPrefix(v, "::ffff:") {
			return true
		}
	}
	return false
}

// isStringsCall reports whether fun is a call into package strings.
func isStringsCall(fun ast.Expr) bool {
	sel, ok := fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "strings"
}

func calledName(fun ast.Expr) string {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		return f.Sel.Name
	}
	return ""
}

// TestDoltHostPolicyScanCatchesEveryShape pins what the scan catches, so a
// weakened scan fails here and not silently in the repository scan.
func TestDoltHostPolicyScanCatchesEveryShape(t *testing.T) {
	const header = "package p\n\nimport (\n\t\"net\"\n\t\"strings\"\n\n\t\"github.com/steveyegge/beads/internal/configfile\"\n)\n\nvar _ = net.IP{}\nvar _ = strings.EqualFold\nvar _ = configfile.BdRunsServerAt\n\n"
	for _, tc := range []struct {
		name, rel, body string
		want            bool
	}{
		{"switch case", "cmd/bd/p.go", "func f(h string) bool {\n\tswitch h {\n\tcase \"localhost\", \"127.0.0.1\":\n\t\treturn true\n\t}\n\treturn false\n}\n", true},
		{"== chain", "cmd/bd/p.go", "func f(h string) bool { return h == \"localhost\" || h == \"127.0.0.1\" || h == \"::1\" }\n", true},
		{"!= chain", "cmd/bd/p.go", "func f(h string) bool { return h != \"localhost\" && (h != \"::1\") }\n", true},
		{"map literal", "cmd/bd/p.go", "var hosts = map[string]bool{\"localhost\": true, \"::1\": true}\n", true},
		{"slice literal", "cmd/bd/p.go", "var hosts = []string{\"127.0.0.1\", \"[::1]\"}\n", true},
		{"IsLoopback", "cmd/bd/p.go", "func f(h string) bool { return net.ParseIP(h).IsLoopback() }\n", true},
		{"new IsLocalHostString caller", "cmd/bd/p.go", "func ownsServer(h string) bool { return configfile.IsLocalHostString(h) }\n", true},
		{"allowlisted IsLocalHostString caller", "cmd/bd/backup_auto.go", "func clientServerShareFilesystem() bool { return configfile.IsLocalHostString(\"\") }\n", false},
		{"IsLocalHostString allowlisted in another file only", "cmd/bd/p.go", "func clientServerShareFilesystem() bool { return configfile.IsLocalHostString(\"\") }\n", true},
		{"single ==", "cmd/bd/p.go", "func f(h string) bool { return configfile.UnbracketHost(h) == \"::1\" }\n", true},
		{"single !=", "cmd/bd/p.go", "func f(h string) bool { return h != \"127.0.0.1\" }\n", true},
		{"separate statements", "cmd/bd/p.go", "func f(h string) int {\n\tif h == \"localhost\" {\n\t\treturn 1\n\t}\n\tif h == \"::1\" {\n\t\treturn 2\n\t}\n\treturn 0\n}\n", true},
		{"single case", "cmd/bd/p.go", "func f(h string) bool {\n\tswitch h {\n\tcase \"::1\":\n\t\treturn true\n\t}\n\treturn false\n}\n", true},
		{"strings.EqualFold", "cmd/bd/p.go", "func f(h string) bool { return strings.EqualFold(h, \"localhost\") }\n", true},
		{"strings.HasPrefix", "cmd/bd/p.go", "func f(h string) bool { return strings.HasPrefix(h, \"127.\") }\n", true},
		{"0.0.0.0 ==", "cmd/bd/p.go", "func f(h string) bool { return h == \"0.0.0.0\" }\n", true},
		{"expanded IPv6 loopback ==", "cmd/bd/p.go", "func f(h string) bool { return h == \"0:0:0:0:0:0:0:1\" }\n", true},
		{"uppercase strings.EqualFold", "cmd/bd/p.go", "func f(h string) bool { return strings.EqualFold(h, \"LOCALHOST\") }\n", true},
		{"mixed-case single case", "cmd/bd/p.go", "func f(h string) bool {\n\tswitch h {\n\tcase \"Localhost\":\n\t\treturn true\n\t}\n\treturn false\n}\n", true},
		{"0.0.0.0 single case", "cmd/bd/p.go", "func f(h string) bool {\n\tswitch h {\n\tcase \"0.0.0.0\":\n\t\treturn true\n\t}\n\treturn false\n}\n", true},
		{"host-list file", "internal/ado/client.go", "func f(h string) bool { return h == \"localhost\" }\n", false},
		{"one literal", "cmd/bd/p.go", "var host = \"127.0.0.1\"\n", false},
		{"empty JSON array ==", "cmd/bd/p.go", "// dials localhost by default\nfunc isEmptyJSON(s string) bool { return s == \"[]\" }\n", false},
		{"empty JSON array case", "cmd/bd/p.go", "// dials localhost by default\nfunc isEmptyJSON(s string) bool {\n\tswitch s {\n\tcase \"[]\", \"{}\":\n\t\treturn true\n\t}\n\treturn false\n}\n", false},
		{"remote ==", "cmd/bd/p.go", "func f(h string) bool { return h == \"db.example.com\" }\n", false},
		{"predicate call", "cmd/bd/p.go", "func f(h string) bool { return configfile.BdRunsServerAt(h) }\n", false},
		{"remote literals", "cmd/bd/p.go", "var hosts = []string{\"db.example.com\", \"10.0.0.1\"}\n", false},
		{"classifier file", doltHostClassifierFile, "func f(h string) bool { return h == \"localhost\" || h == \"127.0.0.1\" }\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := scanDoltHostPolicy(tc.rel, []byte(header+tc.body))
			if err != nil {
				t.Fatal(err)
			}
			if (len(got) > 0) != tc.want {
				t.Errorf("scan of %s found %v, want a violation: %v", tc.rel, got, tc.want)
			}
		})
	}
}

// TestNoPrivateDoltHostInterpretation runs the scan over every production Go
// file in the repository.
func TestNoPrivateDoltHostInterpretation(t *testing.T) {
	root := sourceRepoRoot(t)
	var offenders []string
	scanned := 0
	for _, rel := range repoFiles(t, root, 100) {
		if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") ||
			strings.Contains(rel, "/testdata/") || strings.HasPrefix(rel, "testdata/") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		scanned++
		found, err := scanDoltHostPolicy(rel, src)
		if err != nil {
			continue // not this test's concern; the build reports it
		}
		offenders = append(offenders, found...)
	}
	if scanned < 500 {
		t.Fatalf("scanned only %d production Go files under %s; the listing is broken", scanned, root)
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(doltHostClassifierFile))); err != nil {
		t.Fatalf("the classifier %s is not in the scanned tree: %v", doltHostClassifierFile, err)
	}
	// Every allowlisted IsLocalHostString caller still exists, so the list
	// cannot outlive the code it excuses.
	for site := range isLocalHostStringCallers {
		file := site[:strings.LastIndex(site, ":")]
		src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
		if err != nil || !strings.Contains(string(src), "IsLocalHostString") {
			offenders = append(offenders, site+": allowlisted IsLocalHostString caller no longer calls it; drop it from isLocalHostStringCallers")
		}
	}
	sort.Strings(offenders)
	if len(offenders) > 0 {
		t.Errorf("Dolt host spellings interpreted outside %s:\n  %s", doltHostClassifierFile, strings.Join(offenders, "\n  "))
	}
}
