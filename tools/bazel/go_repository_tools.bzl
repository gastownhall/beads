"""gazelle's go_repository_tools, served from Bazel's repository cache.

Every go_deps repository (go_repository) runs two host binaries that gazelle
compiles with `go build` in a repository rule of its own,
@bazel_gazelle_go_repository_tools: fetch_repo, which downloads the module,
and gazelle, which writes its BUILD files. Nothing else can start until that
build finishes, and a fresh output base (every CI lane) pays it in full:
13-24 s of client loading on a 2-4 vCPU runner, before any go_deps fetch.

The build is reproducible (-trimpath, -buildvcs=false, CGO_ENABLED=0), so
for a given gazelle release, Go SDK and host its output is fixed bytes.
_PINS records those bytes' sha256. This rule, which MODULE.bazel substitutes
for gazelle's (override_repo), asks the repository cache for each pinned
digest first: a hit is copied out after Bazel re-hashes it, so a runner cache
restored from elsewhere can only ever yield the pinned bytes. On a miss (or
an unpinned gazelle, Go or host) it builds exactly as gazelle does, and when
the result matches the pins it enters the repository cache, so the next
fetch, and the next CI run that restores that cache, skips the build.

A gazelle or Go SDK bump changes the key: until _PINS gets the new entry
(the rule prints the digests to add), fetches build from source, as before.
"""

_TOOLS = ["fetch_repo", "gazelle", "generate_repo_config"]

# "<gazelle version>/<go version>/<os>_<arch>" -> {tool: sha256}.
_PINS = {
    "0.53.0/go1.26.7/linux_amd64": {
        "fetch_repo": "6780b48003af40859f849a4cd4ae291473565a057447591a6cbf111eb45ea9e1",
        "gazelle": "4c1300d28f01dbed14661010e6beb8d3595425a4ac4c57b97fd094bcf9d21e9f",
        "generate_repo_config": "6e21ac3f7af18822045718a70118a45ba9d48a964558c483ff013bbe569f6b2f",
    },
    "0.53.0/go1.26.9/linux_amd64": {
        "fetch_repo": "8b1288089311dc63c3d4b04c433d9ba11df5b09b5e8fdb1f8db0804f360ca6c0",
        "gazelle": "1b764f6d9ad08e324bdcab429c3105815b8914f289d594acfc791bdac2bc2b2c",
        "generate_repo_config": "8fab5073786aa587248822b73733a3edde6a33bd34d6ae69c274536811b5f74a",
    },
}

# gazelle's go_repository_tools.bzl _GO_REPOSITORY_TOOLS_BUILD_FILE.
_BUILD_FILE = """\
package(default_visibility = ["//visibility:public"])

filegroup(
    name = "fetch_repo",
    srcs = ["bin/fetch_repo{ext}"],
)

filegroup(
    name = "gazelle",
    srcs = ["bin/gazelle{ext}"],
)

filegroup(
    name = "generate_repo_config",
    srcs = ["bin/generate_repo_config{ext}"],
)

exports_files(["ROOT"])
"""

# Never fetched: a repository cache miss must fall through to the source
# build, not to the network.
_NO_URL = "file:///nonexistent/go_repository_tools"

def _host(rctx):
    os = rctx.os.name.lower()
    if os.startswith("linux"):
        os = "linux"
    elif os.startswith("mac"):
        os = "darwin"
    elif os.startswith("windows"):
        os = "windows"
    arch = {"x86_64": "amd64", "aarch64": "arm64"}.get(rctx.os.arch, rctx.os.arch)
    return os + "_" + arch

def _gazelle_version(rctx):
    for line in rctx.read(rctx.attr._gazelle_module).splitlines():
        line = line.strip()
        if line.startswith("version = \""):
            return line.split("\"")[1]
    fail("no version in @gazelle//:MODULE.bazel")

def _go(rctx, go, args, env, **kwargs):
    """Runs go with exactly env, like gazelle's env_execute: no client GOFLAGS, GOARCH, ..."""
    env = dict(env, GOROOT = str(go.dirname.dirname), GOTOOLCHAIN = "local")
    if rctx.os.name.lower().startswith("windows"):
        return rctx.execute([str(go)] + args, environment = env, **kwargs)
    return rctx.execute(["env", "-i"] + ["%s=%s" % kv for kv in env.items()] + [str(go)] + args, **kwargs)

