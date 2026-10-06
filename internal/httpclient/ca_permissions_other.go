//go:build !unix

// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/ca_permissions_other.go@49d1df2f6)
// to OSS beads under the MIT license.

package httpclient

import (
	"fmt"
	"os"
)

// allowInsecureCAFilePermissionsEnv is the explicit opt-in required to use a
// CA file on a platform this package cannot vet ownership/writability on at
// all. This feature (host-scoped CA trust) is new, not a long-depended-on
// one migrating to a stricter default, so failing closed here by default —
// rather than silently accepting whatever ACLs happen to protect the file —
// is the safe choice: an operator who has reviewed their own platform's
// equivalent protection can set this to proceed anyway, but nobody is
// silently downgraded from the unix build's real ownership/writability
// checks without saying so.
const allowInsecureCAFilePermissionsEnv = "BEADS_ALLOW_INSECURE_CA_FILE_PERMISSIONS"

// checkCAFilePermissions has no ownership/writability check to perform at
// all on a non-unix platform: filesystem permissions there use ACLs rather
// than Unix mode bits, and there is no uid/gid ownership model this package
// knows how to interpret. Rather than silently treating "cannot check" the
// same as "checked and fine" (this package's earlier behavior), it refuses
// the CA file outright unless the operator has explicitly opted in via
// allowInsecureCAFilePermissionsEnv — a CA file this package cannot vet is
// exactly the swap-in-a-writable-location risk the unix build's checks
// exist to catch, and a silent skip would be indistinguishable from "this
// platform is just as safe", which is not a claim this package can make.
//
// It still refuses an obviously wrong path (a directory) even with the
// opt-in set.
func checkCAFilePermissions(path string) (string, os.FileInfo, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", nil, err
	}
	if info.IsDir() {
		return "", nil, fmt.Errorf("is a directory, not a PEM file")
	}
	if os.Getenv(allowInsecureCAFilePermissionsEnv) == "" {
		return "", nil, fmt.Errorf(
			"refusing to use a CA file on this platform: it has no unix-style ownership/writability check to vet %q with, and %s is not set; set it to proceed anyway once you have verified this file (and every containing directory) cannot be written by anyone other than an administrator",
			path, allowInsecureCAFilePermissionsEnv)
	}
	return path, info, nil
}

// openCAFileNoFollow is a plain open on non-unix platforms: O_NOFOLLOW is a
// unix-specific open flag with no portable equivalent this package can rely
// on here, and checkCAFilePermissions above already refuses to reach this
// point at all unless the operator explicitly opted in via
// allowInsecureCAFilePermissionsEnv, accepting that this platform's TOCTOU
// and symlink protections are best-effort at most.
func openCAFileNoFollow(real string) (*os.File, error) {
	return os.Open(real) //nolint:gosec // G304: operator-provided config path, hygiene-gated above
}
