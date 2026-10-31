package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	storeissueops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/issueops"
)

// recordingImager answers Image from a fixed table and records every call, so
// a test can pin that the command asks for all ids in ONE call — the role's
// one transaction.
type recordingImager struct {
	images map[string]issueops.Image
	calls  [][]string
}

func (r *recordingImager) Image(_ context.Context, ids []string) ([]issueops.Image, error) {
	r.calls = append(r.calls, append([]string(nil), ids...))
	var out []issueops.Image
	var missing []string
	for _, id := range ids {
		img, ok := r.images[id]
		if !ok {
			missing = append(missing, id)
			continue
		}
		out = append(out, img)
	}
	if len(missing) > 0 {
		return nil, &issueops.NotFoundError{IDs: missing}
	}
	return out, nil
}

func imageForTest(id, table string) issueops.Image {
	img := issueops.Image{
		ID:     id,
		Table:  table,
		Row:    map[string]any{"id": id, "title": "t <" + id + ">", "closed_at": nil},
		Labels: []map[string]any{{"issue_id": id, "label": "b"}, {"issue_id": id, "label": "a"}},
	}
	img.Revision = storeissueops.ComputeImageRevision(img)
	return img
}

func TestImageHelpNamesExclusionAndRevision(t *testing.T) {
	for _, want := range []string{
		"Generated columns are excluded from the image and from the revision",
		"sha256",
		"without\nthe revision field",
		"--from-file",
	} {
		if !strings.Contains(imageCmd.Long, want) {
			t.Errorf("bd image --help does not say %q", want)
		}
	}
	if imageCmd.Flags().Lookup("from-file") == nil {
		t.Error("bd image has no --from-file flag")
	}
	if !isReadOnlyCommand("image") {
		t.Error("bd image is not registered as a read-only command")
	}
}

func TestPrintImagesOneCallCanonicalLines(t *testing.T) {
	imager := &recordingImager{images: map[string]issueops.Image{
		"bd-1":      imageForTest("bd-1", "issues"),
		"bd-wisp-1": imageForTest("bd-wisp-1", "wisps"),
	}}
	out := captureStdout(t, func() error {
		return printImages(context.Background(), imager, []string{"bd-wisp-1", "bd-1"})
	})
	if len(imager.calls) != 1 || len(imager.calls[0]) != 2 {
		t.Fatalf("Image calls = %v, want one call with both ids", imager.calls)
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines:\n%s", len(lines), out)
	}
	for i, id := range []string{"bd-wisp-1", "bd-1"} {
		want, err := storeissueops.CanonicalImageJSON(imager.images[id])
		if err != nil {
			t.Fatal(err)
		}
		if lines[i] != string(want) {
			t.Errorf("line %d\n got %s\nwant %s", i, lines[i], want)
		}
		if !strings.Contains(lines[i], `"revision":"`+imager.images[id].Revision+`"`) {
			t.Errorf("line %d does not carry its revision: %s", i, lines[i])
		}
	}
	if !strings.Contains(lines[1], `"title":"t <bd-1>"`) {
		t.Errorf("stored text is HTML-escaped: %s", lines[1])
	}
}

func TestPrintImagesMissingIDPrintsNothing(t *testing.T) {
	imager := &recordingImager{images: map[string]issueops.Image{"bd-1": imageForTest("bd-1", "issues")}}
	var runErr error
	out := captureStdout(t, func() error {
		runErr = printImages(context.Background(), imager, []string{"bd-1", "bd-x", "bd-y"})
		return nil
	})
	if out != "" {
		t.Fatalf("stdout = %q, want nothing", out)
	}
	if code, ok := exitCodeFromError(runErr); !ok || code != 1 {
		t.Fatalf("err = %v, want exit 1", runErr)
	}
}

// fakeImagerProvider is a provider that answers the Imager accessor.
type fakeImagerProvider struct{ imager issueops.Imager }

func (fakeImagerProvider) NewUOW(context.Context) (uow.UnitOfWork, error) {
	return nil, errors.New("not used")
}
func (fakeImagerProvider) Close(context.Context) error        { return nil }
func (p fakeImagerProvider) Imager() (issueops.Imager, error) { return p.imager, nil }

// decoratorWithoutImager stands for the external-dependency wrapper: a
// provider decorator that does not declare the accessor but can be peeled.
type decoratorWithoutImager struct{ inner uow.UnitOfWorkProvider }

func (d decoratorWithoutImager) NewUOW(ctx context.Context) (uow.UnitOfWork, error) {
	return d.inner.NewUOW(ctx)
}
func (d decoratorWithoutImager) Close(ctx context.Context) error { return d.inner.Close(ctx) }
func (d decoratorWithoutImager) Unwrap() uow.UnitOfWorkProvider  { return d.inner }

