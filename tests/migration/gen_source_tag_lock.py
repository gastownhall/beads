#!/usr/bin/env python3
"""Regenerate source_tag_modules.json: the Go module proxy files that build the
source-tag SQLite release (v0.9.1, which has no release asset) offline.

Usage: python3 tests/migration/gen_source_tag_lock.py

It fetches the release's module zip from proxy.golang.org, checks it against
the release catalog's source_zip sha256, reads the go.sum inside it, and
records the sha256 of every .mod and .zip that go.sum names (plus the
release's own .mod and .zip). tools/bazel/go_module_proxy.bzl downloads
exactly these files, verified, into a file:// GOPROXY tree; go then checks
every dependency against that go.sum as well. Network required; run it by
hand when the source-tag release changes, never in CI.
"""

import hashlib
import io
import json
import os
import sys
import urllib.request
import zipfile

ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
CATALOG = os.path.join(ROOT, "scripts", "migration-test", "release-catalog.json")
OUT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "source_tag_modules.json")
PROXY = "https://proxy.golang.org"
MODULE = "github.com/steveyegge/beads"
VERSION = "v0.9.1"


def escape(path):
    """Module path case-encoding of the proxy protocol (go.dev/ref/mod#goproxy-protocol)."""
    return "".join("!" + c.lower() if c.isupper() else c for c in path)


def fetch(url):
    with urllib.request.urlopen(url, timeout=60) as resp:
        return resp.read()


def sha256(data):
    return hashlib.sha256(data).hexdigest()


def main():
    with open(CATALOG) as f:
        catalog = json.load(f)
    entry = next(e for e in catalog["versions"] if e["version"] == VERSION)
    base = f"{PROXY}/{escape(MODULE)}/@v/{VERSION}"
    zip_data = fetch(base + ".zip")
    if sha256(zip_data) != entry["source_zip"]["sha256"]:
        sys.exit(f"{MODULE}@{VERSION}.zip does not match the catalog's source_zip sha256")
    gosum = zipfile.ZipFile(io.BytesIO(zip_data)).read(f"{MODULE}@{VERSION}/go.sum").decode()

    wants = {}  # (path, version) -> needs zip
    for line in gosum.splitlines():
        fields = line.split()
        if len(fields) != 3:
            continue
        path, version = fields[0], fields[1]
        if version.endswith("/go.mod"):
            wants.setdefault((path, version[: -len("/go.mod")]), False)
        else:
            wants[(path, version)] = True

    modules = [{
        "path": MODULE,
        "version": VERSION,
        "mod_sha256": sha256(fetch(base + ".mod")),
        "zip_sha256": entry["source_zip"]["sha256"],
    }]
    for (path, version), needs_zip in sorted(wants.items()):
        url = f"{PROXY}/{escape(path)}/@v/{version}"
        m = {"path": path, "version": version, "mod_sha256": sha256(fetch(url + ".mod"))}
        if needs_zip:
            m["zip_sha256"] = sha256(fetch(url + ".zip"))
        modules.append(m)
        print(f"{path}@{version}", file=sys.stderr)

    with open(OUT, "w") as f:
        json.dump({"module": MODULE, "version": VERSION, "modules": modules}, f, indent=2, sort_keys=True)
        f.write("\n")


if __name__ == "__main__":
    main()
