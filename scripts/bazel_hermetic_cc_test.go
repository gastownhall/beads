package scripts_test

import (
	"errors"
	"regexp"
	"strings"
	"testing"
)

// rules_go builds the Go stdlib (under -race) and every cgo package with the
// registered C toolchain, and every Go compile depends on the stdlib, so the
// C toolchain is an input of nearly every action key. A host-detected toolchain
// (local_config_cc) puts the host's gcc path, its lld availability and
// gcc-version-specific generated files into those keys, so developer, fork
// and CI runs never share results, and a client with lld (which the workers
// lack) fails every remote cgo/-race link. MODULE.bazel therefore registers LLVM and
// a sysroot fetched by sha256 (byte-identical on every Linux x86_64 host),
// and .bazelrc turns host detection off so nothing falls back to the host
// compiler silently.

const (
	hermeticCCModuleFile   = "MODULE.bazel"
	hermeticCCDetectOff    = "--repo_env=BAZEL_DO_NOT_DETECT_CPP_TOOLCHAIN=1"
	hermeticCCToolchains   = `register_toolchains("@llvm_toolchain//:all")`
	hermeticCCSysrootLabel = "@cc_sysroot_noble_amd64//:sysroot"
	hermeticCCLLVMLabel    = "@llvm_dist_linux_x86_64//:BUILD.bazel"
	// Every other host's stock LLVM release (toolchains_llvm's sha256 table).
	hermeticCCHostLLVMLabel = "@llvm_dist_host//:BUILD.bazel"
	// The members list tools/cc_toolchain/repack_llvm.sh slices the release
	// to; it must equal llvm_dist's members (MODULE.bazel).
	hermeticCCLLVMMembersFile = "tools/cc_toolchain/llvm_members.txt"
)

var (
	sha256HexRE = regexp.MustCompile(`^[0-9a-f]{64}$`)
	// snapshot.ubuntu.com serves each timestamp's archive state forever, so
	// the package pins stay resolvable at the snapshot they were taken from.
	ubuntuSnapshotRE = regexp.MustCompile(`^https://snapshot\.ubuntu\.com/ubuntu/\d{8}T\d{6}Z/\{path\}$`)
	// archive.ubuntu.com drops superseded versions: a fallback, never first.
	ubuntuMovingMirrorRE = regexp.MustCompile(`^https?://([a-z0-9-]+\.)*archive\.ubuntu\.com/`)
	// deb_sysroot's URL templates: {path} (pool path), {file} (its basename)
	// and {sha256} (the pin) are the only placeholders.
	debURLPlaceholderRE = regexp.MustCompile(`\{[^}]*\}`)
	// A sliced llvm_dist archive is a repack (repack_llvm.sh), not an
	// upstream release, so it may only come from a gastownhall release:
	// beads' own mirror or gascity's original asset (same bytes, same sha256).
	llvmSliceURLRE = regexp.MustCompile(`^https://github\.com/gastownhall/(beads|gascity)/releases/download/toolchain-llvm-[0-9.]+-slice-[0-9]+/[A-Za-z0-9._-]+\.tar\.zst$`)
	quotedStringRE = regexp.MustCompile(`"([^"]*)"`)
)

// moduleCall returns the argument text of the single top-level call
// `<fn>(\n...\n)` in MODULE.bazel whose arguments contain marker.
func moduleCall(module, fn, marker string) (string, error) {
	var found []string
	for _, m := range regexp.MustCompile(`(?ms)^`+regexp.QuoteMeta(fn)+`\(\n(.*?)^\)`).FindAllStringSubmatch(module, -1) {
		if strings.Contains(m[1], marker) {
			found = append(found, m[1])
		}
	}
	if len(found) != 1 {
		return "", errors.New(hermeticCCModuleFile + ": want exactly one " + fn + "(...) naming " + marker)
	}
	return found[0], nil
}

// quotedPairs returns the "key": "value" entries of a Starlark dict body.
func quotedPairs(s string) map[string]string {
	out := map[string]string{}
	for _, m := range regexp.MustCompile(`"([^"]+)":\s*"([^"]*)"`).FindAllStringSubmatch(s, -1) {
		out[m[1]] = m[2]
	}
	return out
}

