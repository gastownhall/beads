//go:build embeddeddolt

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/gastownhall/beads/internal/storage/embeddeddolt"
)

func main() {
	dir := flag.String("dir", "", "path to .beads directory")
	database := flag.String("database", "beads", "database name")
	branch := flag.String("branch", "main", "branch name")
	flag.Parse()

	if *dir == "" {
		fmt.Fprintln(os.Stderr, "error: --dir is required")
		flag.Usage()
		os.Exit(1)
	}

	absDir, err := filepath.Abs(*dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: resolving path: %v\n", err)
		os.Exit(1)
	}

	ctx := context.Background()
	store, err := embeddeddolt.New(ctx, absDir, *database, *branch)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	store.Close()
	fmt.Println("ok")
}