def _build(rctx, go, gazelle_version, out_dir):
    """gazelle's go_repository_tools build (its go.env, flags and packages), into out_dir."""
    scratch = rctx.path("_scratch")
    env = {
        "GOCACHE": str(scratch) + "/gocache",
        "GOPATH": str(scratch) + "/gopath",
        "GOROOT_FINAL": "GOROOT",
        "CGO_ENABLED": "0",
        "HOME": str(scratch),
    }
    for k in ["PATH", "GOPROXY", "TMP", "TMPDIR"]:
        v = rctx.getenv(k)
        if v:
            env[k] = v
    ldflags = [
        "-w",
        "-s",
        "-X",
        "github.com/bazel-contrib/bazel-gazelle/v2/cmd/gazelle/update.BazelModuleVersion=" + gazelle_version,
        "-X",
        "github.com/bazel-contrib/bazel-gazelle/v2/cmd/gazelle/update.IsBazelModule=True",
    ]
    result = _go(
        rctx,
        go,
        [
            "build",
            "-o",
            str(rctx.path(out_dir)) + "/",
            "-ldflags",
            " ".join(ldflags),
            "-trimpath",
            "-buildvcs=false",
            "github.com/bazel-contrib/bazel-gazelle/v2/cmd/gazelle",
            "github.com/bazelbuild/bazel-gazelle/cmd/fetch_repo",
            "github.com/bazelbuild/bazel-gazelle/cmd/generate_repo_config",
        ],
        env,
        working_directory = str(rctx.path(rctx.attr._gazelle_module).dirname),
    )
    if result.return_code:
        fail("building gazelle's go_repository tools: " + result.stderr)

    # The module cache is read-only; make it removable.
    rctx.execute(["chmod", "-R", "u+w", str(scratch)])
    rctx.delete(scratch)

def _sha256(rctx, path):
    result = rctx.execute(["sha256sum", str(path)])
    return result.stdout.split(" ")[0] if result.return_code == 0 else "unknown"

def _go_repository_tools_impl(rctx):
    ext = ".exe" if rctx.os.name.lower().startswith("windows") else ""
    go = rctx.path(Label("@go_sdk//:bin/go" + ext))
    rctx.watch(go)
    result = _go(rctx, go, ["env", "GOVERSION"], {})
    if result.return_code:
        fail("go env GOVERSION: " + result.stderr)
    gazelle_version = _gazelle_version(rctx)
    key = "%s/%s/%s" % (gazelle_version, result.stdout.strip(), _host(rctx))
    pins = _PINS.get(key)
    names = {tool: "%s%s" % (tool, ext) for tool in _TOOLS}

    rctx.file("BUILD.bazel", _BUILD_FILE.format(ext = ext), executable = False)
    rctx.file("ROOT", "", executable = False)
    metadata = rctx.repo_metadata(reproducible = True) if hasattr(rctx, "repo_metadata") else None

    if not pins:
        _build(rctx, go, gazelle_version, "bin")
        sums = {tool: _sha256(rctx, rctx.path("bin/" + names[tool])) for tool in _TOOLS}

        # buildifier: disable=print
        print("go_repository_tools: no pins for %s, built from source. To serve it from the repository cache, add %r: %r to _PINS in tools/bazel/go_repository_tools.bzl." % (key, key, sums))
        return metadata

    # The repository cache, by pinned digest.
    if all([
        rctx.download(
            url = _NO_URL,
            output = "bin/" + names[tool],
            sha256 = pins[tool],
            executable = True,
            allow_fail = True,
        ).success
        for tool in _TOOLS
    ]):
        return metadata

    # A miss: build, and let the downloader check each binary against its
    # pin, which also stores a match in the repository cache.
    rctx.delete("bin")
    _build(rctx, go, gazelle_version, "_build")
    stale = {}
    for tool in _TOOLS:
        built = rctx.path("_build/" + names[tool])
        if not rctx.download(
            url = "file://" + str(built),
            output = "bin/" + names[tool],
            sha256 = pins[tool],
            executable = True,
            allow_fail = True,
        ).success:
            stale[tool] = _sha256(rctx, built)
    if stale:
        # buildifier: disable=print
        print("go_repository_tools: %s built from source with digests %r, not _PINS[%r]; the build is used but not cached. Update _PINS in tools/bazel/go_repository_tools.bzl." % (key, stale, key))
        rctx.delete("bin")
        for tool in _TOOLS:
            rctx.symlink("_build/" + names[tool], "bin/" + names[tool])
    else:
        rctx.delete("_build")
    return metadata

go_repository_tools = repository_rule(
    implementation = _go_repository_tools_impl,
    doc = "gazelle's go_repository_tools (fetch_repo, gazelle, generate_repo_config), served from the repository cache when pinned.",
    attrs = {
        "_gazelle_module": attr.label(default = "@gazelle//:MODULE.bazel"),
    },
)
