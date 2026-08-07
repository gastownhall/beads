package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/steveyegge/beads/internal/atomicfile"
	"golang.org/x/sys/unix"
)

const (
	// One exhaustive census is currently much smaller than this. The fixed
	// ceiling leaves growth headroom without allowing a hostile lane to make
	// trusted promotion consume unbounded memory.
	maxPromotedEvidenceBytes            = 128 << 20
	maxEvidencePromotionDiagnosticBytes = 4 << 10
)

type evidencePromotionDiagnostic struct {
	SchemaVersion int    `json:"schema_version"`
	Status        string `json:"status"`
	Reason        string `json:"reason"`
}

type evidencePromotionRejection struct {
	reason string
	err    error
}

func (rejection *evidencePromotionRejection) Error() string {
	return fmt.Sprintf("evidence promotion rejected (%s): %v", rejection.reason, rejection.err)
}

func (rejection *evidencePromotionRejection) Unwrap() error {
	return rejection.err
}

func rejectEvidence(reason string, err error) error {
	if err == nil {
		err = errors.New(reason)
	}
	return &evidencePromotionRejection{reason: reason, err: err}
}

func evidencePromotionRejectionReason(err error) string {
	var rejection *evidencePromotionRejection
	if errors.As(err, &rejection) {
		return rejection.reason
	}
	return ""
}

// promoteEvidence is the only boundary from a historical generator's raw
// evidence mount into the trusted output directory. It only promotes bytes
// that have already satisfied the pinned catalog's complete semantic census
// and route-topology checks.
func promoteEvidence(catalogPath, source, destination, diagnostic string) error {
	if samePathArgument(catalogPath, source) || samePathArgument(catalogPath, destination) || samePathArgument(catalogPath, diagnostic) ||
		samePathArgument(source, destination) || samePathArgument(source, diagnostic) || samePathArgument(destination, diagnostic) {
		return errors.New("evidence promotion paths must be distinct")
	}
	if err := removePromotionPath(diagnostic); err != nil {
		return fmt.Errorf("remove stale evidence promotion diagnostic: %w", err)
	}
	if err := removePromotionPath(destination); err != nil {
		return rejectEvidencePromotion(destination, diagnostic, rejectEvidence("trusted-output-cleanup-failed", err))
	}

	raw, err := readEvidenceForPromotion(source)
	if err != nil {
		return rejectEvidencePromotion(destination, diagnostic, err)
	}
	result, _, err := readValidCensusBytes(catalogPath, raw, true)
	if err != nil {
		return rejectEvidencePromotion(destination, diagnostic, rejectEvidence("census-invalid", errors.New("census validation failed")))
	}
	if _, err := routeManifestForValidatedCensus(result); err != nil {
		return rejectEvidencePromotion(destination, diagnostic, rejectEvidence("census-invalid", errors.New("census routing failed")))
	}
	if err := atomicfile.WriteFile(destination, raw, 0o644); err != nil { //nolint:gosec // destination is the trusted container's explicit output path.
		return rejectEvidencePromotion(destination, diagnostic, rejectEvidence("trusted-output-write-failed", err))
	}
	info, err := os.Lstat(destination)
	if err != nil {
		return rejectEvidencePromotion(destination, diagnostic, rejectEvidence("trusted-output-inspection-failed", err))
	}
	if !info.Mode().IsRegular() || info.Size() != int64(len(raw)) {
		return rejectEvidencePromotion(destination, diagnostic, rejectEvidence("trusted-output-invalid", errors.New("atomic promotion did not produce the expected regular file")))
	}
	return nil
}

func readEvidenceForPromotion(path string) ([]byte, error) {
	return readEvidenceForPromotionWithHook(path, nil)
}