func checkHermeticCCModule(module string) []error {
	var errs []error
	for _, want := range []string{
		`bazel_dep(name = "toolchains_llvm", `,
		hermeticCCToolchains,
	} {
		if !strings.Contains(module, want) {
			errs = append(errs, errors.New(hermeticCCModuleFile+" must contain "+want))
		}
	}
	for _, m := range regexp.MustCompile(`(?s)register_toolchains\((.*?)\)`).FindAllStringSubmatch(module, -1) {
		if strings.Contains(m[1], "local_config_cc") || strings.Contains(m[1], "@bazel_tools//tools/cpp") {
			errs = append(errs, errors.New(hermeticCCModuleFile+" registers a host-detected C/C++ toolchain: "+m[0]))
		}
	}

	if sysroot, err := moduleCall(module, "llvm.sysroot", hermeticCCSysrootLabel); err != nil {
		errs = append(errs, err)
	} else if !strings.Contains(sysroot, `targets = ["linux-x86_64"]`) {
		errs = append(errs, errors.New("llvm.sysroot must apply to linux-x86_64"))
	}
	if _, err := moduleCall(module, "llvm.toolchain_root", hermeticCCLLVMLabel); err != nil {
		errs = append(errs, err)
	}
	// toolchains_llvm creates no stock distribution once any toolchain_root
	// is set, and fails every build on a host no root covers ("LLVM toolchain
	// root missing"), so the other hosts (macOS, Linux arm64) need a root
	// without targets.
	if root, err := moduleCall(module, "llvm.toolchain_root", hermeticCCHostLLVMLabel); err != nil {
		errs = append(errs, err)
	} else if strings.Contains(root, "targets") {
		errs = append(errs, errors.New("llvm.toolchain_root naming "+hermeticCCHostLLVMLabel+" must be the fallback (no targets)"))
	}
	if dist, err := moduleCall(module, "llvm_host_dist", `name = "llvm_dist_host"`); err != nil {
		errs = append(errs, err)
	} else if !strings.Contains(dist, "llvm_version = LLVM_VERSION,") || !strings.Contains(dist, `llvm_versions = {"": LLVM_VERSION},`) {
		// The bare `llvm` rule reads llvm_versions (llvm_toolchain's macro
		// fills it in from llvm_version; use_repo_rule does not).
		errs = append(errs, errors.New("llvm_host_dist must set llvm_version and llvm_versions to LLVM_VERSION, whose per-host sha256 toolchains_llvm pins"))
	}

	if dist, err := moduleCall(module, "llvm_dist", `name = "llvm_dist_linux_x86_64"`); err != nil {
		errs = append(errs, err)
	} else if m := regexp.MustCompile(`sha256 = "([^"]*)"`).FindStringSubmatch(dist); m == nil || !sha256HexRE.MatchString(m[1]) {
		errs = append(errs, errors.New("llvm_dist must pin its archive by sha256"))
	}

	if _, err := moduleCall(module, "deb_sysroot", `name = "cc_sysroot_noble_amd64"`); err != nil {
		errs = append(errs, err)
	}
	for _, m := range regexp.MustCompile(`(?ms)^deb_sysroot\(\n(.*?)^\)`).FindAllStringSubmatch(module, -1) {
		errs = append(errs, checkDebSysroot(module, m[1])...)
	}
	return errs
}

