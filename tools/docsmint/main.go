// Command docsmint post-processes bd's generic CLI documentation into the
// Mintlify site. bd itself emits only vendor-neutral Markdown (see
// `bd help --docs-root`, which writes the generic pages to build/cli-docs/);
// everything Mintlify-specific — MDX-safe comment markers, extensionless
// route links, and the CLI Reference pages array inside docs/docs.json —
// happens here, in repo tooling, so the OSS binary stays free of
// site-generator formats.
//
// Usage: go run ./tools/docsmint [repo-root]
// (scripts/generate-cli-docs.sh runs it right after bd help --docs-root.)
package main

import (
	"fmt"
	"os"
)

func main() {
	root := "."
	if len(os.Args) > 1 {
		root = os.Args[1]
	}
	if len(os.Args) > 2 {
		fmt.Fprintln(os.Stderr, "usage: docsmint [repo-root]")
		os.Exit(2)
	}
	if err := run(root); err != nil {
		fmt.Fprintln(os.Stderr, "docsmint:", err)
		os.Exit(1)
	}
}
