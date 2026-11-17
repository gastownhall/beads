package scripts_test

import (
	"regexp"
	"strings"
	"testing"
)

// pr.yml's windows-make-shell (required through CI Gate / Required) used to
// install all three of its Make toolchains from the network on every PR and
// merge group: `choco install make` (the Chocolatey feed's 503s on Oct 19
// 2026), msys2/setup-msys2 downloading MSYS2 plus `git make` from MSYS2's
// mirrors ("Operation too slow"), cygwin-install-action (cygwin.com and a
// kernel.org mirror), and a DNS failure on Oct 23. 28 failures in two weeks,
// 8 of the 12 merge-queue ejections that week. These tests pin the offline
// design: each leg takes its toolchain from the Blacksmith image or from a
// cache only main.yml's windows-make-shell-toolchains saver writes, verifies
// what it restores, and keeps the network as a bounded, retried fallback.

const (
	windowsMakeShellJob      = "windows-make-shell"
	windowsMakeShellSaverJob = "windows-make-shell-toolchains"

	windowsMakeShellScriptDir = "scripts/ci/windows-make-shell/"

	// The cache paths both sides name (actions/cache's version hashes the
	// path, so a saver and a reader must spell it identically).
	nativeMakeCachePath = "${{ runner.temp }}/make-shell/native"
	msys2PkgCachePath   = "${{ runner.temp }}/make-shell/msys2"
	cygwinPkgCachePath  = "${{ runner.temp }}/make-shell/cygwin"

	// Chocolatey's make 4.4.1 package carries ezwinports' native build
	// (make-4.4.1-without-guile-w32-bin.zip): tier 3 installs it and the
	// saver caches that same binary for tier 2, pinned by SHA-256.
	chocolateyMake441SHA256 = "cc6dc291113dcbcc7735835acbfc23c52ed037e4e124ff2d7a0aeae6df563a9f"

	nativeMakeCacheKey = "beads-make-shell-native-v1-${{ runner.os }}-${{ runner.arch }}-make-4.4.1-cc6dc291113d"
	msys2CachePrefix   = "beads-make-shell-msys2-v1-${{ runner.os }}-${{ runner.arch }}-git-make-"
	msys2CacheKey      = msys2CachePrefix + "${{ steps.toolchain-keys.outputs.msys2-sync-db }}"
	cygwinCachePrefix  = "beads-make-shell-cygwin-v1-${{ runner.os }}-${{ runner.arch }}-git-make-"
	cygwinCacheKey     = cygwinCachePrefix + "${{ steps.toolchain-keys.outputs.month }}"

	setupMSYS2ActionFamily  = "msys2/setup-msys2"
	cygwinInstallFamily     = "cygwin/cygwin-install-action"
	windowsMakeShellRunKeys = "./scripts/ci/windows-make-shell/cache-keys.ps1"
	windowsMakeShellRunDiag = "./scripts/ci/windows-make-shell/diagnostics.ps1"
	msys2PackagesRun        = "bash scripts/ci/windows-make-shell/msys2-packages.sh git make"
)

// windowsMakeShellSetupSteps is the job's steps after checkout, in order:
// the diagnostics, each leg's restore and install, then the three exercises.
var windowsMakeShellSetupSteps = []string{
	"Check Windows tooling diagnostics",
	"Compute toolchain cache keys",
	"Restore cached native GNU Make",
	"Install native GNU Make",
	"Set up preinstalled MSYS2",
	"Restore MSYS2 package cache",
	"Install MSYS2 GNU Make",
	"Restore Cygwin package cache",
	"Install Cygwin GNU Make from the package cache",
	"Set up Cygwin GNU Make",
	"Warn when Cygwin setup was skipped by a download failure",
	"Exercise native Windows Make",
	"Exercise MSYS2-hosted Make",
	"Exercise Cygwin-hosted Make",
}

// windowsMakeShellCygwinSetupIDs are the only steps in the job that may carry
// continue-on-error: both provision the Cygwin leg, whose setup has always
// been warning-only (a failed cache install falls back to the online
// installer, and a failed download skips the leg with a warning). Every
// native and MSYS2 step, and the Cygwin exercise, fails the job directly.
var windowsMakeShellCygwinSetupIDs = map[string]bool{"cygwin-cached": true, "cygwin": true}

func windowsMakeShellJobs(t *testing.T) (pr, saver ciWorkflowJob) {
	t.Helper()
	return readCIWorkflow(t, "pr.yml").job(t, windowsMakeShellJob), readCIWorkflow(t, "main.yml").job(t, windowsMakeShellSaverJob)
}

