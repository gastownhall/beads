#!/usr/bin/env python3
"""Emit the check-run payload that records a beta's pre-tag gate verdict.

Part of the release-channel proposal for gastownhall/beads (issue #5867),
called by .github/workflows/channel-beta.yml.

Why this exists at all: the pre-tag gates run against the *source* tree, but
the beta commit is that tree plus a version bump — so the gates' own check runs
are recorded against a different SHA than the one a promotion will be judged
on. Phase 2's dossier reads check runs on the beta commit. Without publishing
the verdict there explicitly, the dossier silently under-reports and the
promotion looks less gated than it was.

Values arrive by environment variable so that nothing is quoted inside the
workflow YAML, which is where this sort of payload usually goes wrong.
"""

import json
import os
import sys

REQUIRED = ("CR_SHA", "CR_SRC", "CR_URL")

missing = [k for k in REQUIRED if not os.environ.get(k)]
if missing:
    sys.stderr.write("missing required environment: %s\n" % ", ".join(missing))
    raise SystemExit(1)

sha = os.environ["CR_SHA"]
src = os.environ["CR_SRC"]
url = os.environ["CR_URL"]

summary = (
    "Ran against {src}, the tree this beta bumps.\n\n"
    "Gates in this run: full test suite, differential regression against the "
    "pinned baseline, migration hygiene, and the MCP and npm package gates. "
    "These run here because they fire on pushes to `main` only and would "
    "otherwise never see the candidate.\n\n"
    "Reported separately, triggered by the tag: the 30-release cross-version "
    "upgrade matrix, the migration harness, and the prerelease publish.\n\n"
    "Run: {url}"
).format(src=src, url=url)

json.dump(
    {
        "name": "Beta release gate (pre-tag)",
        "head_sha": sha,
        "status": "completed",
        "conclusion": "success",
        "details_url": url,
        "output": {
            "title": "Full suite, regression, migration hygiene, package gates",
            "summary": summary,
        },
    },
    sys.stdout,
    indent=2,
)
sys.stdout.write("\n")
