//go:build !cgo

package issueops

func isEmbeddedDuplicateError(error) bool {
	return false
}