// TestWindowsMakeShellProvisionsWithoutNetworkInstalls pins the PR job's
// provisioning: no step installs from Chocolatey or downloads MSYS2 itself,
// each leg restores its main-saved cache before its install step, and the
// install steps run the shared scripts with the restored paths.
func TestWindowsMakeShellProvisionsWithoutNetworkInstalls(t *testing.T) {
	pr, _ := windowsMakeShellJobs(t)

	var names []string
	for _, step := range pr.Steps[1:] {
		names = append(names, step.Name)
	}
	if actionFamily(pr.Steps[0].Uses) != "actions/checkout" {
		t.Errorf("%s first step uses %q, want actions/checkout", windowsMakeShellJob, pr.Steps[0].Uses)
	}
	if !equalStrings(names, windowsMakeShellSetupSteps) {
		t.Errorf("%s steps after checkout =\n  %q\nwant\n  %q", windowsMakeShellJob, names, windowsMakeShellSetupSteps)
	}

	for _, step := range pr.Steps {
		if strings.Contains(step.Run, "${{") {
			t.Errorf("%s step %q interpolates an expression into its script; pass it through env:", windowsMakeShellJob, step.Name)
		}
		if strings.Contains(step.Run, "choco install") {
			t.Errorf("%s step %q runs choco install directly; Chocolatey is native-make.ps1's last resort only", windowsMakeShellJob, step.Name)
		}
	}
	// The job has no Go toolchain step, and the image's go downloads go.mod's
	// toolchain from proxy.golang.org the moment anything runs `go env`
	// (the old diagnostics step did, and failed the job on Oct 23's DNS
	// outage).
	diagnostics := readPolicyFile(t, sourceRepoRoot(t), windowsMakeShellScriptDir+"diagnostics.ps1")
	if m := regexp.MustCompile(`(?m)^[^#\n]*\bgo(\.exe)?\s+(env|version|build|test|run)\b`).FindString(diagnostics); m != "" {
		t.Errorf("diagnostics.ps1 runs go (%q); keep the job off the network", m)
	}

	if got := pr.step(t, "Check Windows tooling diagnostics").Run; got != windowsMakeShellRunDiag {
		t.Errorf("diagnostics step runs %q, want %q", got, windowsMakeShellRunDiag)
	}
	keys := pr.step(t, "Compute toolchain cache keys")
	if keys.ID != "toolchain-keys" || keys.Run != windowsMakeShellRunKeys || keys.Shell != "pwsh" {
		t.Errorf("cache-key step id=%q shell=%q run=%q, want id toolchain-keys, pwsh, %q", keys.ID, keys.Shell, keys.Run, windowsMakeShellRunKeys)
	}

	// Native: restore the main-saved binary, then let native-make.ps1 pick
	// the image's make, else that binary, else Chocolatey.
	assertWindowsMakeShellRestore(t, pr.step(t, "Restore cached native GNU Make"), "native-make-cache", "${{ !cancelled() }}", nativeMakeCachePath, nativeMakeCacheKey, "")
	native := pr.step(t, "Install native GNU Make")
	if native.ID != "install-native" || native.Shell != "pwsh" || native.Env["NATIVE_MAKE_CACHE"] != nativeMakeCachePath ||
		native.Run != "./scripts/ci/windows-make-shell/native-make.ps1 -Mode Install -CacheDir $env:NATIVE_MAKE_CACHE" {
		t.Errorf("native install step id=%q shell=%q env=%v run=%q", native.ID, native.Shell, native.Env, native.Run)
	}

	// MSYS2: the image's C:\msys64, never a downloaded release, an update or
	// setup-msys2's own install (no retry) and cache (an implicit PR save).
	msys2 := pr.step(t, "Set up preinstalled MSYS2")
	assertPreinstalledMSYS2(t, windowsMakeShellJob, msys2)
	if msys2.If != "${{ !cancelled() }}" {
		t.Errorf("MSYS2 setup if = %q, want ${{ !cancelled() }}", msys2.If)
	}
	assertWindowsMakeShellRestore(t, pr.step(t, "Restore MSYS2 package cache"), "msys2-cache",
		"${{ !cancelled() && steps.toolchain-keys.outputs.msys2-sync-db != '' }}", msys2PkgCachePath, msys2CacheKey, msys2CachePrefix)
	install := pr.step(t, "Install MSYS2 GNU Make")
	if install.ID != "install-msys2" || install.Shell != "msys2 {0}" || install.Run != msys2PackagesRun ||
		install.If != "${{ !cancelled() && steps.msys2-shell.outcome == 'success' }}" ||
		install.Env["MSYS2_PACKAGE_CACHE"] != msys2PkgCachePath ||
		install.Env["MSYS2_PACKAGE_CACHE_HIT"] != "${{ steps.msys2-cache.outputs.cache-hit }}" {
		t.Errorf("MSYS2 install step id=%q shell=%q if=%q env=%v run=%q", install.ID, install.Shell, install.If, install.Env, install.Run)
	}

	// Cygwin: an offline install from the main-saved packages, and today's
	// online installer only when that did not succeed.
	assertWindowsMakeShellRestore(t, pr.step(t, "Restore Cygwin package cache"), "cygwin-cache", "${{ !cancelled() }}", cygwinPkgCachePath, cygwinCacheKey, cygwinCachePrefix)
	cached := pr.step(t, "Install Cygwin GNU Make from the package cache")
	if cached.ID != "cygwin-cached" || cached.Shell != "pwsh" || cached.Env["CYGWIN_PACKAGE_CACHE"] != cygwinPkgCachePath ||
		cached.If != "${{ !cancelled() && steps.cygwin-cache.outputs.cache-matched-key != '' }}" ||
		cached.Run != "./scripts/ci/windows-make-shell/cygwin-offline.ps1 -Mode Install -CacheDir $env:CYGWIN_PACKAGE_CACHE -Root (Join-Path $env:RUNNER_TEMP 'cygwin')" {
		t.Errorf("Cygwin cache install step id=%q shell=%q if=%q env=%v run=%q", cached.ID, cached.Shell, cached.If, cached.Env, cached.Run)
	}
	online := pr.step(t, "Set up Cygwin GNU Make")
	if online.ID != "cygwin" || actionFamily(online.Uses) != cygwinInstallFamily ||
		online.If != "${{ !cancelled() && steps.cygwin-cached.outcome != 'success' }}" ||
		online.With["packages"] != "git make" || online.With["add-to-path"] != "false" {
		t.Errorf("Cygwin online setup step id=%q uses=%q if=%q with=%v", online.ID, online.Uses, online.If, online.With)
	}
	// The offline install takes its packages from the script, the online one
	// from the action's input. Once main's cache is warm every PR installs
	// offline, with no fallback in the exercise, so the script and the cache
	// key that names the packages must list the input's packages.
	packages := strings.Fields(online.With["packages"])
	cygwinOffline := readPolicyFile(t, sourceRepoRoot(t), windowsMakeShellScriptDir+"cygwin-offline.ps1")
	if want := "\n$Packages = '" + strings.Join(packages, ",") + "'\n"; !strings.Contains(cygwinOffline, want) {
		t.Errorf("cygwin-offline.ps1 does not declare %s, the online setup's packages %q", strings.TrimSpace(want), online.With["packages"])
	}
	if !strings.HasSuffix(cygwinCachePrefix, "-"+strings.Join(packages, "-")+"-") {
		t.Errorf("Cygwin cache key prefix %q does not name the online setup's packages %q", cygwinCachePrefix, online.With["packages"])
	}
	if warn := pr.step(t, "Warn when Cygwin setup was skipped by a download failure"); warn.If != "${{ !cancelled() && steps.cygwin.outcome == 'failure' }}" {
		t.Errorf("Cygwin warning if = %q", warn.If)
	}
	exercise := pr.step(t, "Exercise Cygwin-hosted Make")
	if exercise.If != "${{ !cancelled() && (steps.cygwin-cached.outcome == 'success' || steps.cygwin.outcome == 'success') }}" ||
		exercise.Env["CYGWIN_ROOT"] != "${{ steps.cygwin-cached.outputs.root || steps.cygwin.outputs.root }}" {
		t.Errorf("Cygwin exercise if=%q env=%v; it must run on, and use the root of, whichever setup succeeded", exercise.If, exercise.Env)
	}

	// The exercise steps keep reading the leg's own install step.
	for name, want := range map[string]string{
		"Exercise native Windows Make": "${{ !cancelled() && steps.install-native.outcome == 'success' }}",
		"Exercise MSYS2-hosted Make":   "${{ !cancelled() && steps.install-msys2.outcome == 'success' }}",
	} {
		if got := pr.step(t, name).If; got != want {
			t.Errorf("%s if = %q, want %q", name, got, want)
		}
	}
}

