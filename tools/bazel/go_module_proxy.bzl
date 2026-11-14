"""An offline Go module proxy tree, for building a pinned historical module.

go_module_proxy reads a lock file (tests/migration/source_tag_modules.json,
written by tests/migration/gen_source_tag_lock.py) and downloads every .mod
and .zip it lists from proxy.golang.org, each verified against its recorded
sha256, into proxy/<escaped path>/@v/<version>.{mod,zip} (plus a .info naming the
version): the layout a
`GOPROXY=file://<dir>/proxy` reads. A build action then needs no network,
and go additionally checks every dependency against the module's own go.sum.
The repository exports the tree as :proxy.
"""

_PROXY = "https://proxy.golang.org"

_BUILD = """\
filegroup(
    name = "proxy",
    srcs = glob(["proxy/**"]),
    visibility = ["//visibility:public"],
)
"""

def _escape(path):
    # Module path case-encoding (go.dev/ref/mod#goproxy-protocol).
    out = ""
    for c in path.elems():
        out += "!" + c.lower() if c.isupper() else c
    return out

def _go_module_proxy_impl(rctx):
    lock = json.decode(rctx.read(rctx.attr.lock))
    for m in lock["modules"]:
        rel = "{}/@v/{}".format(_escape(m["path"]), m["version"])
        for ext, key in ((".mod", "mod_sha256"), (".zip", "zip_sha256")):
            if key not in m:
                continue
            url = "{}/{}{}".format(_PROXY, rel, ext)
            rctx.download(
                url = url,
                output = "proxy/{}{}".format(rel, ext),
                sha256 = m[key],
                canonical_id = url,
            )
        # .info only names the version (the proxy's also carries a commit
        # time); `go mod download module@version` reads it, the build does not.
        rctx.file("proxy/{}.info".format(rel), json.encode({"Version": m["version"]}) + "\n")
    rctx.file("BUILD.bazel", _BUILD)

go_module_proxy = repository_rule(
    implementation = _go_module_proxy_impl,
    attrs = {
        "lock": attr.label(mandatory = True, allow_single_file = True, doc = "The module lock JSON."),
    },
    doc = "Downloads the sha256-pinned module proxy files a lock file lists into a file:// GOPROXY tree.",
)