// checkDebSysroot: every package is an amd64 pool .deb pinned by sha256 that
// ships no ICU, and its URL templates give each package more than one host,
// keep the snapshot the pins were taken from, and never start with a moving
// mirror.
func checkDebSysroot(module, call string) []error {
	var errs []error
	name := "deb_sysroot"
	if m := regexp.MustCompile(`name = "([^"]+)"`).FindStringSubmatch(call); m != nil {
		name += " " + m[1]
	}
	urls, err := debSysrootURLs(module, call)
	if err != nil {
		errs = append(errs, errors.New(name+": "+err.Error()))
	} else {
		hosts := map[string]bool{}
		snapshot := false
		for _, u := range urls {
			holes := debURLPlaceholderRE.FindAllString(u, -1)
			if len(holes) != 1 || (holes[0] != "{path}" && holes[0] != "{file}" && holes[0] != "{sha256}") {
				errs = append(errs, errors.New(name+": url "+u+" must name the package by exactly one of {path}, {file} or {sha256}"))
			}
			snapshot = snapshot || ubuntuSnapshotRE.MatchString(u)
			host, _, _ := strings.Cut(strings.TrimPrefix(u, "https://"), "/")
			hosts[host] = true
		}
		if !snapshot {
			errs = append(errs, errors.New(name+": urls must include an immutable https://snapshot.ubuntu.com/ubuntu/<timestamp>/{path}"))
		}
		if len(hosts) < 2 {
			errs = append(errs, errors.New(name+": urls must span at least two hosts, so one outage cannot fail a cold fetch"))
		}
		if len(urls) > 0 && ubuntuMovingMirrorRE.MatchString(urls[0]) {
			errs = append(errs, errors.New(name+": the first url must not be a moving mirror (archive.ubuntu.com drops superseded packages)"))
		}
	}
	body := regexp.MustCompile(`(?s)packages = \{(.*?)\}`).FindStringSubmatch(call)
	if body == nil {
		return append(errs, errors.New(name+" has no packages"))
	}
	pkgs := quotedPairs(body[1])
	if len(pkgs) == 0 {
		errs = append(errs, errors.New(name+" has no packages"))
	}
	for path, sum := range pkgs {
		if !strings.HasPrefix(path, "pool/") || !strings.HasSuffix(path, "_amd64.deb") || !sha256HexRE.MatchString(sum) {
			errs = append(errs, errors.New(name+" package "+path+" must be an amd64 pool .deb pinned by sha256"))
		}
		// ICU policy (engdocs/ICU-POLICY.md): nothing links libicu,
		// so the sysroot offers no ICU to link against.
		if strings.Contains(path, "/icu/") {
			errs = append(errs, errors.New(name+" package "+path+" ships ICU, which beads must never link (engdocs/ICU-POLICY.md)"))
		}
	}
	return errs
}

// debSysrootURLs returns a deb_sysroot call's url templates: an inline list,
// or a top-level MODULE.bazel list constant.
func debSysrootURLs(module, call string) ([]string, error) {
	m := regexp.MustCompile(`(?s)urls = (\[.*?\]|[A-Z][A-Z0-9_]*),`).FindStringSubmatch(call)
	if m == nil {
		return nil, errors.New("no urls")
	}
	list := m[1]
	if !strings.HasPrefix(list, "[") {
		def := regexp.MustCompile(`(?ms)^` + list + ` = (\[.*?^\])`).FindStringSubmatch(module)
		if def == nil {
			return nil, errors.New("urls names " + list + ", which MODULE.bazel does not define as a list")
		}
		list = def[1]
	}
	var urls []string
	for _, q := range regexp.MustCompile(`"([^"]+)"`).FindAllStringSubmatch(list, -1) {
		urls = append(urls, q[1])
	}
	if len(urls) == 0 {
		return nil, errors.New("no urls")
	}
	return urls, nil
}

// checkHermeticLLVMSlice: a sliced llvm_dist downloads only from a
// gastownhall release, and its members equal the repack script's list, so
// the pinned slice can be rebuilt from upstream and checked file by file.
func checkHermeticLLVMSlice(module, membersFile string) []error {
	dist, err := moduleCall(module, "llvm_dist", `name = "llvm_dist_linux_x86_64"`)
	if err != nil {
		return []error{err}
	}
	if !strings.Contains(dist, "sliced = True,") {
		return nil
	}
	var errs []error
	urls := regexp.MustCompile(`(?s)urls = \[(.*?)\]`).FindStringSubmatch(dist)
	if urls == nil || len(quotedStringRE.FindAllStringSubmatch(urls[1], -1)) == 0 {
		errs = append(errs, errors.New("sliced llvm_dist has no urls"))
	} else {
		for _, u := range quotedStringRE.FindAllStringSubmatch(urls[1], -1) {
			if !llvmSliceURLRE.MatchString(u[1]) {
				errs = append(errs, errors.New("sliced llvm_dist url "+u[1]+" is not a gastownhall/beads or gastownhall/gascity release .tar.zst"))
			}
		}
	}
	var members []string
	if body := regexp.MustCompile(`(?s)members = \[(.*?)\]`).FindStringSubmatch(dist); body != nil {
		for _, m := range quotedStringRE.FindAllStringSubmatch(body[1], -1) {
			members = append(members, m[1])
		}
	}
	var listed []string
	for _, line := range strings.Split(membersFile, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			listed = append(listed, line)
		}
	}
	if len(members) == 0 || strings.Join(members, "\n") != strings.Join(listed, "\n") {
		errs = append(errs, errors.New("llvm_dist members must equal "+hermeticCCLLVMMembersFile+" (same order), or the pinned slice cannot be rebuilt by repack_llvm.sh"))
	}
	return errs
}