func assertWindowsMakeShellRestore(t *testing.T, step ciWorkflowStep, id, cond, path, key, restoreKeys string) {
	t.Helper()
	if step.ID != id || actionFamily(step.Uses) != cacheRestoreActionFamily || step.If != cond ||
		step.With["path"] != path || step.With["key"] != key || step.With["restore-keys"] != restoreKeys ||
		step.With["lookup-only"] != "" || step.Env["SEGMENT_DOWNLOAD_TIMEOUT_MINS"] != "1" {
		t.Errorf("restore step %q = {id:%q uses:%q if:%q with:%v env:%v}, want {id:%q restore, if:%q path:%q key:%q restore-keys:%q, SEGMENT_DOWNLOAD_TIMEOUT_MINS=1}",
			step.Name, step.ID, step.Uses, step.If, step.With, step.Env, id, cond, path, key, restoreKeys)
	}
}

func assertPreinstalledMSYS2(t *testing.T, jobName string, step ciWorkflowStep) {
	t.Helper()
	want := map[string]string{"msystem": "MSYS", "path-type": "minimal", "release": "false", "update": "false", "cache": "false"}
	if step.ID != "msys2-shell" || actionFamily(step.Uses) != setupMSYS2ActionFamily || len(step.With) != len(want) {
		t.Errorf("%s MSYS2 setup id=%q uses=%q with=%v, want id msys2-shell, %s with exactly %v", jobName, step.ID, step.Uses, step.With, setupMSYS2ActionFamily, want)
	}
	for k, v := range want {
		if step.With[k] != v {
			t.Errorf("%s MSYS2 setup %s = %q, want %q", jobName, k, step.With[k], v)
		}
	}
	if step.TimeoutMinutes <= 0 {
		t.Errorf("%s MSYS2 setup has no timeout-minutes", jobName)
	}
}