func readEvidenceForPromotionWithHook(path string, afterRead func() error) (raw []byte, err error) {
	initialInfo, err := os.Lstat(path)
	if err != nil {
		return nil, rejectEvidence("source-inspection-failed", err)
	}
	if !initialInfo.Mode().IsRegular() {
		return nil, rejectEvidence("source-not-regular", errors.New("raw evidence is not a regular file"))
	}
	if initialInfo.Size() == 0 {
		return nil, rejectEvidence("source-empty", errors.New("raw evidence is empty"))
	}
	if initialInfo.Size() > maxPromotedEvidenceBytes {
		return nil, rejectEvidence("source-too-large", fmt.Errorf("raw evidence exceeds %d bytes", maxPromotedEvidenceBytes))
	}

	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0) //nolint:gosec // path is the preflighted raw-evidence path; no-follow and nonblocking flags close the type-swap race.
	if err != nil {
		return nil, rejectEvidence("source-open-failed", err)
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		return nil, errors.Join(rejectEvidence("source-open-failed", errors.New("convert evidence descriptor")), unix.Close(fd))
	}
	defer func() {
		if file != nil {
			if closeErr := file.Close(); closeErr != nil {
				err = errors.Join(err, rejectEvidence("source-close-failed", closeErr))
			}
		}
	}()

	openedInfo, err := file.Stat()
	if err != nil {
		return nil, rejectEvidence("source-stat-failed", err)
	}
	if !openedInfo.Mode().IsRegular() {
		return nil, rejectEvidence("source-not-regular", errors.New("opened evidence is not a regular file"))
	}
	if !os.SameFile(initialInfo, openedInfo) {
		return nil, rejectEvidence("source-identity-changed", errors.New("raw evidence changed before open"))
	}
	if openedInfo.Size() == 0 {
		return nil, rejectEvidence("source-empty", errors.New("opened evidence is empty"))
	}
	if openedInfo.Size() > maxPromotedEvidenceBytes {
		return nil, rejectEvidence("source-too-large", fmt.Errorf("opened evidence exceeds %d bytes", maxPromotedEvidenceBytes))
	}
	if err := verifyEvidencePathIdentity(path, openedInfo); err != nil {
		return nil, err
	}

	raw, err = io.ReadAll(io.LimitReader(file, maxPromotedEvidenceBytes+1))
	if err != nil {
		return nil, rejectEvidence("source-read-failed", err)
	}
	if len(raw) == 0 {
		return nil, rejectEvidence("source-empty", errors.New("raw evidence became empty while reading"))
	}
	if int64(len(raw)) > maxPromotedEvidenceBytes {
		return nil, rejectEvidence("source-too-large", fmt.Errorf("raw evidence exceeds %d bytes", maxPromotedEvidenceBytes))
	}
	if int64(len(raw)) != openedInfo.Size() {
		return nil, rejectEvidence("source-size-changed", errors.New("raw evidence size changed while reading"))
	}
	if afterRead != nil {
		if err := afterRead(); err != nil {
			return nil, rejectEvidence("source-identity-check-failed", err)
		}
	}
	if err := verifyEvidencePathIdentity(path, openedInfo); err != nil {
		return nil, err
	}
	if closeErr := file.Close(); closeErr != nil {
		file = nil
		return nil, rejectEvidence("source-close-failed", closeErr)
	}
	file = nil
	return raw, nil
}

func verifyEvidencePathIdentity(path string, openedInfo os.FileInfo) error {
	pathInfo, err := os.Lstat(path)
	if err != nil {
		return rejectEvidence("source-identity-changed", err)
	}
	if !pathInfo.Mode().IsRegular() || !os.SameFile(openedInfo, pathInfo) {
		return rejectEvidence("source-identity-changed", errors.New("raw evidence path no longer identifies the opened regular file"))
	}
	return nil
}

func rejectEvidencePromotion(destination, diagnostic string, cause error) error {
	reason := evidencePromotionRejectionReason(cause)
	if reason == "" {
		reason = "internal-error"
	}
	removeErr := removePromotionPath(destination)
	if removeErr != nil {
		removeErr = fmt.Errorf("remove rejected trusted output: %w", removeErr)
	}
	diagnosticErr := writeEvidencePromotionDiagnostic(diagnostic, reason)
	if diagnosticErr != nil {
		diagnosticErr = fmt.Errorf("write evidence promotion diagnostic: %w", diagnosticErr)
	}
	return errors.Join(cause, removeErr, diagnosticErr)
}

func writeEvidencePromotionDiagnostic(path, reason string) error {
	raw, err := json.Marshal(evidencePromotionDiagnostic{
		SchemaVersion: 1,
		Status:        "rejected",
		Reason:        reason,
	})
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if len(raw) > maxEvidencePromotionDiagnosticBytes {
		return errors.New("evidence promotion diagnostic exceeds fixed size limit")
	}
	return atomicfile.WriteFile(path, raw, 0o644) //nolint:gosec // path is the trusted container's explicit diagnostic output.
}

func removePromotionPath(path string) error {
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func samePathArgument(first, second string) bool {
	firstAbs, firstErr := filepath.Abs(first)
	secondAbs, secondErr := filepath.Abs(second)
	return firstErr == nil && secondErr == nil && firstAbs == secondAbs
}
