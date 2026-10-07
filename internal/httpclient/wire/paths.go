// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/wire/paths.go@49d1df2f6)
// to OSS beads under the MIT license.
package wire

import (
	"fmt"
	"net/url"
	"strings"
)

// The operation ids, spelled exactly as the document's operationId values and
// as internal/httpapi's Op* constants. They are redeclared here rather than
// imported because internal/httpapi is the SERVER — importing it would drag the
// storage engine into every process that only wants to talk to one.
// TestOperationIdsMatchTheServerRouteTable is the seam that keeps the two
// spellings from drifting.
const (
	OpHealth                  = "health"
	OpGetContext              = "getContext"
	OpListReadyWork           = "listReadyWork"
	OpCountReadyWork          = "countReadyWork"
	OpGetStats                = "getStats"
	OpListIssues              = "listIssues"
	OpQueryIssues             = "queryIssues"
	OpGetIssue                = "getIssue"
	OpClaimIssue              = "claimIssue"
	OpCloseIssue              = "closeIssue"
	OpReopenIssue             = "reopenIssue"
	OpUpdateIssue             = "updateIssue"
	OpSweepIssues             = "sweepIssues"
	OpDeleteIssues            = "deleteIssues"
	OpBatchCreateIssues       = "batchCreateIssues"
	OpBatchCloseIssues        = "batchCloseIssues"
	OpClaimNextIssue          = "claimNextIssue"
	OpReleaseIssue            = "releaseIssue"
	OpCountIssues             = "countIssues"
	OpCreateIssue             = "createIssue"
	OpApplyBatch              = "applyBatch"
	OpCompareAndSetMetadata   = "compareAndSetMetadata"
	OpListEvents              = "listEvents"
	OpWatchEvents             = "watchEvents"
	OpListSettings            = "listSettings"
	OpGetSetting              = "getSetting"
	OpSetSetting              = "setSetting"
	OpUnsetSetting            = "unsetSetting"
	OpListDependencies        = "listDependencies"
	OpListBlockingAnnotations = "listBlockingAnnotations"
	OpGetDependencyTree       = "getDependencyTree"
	OpCountDependencyEdges    = "countDependencyEdges"
	OpListRelatedIssues       = "listRelatedIssues"
	OpAddComment              = "addComment"
	OpListDependencyCycles    = "listDependencyCycles"
	OpAddDependencies         = "addDependencies"
	OpRemoveDependency        = "removeDependency"
	OpListMemories            = "listMemories"
	OpRememberMemory          = "rememberMemory"
	OpGetMemory               = "getMemory"
	OpForgetMemory            = "forgetMemory"
	OpBatchGetIssues          = "batchGetIssues"
)

// The paths that carry no caller-supplied segment. A path with one is built by
// the functions below, because an id has to be escaped before it can be joined.
const (
	PathHealth               = "/healthz"
	PathContext              = "/v0/beads/context"
	PathReady                = "/v0/beads/ready"
	PathReadyCount           = "/v0/beads/ready:count"
	PathStats                = "/v0/beads/stats"
	PathIssues               = "/v0/beads/issues"
	PathIssuesQuery          = "/v0/beads/issues:query"
	PathIssuesCount          = "/v0/beads/issues:count"
	PathIssuesSweep          = "/v0/beads/issues:sweep"
	PathIssuesDelete         = "/v0/beads/issues:delete"
	PathIssuesBatchCreate    = "/v0/beads/issues:batchCreate"
	PathIssuesBatchClose     = "/v0/beads/issues:batchClose"
	PathIssuesClaimNext      = "/v0/beads/issues:claimNext"
	PathIssuesBatchApply     = "/v0/beads/issues:batchApply"
	PathSettings             = "/v0/beads/config"
	PathEvents               = "/v0/beads/events"
	PathEventsWatch          = "/v0/beads/events:watch"
	PathDependencies         = "/v0/beads/dependencies"
	PathDependenciesCount    = "/v0/beads/dependencies:count"
	PathDependenciesBlocking = "/v0/beads/dependencies/blocking"
	PathDependenciesTree     = "/v0/beads/dependencies/tree"
	PathDependenciesCycles   = "/v0/beads/dependencies/cycles"
	PathDependenciesAdd      = "/v0/beads/dependencies:add"
	PathDependenciesRemove   = "/v0/beads/dependencies:remove"
	PathMemories             = "/v0/beads/memories"
)