// TestWindowsMakeShellToolchainSaverMatchesPRJob pins main.yml's saver
// against the PR job: the same label and image (Blacksmith cannot see
// GitHub-saved caches, and the MSYS2 packages are resolved against the
// image's own sync database), the same paths and keys, saves only on a miss
// and only after its seed step proved the content installs, and the same
// shared scripts.
func TestWindowsMakeShellToolchainSaverMatchesPRJob(t *testing.T) {
	pr, saver := windowsMakeShellJobs(t)
	if saver.RunsOn != pr.RunsOn || saver.RunsOn != mainBlacksmithWindows4vcpuRunsOn {
		t.Errorf("%s runs-on = %q, want the PR job's %q", windowsMakeShellSaverJob, saver.RunsOn, pr.RunsOn)
	}
	if saver.TimeoutMinutes <= 0 {
		t.Errorf("%s has no timeout-minutes", windowsMakeShellSaverJob)
	}
	if got := saver.step(t, "Compute toolchain cache keys"); got.ID != "toolchain-keys" || got.Run != windowsMakeShellRunKeys {
		t.Errorf("saver cache-key step id=%q run=%q, want the PR job's", got.ID, got.Run)
	}
	if got := saver.step(t, "Check Windows tooling diagnostics").Run; got != windowsMakeShellRunDiag {
		t.Errorf("saver diagnostics run %q, want %q", got, windowsMakeShellRunDiag)
	}
	for _, step := range saver.Steps {
		if strings.Contains(step.Run, "${{") {
			t.Errorf("%s step %q interpolates an expression into its script; pass it through env:", windowsMakeShellSaverJob, step.Name)
		}
	}

	type cacheLeg struct {
		prRestore, lookup, lookupID, seed, seedID, save string
		path, key                                       string
		lookupIf                                        string
	}
	legs := []cacheLeg{
		{"Restore cached native GNU Make", "Look up cached native GNU Make", "native-make-cache", "Seed native GNU Make cache", "seed-native", "Save native GNU Make cache",
			nativeMakeCachePath, nativeMakeCacheKey, "${{ !cancelled() }}"},
		{"Restore MSYS2 package cache", "Look up cached MSYS2 packages", "msys2-cache", "Seed MSYS2 package cache", "seed-msys2", "Save MSYS2 package cache",
			msys2PkgCachePath, msys2CacheKey, "${{ !cancelled() && steps.toolchain-keys.outputs.msys2-sync-db != '' }}"},
		{"Restore Cygwin package cache", "Look up cached Cygwin packages", "cygwin-cache", "Seed Cygwin package cache", "seed-cygwin", "Save Cygwin package cache",
			cygwinPkgCachePath, cygwinCacheKey, "${{ !cancelled() }}"},
	}
	for _, leg := range legs {
		prRestore := pr.step(t, leg.prRestore)
		lookup := saver.step(t, leg.lookup)
		save := saver.step(t, leg.save)
		if lookup.ID != leg.lookupID || actionFamily(lookup.Uses) != cacheRestoreActionFamily || lookup.With["lookup-only"] != "true" ||
			lookup.With["path"] != leg.path || lookup.With["key"] != leg.key || lookup.With["restore-keys"] != "" || lookup.If != leg.lookupIf {
			t.Errorf("saver %q = {id:%q uses:%q if:%q with:%v}, want an exact-key lookup-only restore of %q / %q if %q",
				leg.lookup, lookup.ID, lookup.Uses, lookup.If, lookup.With, leg.path, leg.key, leg.lookupIf)
		}
		if actionFamily(save.Uses) != cacheSaveActionFamily || save.With["path"] != leg.path || save.With["key"] != leg.key {
			t.Errorf("saver %q uses=%q with=%v, want a save of %q / %q", leg.save, save.Uses, save.With, leg.path, leg.key)
		}
		wantSaveIf := "${{ !cancelled() && steps." + leg.lookupID + ".outputs.cache-hit != 'true' && steps." + leg.seedID + ".outcome == 'success' }}"
		if save.If != wantSaveIf {
			t.Errorf("saver %q if = %q, want %q (save only a seeded, verified miss)", leg.save, save.If, wantSaveIf)
		}
		if prRestore.With["path"] != save.With["path"] || prRestore.With["key"] != save.With["key"] ||
			(prRestore.With["restore-keys"] != "" && !strings.HasPrefix(save.With["key"], prRestore.With["restore-keys"])) {
			t.Errorf("pr.yml %q (%q, %q, %q) cannot reach the saver's %q (%q, %q)", leg.prRestore,
				prRestore.With["path"], prRestore.With["key"], prRestore.With["restore-keys"], leg.save, save.With["path"], save.With["key"])
		}
		if seed := saver.step(t, leg.seed); seed.ID != leg.seedID || seed.TimeoutMinutes <= 0 || !strings.Contains(seed.If, "!cancelled()") {
			t.Errorf("saver %q id=%q timeout-minutes=%d if=%q, want id %q, a timeout and !cancelled()", leg.seed, seed.ID, seed.TimeoutMinutes, seed.If, leg.seedID)
		}
		assertStepsBefore(t, saver, []string{"Compute toolchain cache keys", leg.lookup}, []string{leg.seed})
		assertStepsBefore(t, saver, []string{leg.seed}, []string{leg.save})
	}

	native := saver.step(t, "Seed native GNU Make cache")
	if native.If != "${{ !cancelled() && steps.native-make-cache.outputs.cache-hit != 'true' }}" || native.Shell != "pwsh" ||
		native.Env["NATIVE_MAKE_CACHE"] != nativeMakeCachePath ||
		native.Run != "./scripts/ci/windows-make-shell/native-make.ps1 -Mode Seed -CacheDir $env:NATIVE_MAKE_CACHE" {
		t.Errorf("saver native seed if=%q shell=%q env=%v run=%q", native.If, native.Shell, native.Env, native.Run)
	}

	msys2 := saver.step(t, "Set up preinstalled MSYS2")
	assertPreinstalledMSYS2(t, windowsMakeShellSaverJob, msys2)
	if msys2.If != "${{ !cancelled() && steps.toolchain-keys.outputs.msys2-sync-db != '' && steps.msys2-cache.outputs.cache-hit != 'true' }}" {
		t.Errorf("saver MSYS2 setup if = %q, want it only on a miss", msys2.If)
	}
	prInstall := pr.step(t, "Install MSYS2 GNU Make")
	seedMSYS2 := saver.step(t, "Seed MSYS2 package cache")
	if seedMSYS2.Run != prInstall.Run || seedMSYS2.Shell != prInstall.Shell || seedMSYS2.Env["MSYS2_PACKAGE_CACHE"] != msys2PkgCachePath ||
		seedMSYS2.If != "${{ !cancelled() && steps.msys2-shell.outcome == 'success' }}" {
		t.Errorf("saver MSYS2 seed shell=%q if=%q env=%v run=%q; it must run the PR job's install (%q under %q)",
			seedMSYS2.Shell, seedMSYS2.If, seedMSYS2.Env, seedMSYS2.Run, prInstall.Run, prInstall.Shell)
	}
	assertStepsBefore(t, saver, []string{"Set up preinstalled MSYS2"}, []string{"Seed MSYS2 package cache"})

	download := saver.step(t, "Download Cygwin GNU Make")
	prOnline := pr.step(t, "Set up Cygwin GNU Make")
	if download.ID != "cygwin" || download.Uses != prOnline.Uses || download.With["packages"] != prOnline.With["packages"] ||
		download.With["add-to-path"] != "false" || download.ContinueOnError != nil ||
		download.If != "${{ !cancelled() && steps.cygwin-cache.outputs.cache-hit != 'true' }}" || download.TimeoutMinutes <= 0 {
		t.Errorf("saver Cygwin download id=%q uses=%q if=%q with=%v continue-on-error=%v", download.ID, download.Uses, download.If, download.With, download.ContinueOnError)
	}
	seedCygwin := saver.step(t, "Seed Cygwin package cache")
	if seedCygwin.If != "${{ !cancelled() && steps.cygwin.outcome == 'success' }}" || seedCygwin.Shell != "pwsh" ||
		seedCygwin.Env["CYGWIN_PACKAGE_CACHE"] != cygwinPkgCachePath ||
		seedCygwin.Env["CYGWIN_SETUP"] != "${{ steps.cygwin.outputs.setup }}" ||
		seedCygwin.Env["CYGWIN_DOWNLOADS"] != "${{ steps.cygwin.outputs.package-cache }}" ||
		seedCygwin.Run != "./scripts/ci/windows-make-shell/cygwin-offline.ps1 -Mode Seed -CacheDir $env:CYGWIN_PACKAGE_CACHE -SetupExe $env:CYGWIN_SETUP -PackageDir $env:CYGWIN_DOWNLOADS" {
		t.Errorf("saver Cygwin seed if=%q shell=%q env=%v run=%q", seedCygwin.If, seedCygwin.Shell, seedCygwin.Env, seedCygwin.Run)
	}
	assertStepsBefore(t, saver, []string{"Download Cygwin GNU Make"}, []string{"Seed Cygwin package cache"})

	// No other step in the saver writes a cache, and nothing keys per commit:
	// a key changes only with a pin, the image's MSYS2 sync database or the
	// UTC month.
	saves := 0
	for _, step := range saver.Steps {
		if actionFamily(step.Uses) == cacheSaveActionFamily {
			saves++
		}
		for _, perRun := range []string{"github.sha", "github.run_id", "github.run_number", "github.run_attempt"} {
			if strings.Contains(step.With["key"], perRun) {
				t.Errorf("saver step %q key %q names %s; toolchain keys must not change per commit or run", step.Name, step.With["key"], perRun)
			}
		}
	}
	if saves != len(legs) {
		t.Errorf("saver has %d cache saves, want %d", saves, len(legs))
	}
}

