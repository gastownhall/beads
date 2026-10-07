//go:build regression

package regression

// Only -tags=regression compiles this file: it marks the build as an explicit
// request for the suite (see testMainInner). Gazelle never adds it to the
// Bazel target, which runs the suite because it runs under Bazel.
func init() { builtWithRegressionTag = true }