// The custom methods that ride on the issue-detail segment. The server splits
// them back off the segment its wildcard matched (internal/httpapi/routes.go,
// customMethodTarget), so the suffix must be the LAST thing in that segment and
// its colon must be literal — which is the whole reason escapeSegment escapes a
// colon inside the id itself.
const (
	MethodClaim       = ":claim"
	MethodClose       = ":close"
	MethodReopen      = ":reopen"
	MethodRelease     = ":release"
	MethodCASMetadata = ":casMetadata"
)

// The sub-resource collections that hang off the issue-detail path. Unlike a
// custom method these are ORDINARY segments after the id, so the server routes
// them with a literal rather than through the custom-method dispatcher — but the
// id in front of them is still one wildcard-matched segment, which is why they
// go through the same escape.
const (
	SubresourceComments = "comments"
	SubresourceRelated  = "related"
)

// IssuePath is the issue-detail path: GET reads it, PATCH updates it.
func IssuePath(id string) (string, error) {
	seg, err := escapeSegment("issue id", id)
	if err != nil {
		return "", err
	}
	return PathIssues + "/" + seg, nil
}

// IssueMethodPath is the issue-detail path with a custom method appended to the
// final segment: /v0/beads/issues/{id}:claim and its two siblings.
func IssueMethodPath(id, method string) (string, error) {
	switch method {
	case MethodClaim, MethodClose, MethodReopen, MethodRelease, MethodCASMetadata:
	default:
		return "", fmt.Errorf("no custom method %q on the issue resource", method)
	}
	base, err := IssuePath(id)
	if err != nil {
		return "", err
	}
	return base + method, nil
}

// IssueCommentsPath is the comment collection an issue owns: the POST that
// appends one comment to its thread.
//
// There is no GET here on purpose. The wire publishes no comment page — the
// thread is read through `GET /v0/beads/issues/{id}?include_comments=true` — so
// a read helper on this path would name a route that does not exist.
func IssueCommentsPath(id string) (string, error) {
	return issueSubresourcePath(id, SubresourceComments)
}

// IssueRelatedPath is the neighbor read anchored on one issue.
func IssueRelatedPath(id string) (string, error) {
	return issueSubresourcePath(id, SubresourceRelated)
}

// issueSubresourcePath joins a literal collection onto the escaped issue
// segment.
//
// The escape matters MORE here than on the detail path, not less, and it is
// worth saying why the sub-resource does not make it safe: a slash in the id
// would push the literal onto a third segment, so `/comments` would no longer be
// the collection this request named. The server's wildcard matches exactly one
// segment, so that request 404s rather than writing to another anchor's thread —
// but only because the path shape is what it is, and escapeSegment is what keeps
// it that way for every id.
func issueSubresourcePath(id, collection string) (string, error) {
	base, err := IssuePath(id)
	if err != nil {
		return "", err
	}
	return base + "/" + collection, nil
}

// SettingPath is the single-setting read path.
func SettingPath(key string) (string, error) {
	seg, err := escapeSegment("setting key", key)
	if err != nil {
		return "", err
	}
	return PathSettings + "/" + seg, nil
}

// MemoryPath is the single-memory path, shared by the read and the delete.
//
// Memory keys are the widest segment on this surface — the document allows
// spaces, dots and unicode — so this is the escape that earns its keep.
func MemoryPath(key string) (string, error) {
	seg, err := escapeSegment("memory key", key)
	if err != nil {
		return "", err
	}
	return PathMemories + "/" + seg, nil
}

// escapeSegment percent-escapes one caller-supplied path segment.
//
// url.PathEscape is the start of the job and not the end of it. Two characters
// it deliberately leaves alone are load-bearing here:
//
//	':'  is legal in a path segment, so PathEscape keeps it — but on THIS
//	     surface a trailing `:verb` is a custom method the server splits off the
//	     segment. An id ending in ":claim" would be read as a claim of a shorter
//	     id, and one containing any colon makes the split ambiguous. It travels
//	     escaped.
//	'.'  survives too, and the joins below run through path.Join, which RESOLVES
//	     "." and ".." — a segment that is one of them would climb out of its own
//	     collection. Only the whole-segment case can do that, so only it is
//	     rewritten.
//
// An empty segment is refused rather than escaped: it would join to the
// collection path and turn a read of one resource into a read of all of them.
func escapeSegment(kind, s string) (string, error) {
	if s == "" {
		return "", fmt.Errorf("empty %s", kind)
	}
	seg := strings.ReplaceAll(url.PathEscape(s), ":", "%3A")
	switch seg {
	case ".":
		seg = "%2E"
	case "..":
		seg = "%2E%2E"
	}
	return seg, nil
}