// TestWindowsMakeShellScriptsVerifyWhatTheyRestore pins the checks that make
// a restored cache safe to execute and keep MSYS2 consistent with the
// image: pacman's signature check (enforced, never weakened), no database
// refresh, an MSYS2 cache that holds every package file the install
// resolves to, the pinned SHA-256 on every native binary that is not the
// image's own, and Cygwin setup's signed setup.zst plus Authenticode on
// setup itself.
func TestWindowsMakeShellScriptsVerifyWhatTheyRestore(t *testing.T) {
	root := sourceRepoRoot(t)
	read := func(name string) string { return readPolicyFile(t, root, windowsMakeShellScriptDir+name) }

	msys2 := read("msys2-packages.sh")
	const (
		msys2Download = `pacman --noconfirm --noprogressbar -Sw --cachedir "$cache_dir" "$@"`
		msys2Resolve  = `package_files="$(pacman -Sp --print-format '%f' "$@")"`
		msys2Install  = `pacman --noconfirm --noprogressbar -S --needed --overwrite '*' --cachedir "$cache_dir" "$@"`
	)
	for _, want := range []string{
		`pacman-conf --repo=msys SigLevel`,
		`grep -qx PackageRequired`,
		`grep -qx PackageTrustedOnly`,
		msys2Download,
		msys2Resolve,
		`if [[ -z "$package_files" ]]; then`,
		`if [[ ! -s "$cache_dir/$file" ]]; then`,
		msys2Install,
	} {
		if !strings.Contains(msys2, want) {
			t.Errorf("msys2-packages.sh is missing %q", want)
		}
	}
	// The saver stores the cache directory as the exact hit for the image's
	// sync database, and pacman -Q passes whenever the image already has a
	// package. So the download fetches the targets even when they are
	// installed (--needed would skip them; pacman already skips files the
	// cache holds), and the cache must hold every file the transaction
	// resolves to before the install, after which the dependencies it
	// installed drop out of that transaction.
	pacmanDownload := regexp.MustCompile(`\bpacman\b.*(\s-S[a-z]*w|--downloadonly)`)
	for _, line := range strings.Split(msys2, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		if pacmanDownload.MatchString(line) && strings.Contains(line, "--needed") {
			t.Errorf("msys2-packages.sh downloads with --needed (%q): a package the image has installed never reaches the cache", strings.TrimSpace(line))
		}
	}
	if d, r, i := strings.Index(msys2, msys2Download), strings.Index(msys2, msys2Resolve), strings.Index(msys2, msys2Install); d < 0 || r < d || i < r {
		t.Errorf("msys2-packages.sh must download (at %d), then check the cache holds every resolved file (at %d), then install (at %d)", d, r, i)
	}
	// -y refreshes the image's sync database and -u upgrades its base: both
	// turn this into a partial upgrade against packages the cache never saw.
	if m := regexp.MustCompile(`pacman[^\n]*\s-(S|-sync)[a-z]*[yu]|--refresh|--sysupgrade`).FindString(msys2); m != "" {
		t.Errorf("msys2-packages.sh refreshes or upgrades MSYS2 (%q); install against the image's own sync database", m)
	}

	native := read("native-make.ps1")
	if !strings.Contains(strings.ToLower(native), "'"+chocolateyMake441SHA256+"'") {
		t.Errorf("native-make.ps1 does not pin Chocolatey's make 4.4.1 binary (sha256 %s)", chocolateyMake441SHA256)
	}
	if !strings.HasSuffix(nativeMakeCacheKey, chocolateyMake441SHA256[:12]) {
		t.Errorf("native cache key %q does not name the pinned binary %s", nativeMakeCacheKey, chocolateyMake441SHA256[:12])
	}
	for _, want := range []string{`'C:\mingw64\bin\mingw32-make.exe'`, `'GNU Make 4.4.1'`, `'--version=4.4.1'`, "Get-FileHash"} {
		if !strings.Contains(native, want) {
			t.Errorf("native-make.ps1 is missing %q", want)
		}
	}
	image, cache, choco := strings.Index(native, "# Tier 1"), strings.Index(native, "# Tier 2"), strings.Index(native, "# Tier 3")
	if image < 0 || cache < 0 || choco < 0 || image >= cache || cache >= choco {
		t.Errorf("native-make.ps1 tiers out of order (image %d, cache %d, chocolatey %d): the image's make comes first and Chocolatey last", image, cache, choco)
	}
	// Tier 3 is the only native path that fetches make.exe over the network.
	// Like tier 2 and Seed, it refuses a binary that is not the pin, and it
	// does so before its copy check, so Install never adds that binary's
	// directory to GITHUB_PATH.
	_, tier3, _ := strings.Cut(powerShellFunctionBody(t, native, "Install-NativeMake"), "# Tier 3")
	refuse := regexp.MustCompile(`if \(\$hash -ne \$ChocolateyMakeSha256\) \{\s+throw `).FindStringIndex(tier3)
	copyCheck := strings.Index(tier3, "Test-CopiedNativeMake -Candidate $installed")
	if refuse == nil || copyCheck < 0 || refuse[0] > copyCheck {
		t.Errorf("native-make.ps1 tier 3 must throw on a make.exe whose SHA-256 is not %s before Test-CopiedNativeMake (throw at %v, copy check at %d)", chocolateyMake441SHA256, refuse, copyCheck)
	}

	cygwin := read("cygwin-offline.ps1")
	for _, want := range []string{
		// cygwin-install-action's check-installer-sig pins (v6).
		"2CE11DA3A675A9D631E06A28DDFD6F730B9CC6989B43BD30AD7CC79D219CF2BD",
		"3AB2B14363B58A1856D4902C737797C899118095F7025DF7832C1B52483865A0",
		"Get-AuthenticodeSignature",
		"'setup.zst.sig'",
		"'-L'",
	} {
		if !strings.Contains(cygwin, want) {
			t.Errorf("cygwin-offline.ps1 is missing %q", want)
		}
	}
	if m := regexp.MustCompile(`'-X'|--no-verify`).FindString(cygwin); m != "" {
		t.Errorf("cygwin-offline.ps1 passes %s: setup must verify setup.zst's signature", m)
	}
}