// checkHermeticCCBazelRC: host detection is off unconditionally (every
// command, every config) and nothing hands actions a host compiler.
func checkHermeticCCBazelRC(rc string) []error {
	var errs []error
	off := false
	for _, line := range strings.Split(rc, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		command, config, _ := strings.Cut(fields[0], ":")
		for _, flag := range fields[1:] {
			if strings.HasPrefix(flag, "#") {
				break
			}
			name, value, _ := strings.Cut(flag, "=")
			envName, _, _ := strings.Cut(value, "=")
			switch {
			case flag == hermeticCCDetectOff && command == "common" && config == "":
				off = true
			case envName == "BAZEL_DO_NOT_DETECT_CPP_TOOLCHAIN" && flag != hermeticCCDetectOff:
				errs = append(errs, errors.New(".bazelrc: "+line+" re-enables host C/C++ toolchain detection"))
			case (name == "--repo_env" || name == "--action_env" || name == "--host_action_env") &&
				(envName == "CC" || envName == "CXX" || envName == "BAZEL_USE_CPP_ONLY_TOOLCHAIN"):
				errs = append(errs, errors.New(".bazelrc: "+line+" points C/C++ builds at a host compiler"))
			case (name == "--extra_toolchains" || name == "--crosstool_top" || name == "--host_crosstool_top") &&
				(strings.Contains(value, "local_config_cc") || strings.Contains(value, "@bazel_tools//tools/cpp")):
				errs = append(errs, errors.New(".bazelrc: "+line+" selects the host-detected C/C++ toolchain"))
			}
		}
	}
	if !off {
		errs = append(errs, errors.New(".bazelrc must set `common "+hermeticCCDetectOff+"` unconditionally"))
	}
	return errs
}

func TestBazelHermeticCCToolchain(t *testing.T) {
	root := bazelPolicyRoot(t)
	module := readPolicyFile(t, root, hermeticCCModuleFile)
	for _, err := range checkHermeticCCModule(module) {
		t.Error(err)
	}
	for _, err := range checkHermeticLLVMSlice(module, readPolicyFile(t, root, hermeticCCLLVMMembersFile)) {
		t.Error(err)
	}
	for _, err := range checkHermeticCCBazelRC(readPolicyFile(t, root, ".bazelrc")) {
		t.Error(err)
	}
}

