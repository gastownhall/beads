//go:build ci_tools

package main

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
)

type listedPackage struct {
	Dir          string
	TestGoFiles  []string
	XTestGoFiles []string
}

var topLevelTestName = regexp.MustCompile(`^Test[A-Za-z0-9_]*$`)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintf(os.Stderr, "usage: %s <go package>\n", os.Args[0])
		os.Exit(2)
	}

	pkg, err := listPackage(os.Args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "go list: %v\n", err)
		os.Exit(1)
	}

	names, err := testNames(pkg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "list tests: %v\n", err)
		os.Exit(1)
	}

	for _, name := range names {
		fmt.Println(name)
	}
}

func listPackage(pattern string) (listedPackage, error) {
	args := []string{"list", "-json"}
	if tags := os.Getenv("GO_TEST_SHARD_TAGS"); tags != "" {
		args = append(args, "-tags", tags)
	}
	args = append(args, pattern)

	cmd := exec.Command("go", args...)
	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return listedPackage{}, fmt.Errorf("%w: %s", err, string(exitErr.Stderr))
		}
		return listedPackage{}, err
	}

	var pkg listedPackage
	if err := json.Unmarshal(output, &pkg); err != nil {
		return listedPackage{}, err
	}
	if pkg.Dir == "" {
		return listedPackage{}, fmt.Errorf("package %q did not report a directory", pattern)
	}
	return pkg, nil
}

func testNames(pkg listedPackage) ([]string, error) {
	seen := make(map[string]struct{})
	files := append(append([]string{}, pkg.TestGoFiles...), pkg.XTestGoFiles...)
	for _, file := range files {
		path := filepath.Join(pkg.Dir, file)
		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		for _, decl := range parsed.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || fn.Name.Name == "TestMain" || !topLevelTestName.MatchString(fn.Name.Name) {
				continue
			}
			seen[fn.Name.Name] = struct{}{}
		}
	}

	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}
