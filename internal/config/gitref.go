package config

import (
	"fmt"
	"os"
	"strings"
)

// Dolt's git blobstore writes a marker commit to a visible "info" branch
// after every push; data can never live on that branch. The branch name is
// dolt's default unless DOLT_REMOTE_INFO_BRANCH overrides it (an empty
// override disables the marker).
const (
	DefaultDoltRemoteInfoBranch = "__dolt_remote_info__"
	DoltRemoteInfoBranchEnvVar  = "DOLT_REMOTE_INFO_BRANCH"
)

// DoltRemoteInfoRef returns the full ref of dolt's effective info branch, or
// "" when the marker is disabled. Dolt puts the name under refs/heads/
// whatever it looks like, so an override that starts with refs/ lands on
// refs/heads/refs/..., and that is the ref reserved here.
func DoltRemoteInfoRef() string {
	name := DefaultDoltRemoteInfoBranch
	if v, ok := os.LookupEnv(DoltRemoteInfoBranchEnvVar); ok {
		name = strings.TrimSpace(v)
	}
	if name == "" {
		return ""
	}
	return "refs/heads/" + name
}

// ValidateGitDataRef checks a sync.remote-ref value wherever it enters bd:
// the --ref flags and bd config set. Empty means Dolt's default ref. Anything
// else must be a full, well-formed ref name (git check-ref-format rules), so
// a bare branch name, a typo such as ref/heads/x, or a pattern such as
// refs/heads/* is refused, and so is dolt's effective info branch.
func ValidateGitDataRef(value string) error {
	ref := strings.TrimSpace(value)
	if ref == "" {
		return nil
	}
	if !strings.HasPrefix(ref, "refs/") {
		return fmt.Errorf("must be a full git ref starting with refs/ (for example refs/heads/issue-data or refs/dolt/units/team-a), got %q", value)
	}
	if err := CheckGitRefFormat(ref); err != nil {
		return fmt.Errorf("%q is not a valid git ref name: %w", value, err)
	}
	if info := DoltRemoteInfoRef(); info != "" && ref == info {
		return fmt.Errorf("%q is dolt's remote info branch and cannot hold data", value)
	}
	return nil
}

// CheckGitRefFormat applies the rules of git check-ref-format to a full ref
// name, so the value names exactly one ref: no glob or refspec characters,
// no control characters or whitespace, no "..", "@{", or empty components,
// no component starting with "." or ending in ".lock", and no trailing "/"
// or ".".
func CheckGitRefFormat(ref string) error {
	if strings.HasSuffix(ref, "/") || strings.HasSuffix(ref, ".") {
		return fmt.Errorf("must not end with / or .")
	}
	if strings.Contains(ref, "..") || strings.Contains(ref, "@{") || strings.Contains(ref, "//") {
		return fmt.Errorf("must not contain .., @{, or //")
	}
	for _, r := range ref {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(" ~^:?*[\\", r) {
			return fmt.Errorf("must not contain whitespace, control characters, or any of ~ ^ : ? * [ \\")
		}
	}
	for _, component := range strings.Split(ref, "/") {
		if strings.HasPrefix(component, ".") || strings.HasSuffix(component, ".lock") {
			return fmt.Errorf("a path component must not start with . or end with .lock")
		}
	}
	return nil
}
