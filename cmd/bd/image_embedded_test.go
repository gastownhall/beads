//go:build cgo

package main

import (
	"os"
	"strings"
	"testing"
)

// TestEmbeddedImage pins `bd image` on an embedded workspace alone: the
// front door's id resolution, the refusal of a missing id and --from-file.
// The cross-route byte comparison lives in image_proxied_integration_test.go,
// which images this same kind of workspace before serving it to the other two
// routes.
func TestEmbeddedImage(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "imge")
	route := embeddedImageRoute(bd, dir)

	durable := bdCreate(t, bd, dir, "imaged durable")
	wisp := bdCreate(t, bd, dir, "imaged wisp", "--ephemeral")
	runClassic(t, bd, dir, "comments", "add", durable.ID, "kept in the image")

	out := route.image(t, wisp.ID, durable.ID)
	images := decodeImageLines(t, out)
	if len(images) != 2 || images[0].Table != "wisps" || images[1].Table != "issues" {
		t.Fatalf("want the wisp then the durable image, got:\n%s", out)
	}
	if len(images[1].Comments) != 1 || images[1].Comments[0]["text"] != "kept in the image" {
		t.Errorf("durable comments = %v", images[1].Comments)
	}

	t.Run("prefix_resolution", func(t *testing.T) {
		short := strings.TrimPrefix(durable.ID, "imge-")
		if got, want := route.image(t, short), route.image(t, durable.ID); got != want {
			t.Errorf("bd image %s does not resolve to %s:\n got %s\nwant %s", short, durable.ID, got, want)
		}
	})

	t.Run("missing_id", func(t *testing.T) {
		assertImageMissing(t, route, durable.ID)
	})

	t.Run("from_file", func(t *testing.T) {
		assertImageFromFile(t, route, out, wisp.ID, durable.ID)
	})

	t.Run("revision_moves_on_write", func(t *testing.T) {
		before := decodeImageLines(t, route.image(t, durable.ID))[0].Revision
		runClassic(t, bd, dir, "label", "add", durable.ID, "archived")
		after := decodeImageLines(t, route.image(t, durable.ID))[0].Revision
		if before == after {
			t.Errorf("a label add left the revision at %s", before)
		}
	})
}
