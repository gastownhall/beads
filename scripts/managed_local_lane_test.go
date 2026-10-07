package scripts_test

import (
	"go/build"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// managedLocalLaneTests are the managed-local proxied lifecycle tests the
// retired proxied-local-smoke.yml asserted were compiled into its test binary
// before it ran `-test.run ^TestManagedLocalProxied`. They now run in
// //cmd/bd:bd_managed_local_test, whose -test.run would silently match fewer
// tests if a build constraint dropped one.
var managedLocalLaneTests = []string{
	"TestManagedLocalProxiedLifecycleSmoke",
	"TestManagedLocalProxiedForeignListenerNotAdopted",
	"TestManagedLocalProxiedReusedPidNeverKilled",
	"TestManagedLocalProxiedLegacyRecordFailClosed",
	"TestManagedLocalProxiedOrphanBackendReaped",
	"TestManagedLocalProxiedNProcessColdStartConvergence",
	"TestManagedLocalProxiedStopStartInterleave",
	"TestManagedLocalProxiedDoltStatusReportsLiveTopology",
	"TestManagedLocalProxiedDoltStartRefusesOverLiveProxy",
	"TestManagedLocalProxiedDoltStartRefusesWithProxyDown",
	"TestManagedLocalProxiedDoltLifecycleLeavesOtherTopologiesAlone",
	"TestManagedLocalProxiedTrackerUOWConformance",
	"TestManagedLocalProxiedBackupRoundTrip",
	"TestManagedLocalProxiedAutoBackup",
	"TestManagedLocalProxiedBackupRestoreRefusedWhileAttached",
	"TestManagedLocalProxiedPurgeWispsPlaneRetention",
	"TestManagedLocalProxiedBackendCleanExitRetiresProxy",
}

// TestManagedLocalLaneTestsCompileIntoBdTest is the job's "Assert the
// managed-local lifecycle tests are compiled in" step: each pinned test is
// defined in a cmd/bd _test.go file that a linux/amd64 cgo build with
// bd_test's tags (gms_pure_go, no integration) compiles.
func TestManagedLocalLaneTestsCompileIntoBdTest(t *testing.T) {
	dir := filepath.Join(sourceRepoRoot(t), "cmd", "bd")
	ctx := build.Default
	ctx.GOOS, ctx.GOARCH, ctx.CgoEnabled = "linux", "amd64", true
	ctx.BuildTags = []string{"gms_pure_go"}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	defined := map[string]string{}
	funcRe := regexp.MustCompile(`(?m)^func (TestManagedLocalProxied\w*)\(`)
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, "_test.go") {
			continue
		}
		match, err := ctx.MatchFile(dir, name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !match {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range funcRe.FindAllStringSubmatch(string(data), -1) {
			defined[m[1]] = name
		}
	}
	for _, name := range managedLocalLaneTests {
		if _, ok := defined[name]; !ok {
			t.Errorf("%s is not defined in any cmd/bd test file a linux cgo build of bd_test compiles", name)
		}
	}
	if len(defined) < len(managedLocalLaneTests) {
		t.Errorf("found %d TestManagedLocalProxied* tests, want at least %d: %v", len(defined), len(managedLocalLaneTests), defined)
	}
}