func TestBazelHermeticCCToolchainGuards(t *testing.T) {
	sum := strings.Repeat("ab", 32)
	module := `bazel_dep(name = "rules_go", version = "0.63.0")
bazel_dep(name = "toolchains_llvm", version = "1.11.0")

NOBLE_DEB_URLS = [
    "https://github.com/gastownhall/gascity/releases/download/toolchain-noble-debs-20261001/{sha256}.deb",
    "https://snapshot.ubuntu.com/ubuntu/20261001T000000Z/{path}",
    "https://launchpad.net/ubuntu/+archive/primary/+files/{file}",
    "https://archive.ubuntu.com/ubuntu/{path}",
]

deb_sysroot(
    name = "cc_sysroot_noble_amd64",
    urls = NOBLE_DEB_URLS,
    packages = {
        "pool/main/g/glibc/libc6-dev_2.39-0ubuntu8.9_amd64.deb": "` + sum + `",
    },
)

llvm_dist(
    name = "llvm_dist_linux_x86_64",
    sha256 = "` + sum + `",
)

llvm_host_dist(
    name = "llvm_dist_host",
    llvm_version = LLVM_VERSION,
    llvm_versions = {"": LLVM_VERSION},
)

llvm.toolchain_root(
    name = "llvm_toolchain",
    label = "` + hermeticCCLLVMLabel + `",
    targets = ["linux-x86_64"],
)
llvm.toolchain_root(
    name = "llvm_toolchain",
    label = "` + hermeticCCHostLLVMLabel + `",
)
llvm.sysroot(
    name = "llvm_toolchain",
    label = "` + hermeticCCSysrootLabel + `",
    targets = ["linux-x86_64"],
)

` + hermeticCCToolchains + "\n"
	if errs := checkHermeticCCModule(module); len(errs) != 0 {
		t.Fatalf("good MODULE.bazel fixture: %v", errs)
	}
	for name, bad := range map[string]string{
		"no toolchains_llvm":       strings.Replace(module, `bazel_dep(name = "toolchains_llvm", version = "1.11.0")`, "", 1),
		"no registration":          strings.Replace(module, hermeticCCToolchains, "", 1),
		"second registration":      module + `register_toolchains("@local_config_cc//:all")` + "\n",
		"no sysroot":               strings.Replace(module, "llvm.sysroot(", "llvm.other(", 1),
		"host LLVM":                strings.Replace(module, "llvm.toolchain_root(", "llvm.other(", 1),
		"no other-host root":       strings.Replace(module, "label = \""+hermeticCCHostLLVMLabel+"\",", "", 1),
		"other-host root scoped":   strings.Replace(module, "label = \""+hermeticCCHostLLVMLabel+"\",", "label = \""+hermeticCCHostLLVMLabel+"\",\n    targets = [\"darwin-aarch64\"],", 1),
		"no other-host dist":       strings.Replace(module, "llvm_host_dist(", "other(", 1),
		"other-host dist version":  strings.Replace(module, "llvm_version = LLVM_VERSION", `llvm_version = "17.0.6"`, 1),
		"other-host dist versions": strings.Replace(module, `llvm_versions = {"": LLVM_VERSION},`, "", 1),
		"unpinned LLVM":            strings.Replace(module, `    sha256 = "`+sum+`",`+"\n)", "\n)", 1),
		"moving mirror first":      strings.Replace(module, `"https://github.com/gastownhall/gascity/releases/download/toolchain-noble-debs-20261001/{sha256}.deb",`, `"https://archive.ubuntu.com/ubuntu/{path}",`, 1),
		"no snapshot":              strings.Replace(module, `"https://snapshot.ubuntu.com/ubuntu/20261001T000000Z/{path}",`, "", 1),
		"moving snapshot":          strings.Replace(module, "/ubuntu/20261001T000000Z/{path}", "/ubuntu/{path}", 1),
		"single host":              strings.NewReplacer(`"https://github.com/gastownhall/gascity/releases/download/toolchain-noble-debs-20261001/{sha256}.deb",`, "", `"https://launchpad.net/ubuntu/+archive/primary/+files/{file}",`, "", `"https://archive.ubuntu.com/ubuntu/{path}",`, "").Replace(module),
		"package-blind url":        strings.Replace(module, "/+files/{file}", "/+files/libc6.deb", 1),
		"unknown placeholder":      strings.Replace(module, "/+files/{file}", "/+files/{name}", 1),
		"undefined url list":       strings.Replace(module, "urls = NOBLE_DEB_URLS,", "urls = OTHER_URLS,", 1),
		"no urls":                  strings.Replace(module, "urls = NOBLE_DEB_URLS,", "", 1),
		"unpinned package":         strings.Replace(module, `_amd64.deb": "`+sum+`"`, `_amd64.deb": ""`, 1),
		"ICU in sysroot":           strings.Replace(module, "    packages = {\n", "    packages = {\n        \"pool/main/i/icu/libicu74_74.2-1ubuntu3.1_amd64.deb\": \""+sum+"\",\n", 1),
		"sysroot other targets":    strings.Replace(module, "label = \""+hermeticCCSysrootLabel+"\",\n    targets = [\"linux-x86_64\"]", "label = \""+hermeticCCSysrootLabel+"\",\n    targets = []", 1),
	} {
		if len(checkHermeticCCModule(bad)) == 0 {
			t.Errorf("%s: expected an error for MODULE.bazel fixture:\n%s", name, bad)
		}
	}

	rc := "common --enable_bzlmod\ncommon " + hermeticCCDetectOff + "\ntest --test_output=errors\n"
	if errs := checkHermeticCCBazelRC(rc); len(errs) != 0 {
		t.Fatalf("good .bazelrc fixture: %v", errs)
	}
	for name, bad := range map[string]string{
		"detection on":          strings.Replace(rc, "common "+hermeticCCDetectOff+"\n", "", 1),
		"only for build":        strings.Replace(rc, "common "+hermeticCCDetectOff, "build "+hermeticCCDetectOff, 1),
		"only in a config":      strings.Replace(rc, "common "+hermeticCCDetectOff, "common:ci "+hermeticCCDetectOff, 1),
		"commented out":         strings.Replace(rc, "common "+hermeticCCDetectOff, "# common "+hermeticCCDetectOff, 1),
		"re-enabled":            rc + "build:remote-exec --repo_env=BAZEL_DO_NOT_DETECT_CPP_TOOLCHAIN=0\n",
		"host CC":               rc + "build --repo_env=CC=/usr/bin/gcc\n",
		"host CC action env":    rc + "build --action_env=CC\n",
		"local_config_cc":       rc + "build --extra_toolchains=@local_config_cc//:all\n",
		"bazel_tools crosstool": rc + "build --crosstool_top=@bazel_tools//tools/cpp:toolchain\n",
	} {
		if len(checkHermeticCCBazelRC(bad)) == 0 {
			t.Errorf("%s: expected an error for .bazelrc fixture:\n%s", name, bad)
		}
	}
}

