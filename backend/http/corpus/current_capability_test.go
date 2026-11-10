//go:build cgo

package corpus_test

import (
	"context"
	"slices"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/steveyegge/beads/internal/httpapi/spec"
)

// currentCapabilityShapes are the corpus shapes whose wire transcripts are
// ALSO recorded against a current-capability server: this tree's bd serve,
// unmasked. The reference profile (bd 1.3.0) lacks issues.update.claim, so
// there every update --claim already takes the claimIssue fallback, and a
// client change on the current path (fix D7's "always claimIssue plus a
// read-back") leaves every reference transcript as it was. These catch it,
// together with the request paths the native fixes are recorded as changing
// over bd connect: update --claim (D7), ready and list text (D1, D2) and
// close --claim-next (B10).
var currentCapabilityShapes = []string{
	"gc-update-claim",
	"gc-update-claim-actor",
	"gc-update-claim-wisp",
	"gc-update-claim-conflict",
	"gc-update-claim-closed",
	"gc-ready-pool-demand",
	"gc-ready-probe",
	"mc-list-label-text",
	"mc-list-plain",
	"gc-close-claim-next",
}

// loadCurrentProfile names the operations this tree's server publishes, from
// the OpenAPI document it embeds. Only OperationID, Method and the path
// pattern are filled in: the unmasked proxy refuses nothing.
func loadCurrentProfile(t *testing.T) *referenceProfile {
	t.Helper()
	var doc struct {
		Paths map[string]map[string]yaml.Node `yaml:"paths"`
	}
	if err := yaml.Unmarshal(spec.OpenAPIV0(), &doc); err != nil {
		t.Fatalf("parse the embedded OpenAPI document: %v", err)
	}
	p := &referenceProfile{}
	for path, item := range doc.Paths {
		for method, node := range item {
			switch method {
			case "get", "put", "post", "patch", "delete":
			default:
				continue
			}
			var op struct {
				OperationID string `yaml:"operationId"`
			}
			if err := node.Decode(&op); err != nil || op.OperationID == "" {
				t.Fatalf("OpenAPI %s %s: no operationId (%v)", method, path, err)
			}
			pattern, err := templatePattern(path)
			if err != nil {
				t.Fatalf("OpenAPI path %q: %v", path, err)
			}
			p.Operations = append(p.Operations, referenceOperation{
				Method: map[string]string{"get": "GET", "put": "PUT", "post": "POST", "patch": "PATCH", "delete": "DELETE"}[method],
				Path:   path, OperationID: op.OperationID, pattern: pattern,
			})
		}
	}
	slices.SortFunc(p.Operations, func(a, b referenceOperation) int {
		if a.OperationID < b.OperationID {
			return -1
		}
		if a.OperationID > b.OperationID {
			return 1
		}
		return 0
	})
	return p
}

// TestServedCorpusCurrentCapabilityTranscripts runs currentCapabilityShapes,
// in corpus order, on one served workspace seeded with the corpus seed,
// through an unmasked proxy, and holds each served run's requests and exit
// code to testdata/transcripts/current/<shape>.json. There is no embedded run
// and no output comparison: the reference-profile parts hold the outcomes;
// this holds what bd sends to a server that advertises everything.
func TestServedCorpusCurrentCapabilityTranscripts(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	c, _ := loadCorpus(t)
	var shapes []shape
	for _, s := range c.Shapes {
		if slices.Contains(currentCapabilityShapes, s.ID) {
			shapes = append(shapes, s)
		}
	}
	if len(shapes) != len(currentCapabilityShapes) {
		t.Fatalf("currentCapabilityShapes names %d shapes, the corpus has %d of them", len(currentCapabilityShapes), len(shapes))
	}
	bin := buildBD(t)
	proxy := newReferenceProxy(t, loadCurrentProfile(t), startCorpusServer(t))
	proxy.unmasked = true
	proxyAddr := startProxy(t, proxy)
	served := &side{name: "served", dir: t.TempDir(), files: t.TempDir(), env: servedEnv(proxyAddr)}
	if err := prepareSide(context.Background(), t, bin, c, served, connectArgs(proxyAddr)); err != nil {
		t.Fatal(err)
	}
	warmPolicyCache(t, bin, served)
	known := knownCorpusIDs(t, c)
	for _, s := range shapes {
		t.Run(s.ID, func(t *testing.T) {
			for _, args := range s.Setup {
				if r := runBD(t, bin, served.dir, served.env, "", expand(context.Background(), t, bin, served, args)...); r.code != 0 {
					t.Fatalf("setup %v failed: %s", args, r)
				}
			}
			args := expand(context.Background(), t, bin, served, s.Args)
			proxy.wire.start()
			r := runBD(t, bin, served.dir, served.env, s.Stdin, args...)
			wire, expired := proxy.wire.stop()
			exit := r.code
			t.Logf("bd %v: %s", s.Args, r)
			if expired > 0 {
				t.Logf("%d read(s) the read memo let through only for its TTL were left out of the transcript", expired)
			}
			if drift := checkTranscript(t, currentTranscriptDir, transcript{Shape: s.ID, Exit: &exit, Requests: maskMintedIDs(wire, known)}, true); drift != "" {
				t.Error(drift)
			}
		})
	}
	if n := proxy.foreign.Load(); n > 0 {
		t.Logf("%d request(s) from other processes reached the proxy and were neither counted nor recorded", n)
	}
}