// cygwinSetupIndexFiles are the package indexes Cygwin's setup reads from a
// local package tree (-L -l <dir>), in its order of preference (setup's
// ini.cc). It looks for them in <dir>\x86_64 if that directory exists, else in
// <dir>\<mirror>\x86_64 (fromcwd.cc); it skips an empty one; in local mode it
// installs from an index that has no .sig file without verifying it (ini.cc,
// check_ini_sig); and it reads <dir>\setup.rc before anything else, whose
// extrakeys entry adds keys it then accepts index signatures from
// (UserSettings.cc, KeysSetting.cc, crypto.cc).
var cygwinSetupIndexFiles = []string{"setup.zst", "setup.xz", "setup.bz2", "setup.ini"}

// TestCygwinOfflineLeavesSetupOnlyTheSignedIndex pins cygwin-offline.ps1's
// package-tree check against what setup reads (cygwinSetupIndexFiles): a
// restored cache is installed only if setup can read nothing in it but a
// non-empty setup.zst with a non-empty signature. Otherwise a crafted cache
// could put an empty setup.zst beside an unsigned setup.ini, an x86_64
// directory at the root, or a setup.rc with its own key there, and setup would
// install packages whose hashes nobody signed. A rejected tree fails the
// offline install, and the job falls back to cygwin-install-action.
func TestCygwinOfflineLeavesSetupOnlyTheSignedIndex(t *testing.T) {
	script := readPolicyFile(t, sourceRepoRoot(t), windowsMakeShellScriptDir+"cygwin-offline.ps1")
	check := powerShellFunctionBody(t, script, "Get-SignedPackageTree")

	// setup lists directories with FindFirstFile, which returns hidden files.
	for _, call := range regexp.MustCompile(`Get-ChildItem[^\n|)]*`).FindAllString(check, -1) {
		if !strings.Contains(call, "-Force") {
			t.Errorf("Get-SignedPackageTree lists %q without -Force; setup sees hidden files too", strings.TrimSpace(call))
		}
	}
	for _, want := range []string{
		// Nothing at the root but the one mirror directory: no setup.rc, no
		// second mirror, no x86_64 index directory of its own.
		"$rootFiles = @(Get-ChildItem -LiteralPath $PackageRoot -File -Force)",
		"if ($rootFiles.Count -ne 0) {",
		"$mirrors = @(Get-ChildItem -LiteralPath $PackageRoot -Directory -Force)",
		"if ($mirrors.Count -ne 1) {",
		"if ($mirrors[0].Name -eq 'x86_64') {",
		// In the index directory, setup.* files other than the signed pair
		// are rejected, and each of the pair is there once and not empty.
		"Where-Object { $_.Name -like 'setup.*' }",
		"Where-Object { $_.Name -notin $SignedIndexFiles }",
		"if ($unexpected.Count -ne 0) {",
		"if ($file.Count -ne 1 -or $file[0].Length -eq 0) {",
	} {
		if !strings.Contains(check, want) {
			t.Errorf("Get-SignedPackageTree is missing %q", want)
		}
	}

	m := regexp.MustCompile(`(?m)^\$SignedIndexFiles = @\(([^)\n]*)\)$`).FindStringSubmatch(script)
	if m == nil {
		t.Fatal("cygwin-offline.ps1 declares no $SignedIndexFiles = @(...)")
	}
	var allowed []string
	for _, q := range regexp.MustCompile(`'([^']*)'`).FindAllStringSubmatch(m[1], -1) {
		allowed = append(allowed, q[1])
	}
	if !equalStrings(allowed, []string{"setup.zst", "setup.zst.sig"}) {
		t.Errorf("$SignedIndexFiles = %q, want exactly setup.zst and its signature", allowed)
	}
	for _, index := range cygwinSetupIndexFiles {
		if !strings.HasPrefix(index, "setup.") {
			t.Errorf("index %q escapes the check's setup.* filter", index)
		}
		if index != "setup.zst" && contains(allowed, index) {
			t.Errorf("$SignedIndexFiles allows %s, an index setup reads without a signature check of its own", index)
		}
	}

	install := powerShellFunctionBody(t, script, "Install-FromCache")
	checkAt, setupAt := strings.Index(install, "Get-SignedPackageTree -PackageRoot $packageRoot"), strings.Index(install, "& $setup @(")
	if checkAt < 0 || setupAt < 0 || checkAt > setupAt {
		t.Errorf("Install-FromCache must run Get-SignedPackageTree (at %d) before setup (at %d)", checkAt, setupAt)
	}
}