func TestBazelHermeticLLVMSliceGuards(t *testing.T) {
	sum := strings.Repeat("ab", 32)
	slice := "https://github.com/gastownhall/gascity/releases/download/toolchain-llvm-22.1.8-slice-1/LLVM-22.1.8-Linux-X64-slice.tar.zst"
	mirror := "https://github.com/gastownhall/beads/releases/download/toolchain-llvm-22.1.8-slice-1/LLVM-22.1.8-Linux-X64-slice.tar.zst"
	module := `llvm_dist(
    name = "llvm_dist_linux_x86_64",
    members = [
        "bin/clang",
        # a comment
        "lib/clang/22/include",
    ],
    sha256 = "` + sum + `",
    sliced = True,
    urls = [
        "` + mirror + `",
        "` + slice + `",
    ],
)
`
	members := "bin/clang\nlib/clang/22/include\n"
	if errs := checkHermeticLLVMSlice(module, members); len(errs) != 0 {
		t.Fatalf("good sliced llvm_dist fixture: %v", errs)
	}
	unsliced := strings.Replace(strings.Replace(module, "    sliced = True,\n", "", 1), slice, "https://github.com/llvm/llvm-project/releases/download/llvmorg-22.1.8/LLVM-22.1.8-Linux-X64.tar.xz", 1)
	if errs := checkHermeticLLVMSlice(unsliced, members); len(errs) != 0 {
		t.Fatalf("unsliced llvm_dist needs no slice host or members file: %v", errs)
	}
	for name, bad := range map[string][2]string{
		"other host":        {strings.Replace(module, slice, "https://example.com/LLVM-22.1.8-Linux-X64-slice.tar.zst", 1), members},
		"other repo":        {strings.Replace(module, "gastownhall/gascity", "someone/gascity", 1), members},
		"xz release":        {strings.Replace(module, slice, "https://github.com/gastownhall/gascity/releases/download/toolchain-llvm-22.1.8-slice-1/LLVM.tar.xz", 1), members},
		"no urls":           {strings.Replace(strings.Replace(module, `"`+slice+`",`, "", 1), `"`+mirror+`",`, "", 1), members},
		"member missing":    {module, "bin/clang\n"},
		"member extra":      {module, members + "bin/lld\n"},
		"member reordered":  {module, "lib/clang/22/include\nbin/clang\n"},
		"empty member file": {module, ""},
	} {
		if len(checkHermeticLLVMSlice(bad[0], bad[1])) == 0 {
			t.Errorf("%s: expected an error for sliced llvm_dist fixture:\n%s\nmembers:\n%s", name, bad[0], bad[1])
		}
	}
}