func TestProxiedImagerPeelsDecoratorsWithoutTheAccessor(t *testing.T) {
	saved := uowProvider
	t.Cleanup(func() { uowProvider = saved })

	want := &recordingImager{}
	uowProvider = decoratorWithoutImager{inner: decoratorWithoutImager{inner: fakeImagerProvider{imager: want}}}
	got, err := proxiedImager()
	if err != nil {
		t.Fatalf("proxiedImager: %v", err)
	}
	if got != want {
		t.Fatalf("proxiedImager returned %T, not the innermost provider's imager", got)
	}

	uowProvider = decoratorWithoutImager{inner: decoratorWithoutImager{}}
	if _, err := proxiedImager(); err == nil {
		t.Fatal("a provider chain with no Imager accessor was accepted")
	}

	uowProvider = nil
	if _, err := proxiedImager(); err == nil {
		t.Fatal("a nil provider was accepted")
	}
}

// TestImageTakesTheNonMutatingOpenOnEveryRoute pins the policy the root
// pre-run selects for `bd image`: the preview policy, not the classified-read
// one. The difference matters: a classified read still records the version,
// runs the post-upgrade migration and reconciliation, opens the embedded store
// with schema initialization and runs post-command maintenance.
func TestImageTakesTheNonMutatingOpenOnEveryRoute(t *testing.T) {
	if !isPreviewCommand(imageCmd) {
		t.Fatal("bd image does not take the preview policy")
	}
	// Only image: other read commands keep their classification.
	for _, cmd := range []*cobra.Command{showCmd, listCmd} {
		if isPreviewCommand(cmd) {
			t.Errorf("bd %s takes the preview policy too", cmd.Name())
		}
	}

	preview := isPreviewCommand(imageCmd)
	policy := effectiveRootStorePolicy(imageCmd.Name(), false)

	// Proxied: rootProviderOptions picks WithPreview (no CREATE DATABASE, no
	// migration) whenever preview is set, ahead of WithReadOnly, which still
	// creates and migrates; the integration test checks the effect.
	if opts := rootProviderOptions(preview, policy.readOnly || preview); len(opts) != 1 {
		t.Errorf("the proxied provider opens with %d options, want WithPreview alone", len(opts))
	}
	// Embedded and server: a preview open, with no classified-read sweep (the
	// root pre-run's ClassifiedRead: policy.readOnly && !readonlyMode &&
	// !previewMode).
	if policy.readOnly && !preview {
		t.Error("the store opens as a classified read, which may still sweep")
	}
	// Post-command maintenance: TestPersistentPostRunImageRunsNoAutoBackup
	// runs the real hook.
}

// TestPersistentPostRunImageRunsNoAutoBackup runs the real PersistentPostRunE
// after `bd image` on both arms with the auto-backup step recorded. The direct
// and embedded arm must not call it after a preview, or an image with backup
// enabled writes backup state; the proxied arm already skips previews.
// `bd list` is the control that the step is reachable.
//
// Cannot be parallel: mutates PersistentPostRunE's package globals.
func TestPersistentPostRunImageRunsNoAutoBackup(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cmd     *cobra.Command
		proxied bool
		want    int
	}{
		{name: "direct image", cmd: imageCmd},
		{name: "proxied image", cmd: imageCmd, proxied: true},
		{name: "direct list control", cmd: &cobra.Command{Use: "list"}, want: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			t.Setenv("BEADS_DIR", "")

			oldStore, oldProvider := store, uowProvider
			oldReadonly, oldProxied := readonlyMode, proxiedServerMode
			oldRootCtx, oldRootCancel := rootCtx, rootCancel
			oldSpan, oldProfile, oldTrace := commandSpan, profileFile, traceFile
			oldDidWrite, oldTipMetadata := commandDidWrite.Load(), commandDidWriteTipMetadata
			oldBackup := runPostRunAutoBackup
			t.Cleanup(func() {
				store, uowProvider = oldStore, oldProvider
				readonlyMode, proxiedServerMode = oldReadonly, oldProxied
				rootCtx, rootCancel = oldRootCtx, oldRootCancel
				commandSpan, profileFile, traceFile = oldSpan, oldProfile, oldTrace
				commandDidWrite.Store(oldDidWrite)
				commandDidWriteTipMetadata = oldTipMetadata
				runPostRunAutoBackup = oldBackup
			})

			calls := 0
			runPostRunAutoBackup = func(context.Context) { calls++ }
			store, uowProvider = nil, nil
			readonlyMode = false
			proxiedServerMode = tc.proxied
			rootCtx, rootCancel = context.Background(), nil
			commandSpan, profileFile, traceFile = nil, nil, nil
			commandDidWrite.Store(false)
			commandDidWriteTipMetadata = false

			if err := rootCmd.PersistentPostRunE(tc.cmd, nil); err != nil {
				t.Fatalf("PersistentPostRunE: %v", err)
			}
			if calls != tc.want {
				t.Fatalf("auto-backup step calls = %d, want %d", calls, tc.want)
			}
		})
	}
}