// powerShellFunctionBody returns the body of a top-level `function name {`
// whose closing brace is the first `}` at the start of a line after it.
func powerShellFunctionBody(t *testing.T, script, name string) string {
	t.Helper()
	start := strings.Index(script, "\nfunction "+name+" {\n")
	if start < 0 {
		t.Fatalf("no top-level function %s", name)
	}
	body := script[start+len("\nfunction "+name+" {\n"):]
	end := strings.Index(body, "\n}\n")
	if end < 0 {
		t.Fatalf("function %s has no closing brace at the start of a line", name)
	}
	return body[:end]
}

// windowsMakeShellRetry is one retried network fallback: the attempts and
// the per-attempt bound the script declares, the backoff between attempts,
// and an allowance for the rest of the step. The step's timeout-minutes must
// exceed that worst case (or the step dies mid-retry with no hint which
// attempt stalled) and stay under the job's.
type windowsMakeShellRetry struct {
	script, step  string
	attempts      int
	perAttempt    int // seconds
	backoff       []int
	restAllowance int // seconds outside the retried call
	declarations  []string
}

func (r windowsMakeShellRetry) worstCaseSeconds() int {
	total := r.attempts*r.perAttempt + r.restAllowance
	for _, b := range r.backoff {
		total += b
	}
	return total
}

func TestWindowsMakeShellNetworkFallbacksAreBoundedAndRetried(t *testing.T) {
	pr, saver := windowsMakeShellJobs(t)
	root := sourceRepoRoot(t)
	for _, r := range []windowsMakeShellRetry{
		{"native-make.ps1", "Install native GNU Make", 3, 120, []int{15, 30}, 60,
			[]string{"$ChocolateyAttempts = 3", "$ChocolateyAttemptSeconds = 120", "$BackoffSeconds = @(15, 30)"}},
		{"msys2-packages.sh", "Install MSYS2 GNU Make", 3, 120, []int{15, 30}, 60,
			[]string{"attempts=3", "attempt_seconds=120", "backoff_seconds=(15 30)", `timeout --kill-after=10 "$attempt_seconds"`}},
	} {
		script := readPolicyFile(t, root, windowsMakeShellScriptDir+r.script)
		for _, want := range r.declarations {
			if !strings.Contains(script, want) {
				t.Errorf("%s does not declare %q", r.script, want)
			}
		}
		if len(r.backoff) != r.attempts-1 {
			t.Fatalf("%s: %d backoffs for %d attempts", r.script, len(r.backoff), r.attempts)
		}
		step := pr.step(t, r.step)
		if step.TimeoutMinutes*60 <= r.worstCaseSeconds() {
			t.Errorf("%s timeout-minutes = %d (%ds) does not exceed its worst case of %ds", r.step, step.TimeoutMinutes, step.TimeoutMinutes*60, r.worstCaseSeconds())
		}
		if step.TimeoutMinutes >= pr.TimeoutMinutes {
			t.Errorf("%s timeout-minutes = %d is not below the job's %d", r.step, step.TimeoutMinutes, pr.TimeoutMinutes)
		}
	}
	// Every other step that can reach the network carries its own cap too.
	for _, name := range []string{"Set up preinstalled MSYS2", "Install Cygwin GNU Make from the package cache", "Set up Cygwin GNU Make"} {
		if step := pr.step(t, name); step.TimeoutMinutes <= 0 || step.TimeoutMinutes >= pr.TimeoutMinutes {
			t.Errorf("%s timeout-minutes = %d, want 0 < it < %d", name, step.TimeoutMinutes, pr.TimeoutMinutes)
		}
	}
	for _, step := range saver.Steps {
		if strings.HasPrefix(step.Name, "Seed ") || step.Name == "Download Cygwin GNU Make" {
			if step.TimeoutMinutes <= 0 || step.TimeoutMinutes >= saver.TimeoutMinutes {
				t.Errorf("saver %s timeout-minutes = %d, want 0 < it < %d", step.Name, step.TimeoutMinutes, saver.TimeoutMinutes)
			}
		}
	}

	// Every capped step can run to its cap in one job (each leg's fallback
	// fails on its own), so the job cap must hold all of them plus the
	// uncapped steps, or the job is cancelled mid-leg and the later legs'
	// exercises never run. The uncapped steps (checkout, diagnostics, cache
	// keys, cache lookups and saves, and the exercises, about 30 s in all)
	// get windowsMakeShellUncappedMinutes; a cache restore gives up on a
	// stalled download after SEGMENT_DOWNLOAD_TIMEOUT_MINS (1 minute each).
	for _, j := range []struct {
		name string
		job  ciWorkflowJob
	}{{windowsMakeShellJob, pr}, {windowsMakeShellSaverJob, saver}} {
		capped := 0
		for _, step := range j.job.Steps {
			capped += step.TimeoutMinutes
		}
		if capped+windowsMakeShellUncappedMinutes >= j.job.TimeoutMinutes {
			t.Errorf("%s step caps sum to %d min; with %d min for the uncapped steps that is not below the job's timeout-minutes %d",
				j.name, capped, windowsMakeShellUncappedMinutes, j.job.TimeoutMinutes)
		}
		if j.job.TimeoutMinutes != windowsMakeShellJobTimeoutMinutes {
			t.Errorf("%s timeout-minutes = %d, want %d", j.name, j.job.TimeoutMinutes, windowsMakeShellJobTimeoutMinutes)
		}
	}
}

const (
	windowsMakeShellUncappedMinutes   = 5
	windowsMakeShellJobTimeoutMinutes = 40
)
