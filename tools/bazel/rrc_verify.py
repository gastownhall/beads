#!/usr/bin/env python3
"""Verify rbe-west's remote repo contents cache against a cold fetch (ga-vnycm2.26).

Bazel's --experimental_remote_repo_contents_cache stores each reproducible
external repository as the ActionResult of a synthetic, never executed action
keyed by the repository rule's predeclared-inputs hash
(RemoteRepoContentsCacheImpl, engdocs/design/bazel-remote-repo-contents-cache.md).
Nothing on the reader checks that the cached tree is what the rule produces:
that is the work the cache skips. So, nightly, this job fetches every lane's
repositories cold, without the cache, and compares each repository's tree and
marker file with the entry a lane would read for the same key. A mismatch is
a poisoned entry or a repository rule falsely marked reproducible (design
R1/R2); both must be looked at by a human.

Inputs: the cold output base (setup-bazel's rc: --repo_contents_cache= keeps
each repository in external/<name> with its marker external/@<name>.marker)
and read access to the cache (rbe-west's trusted edge with the CI client
certificate, or any REAPI endpoint). For each marker, it walks the action
cache as the reader does: the final entry under the predeclared hash, or
intermediate entries whose stdout lists batches of recorded inputs, whose
values it takes from the cold marker and rolls into the next key
(Fingerprint: sha256 over length-prefixed UTF-8 strings). The final entry's
marker must equal the cold marker byte for byte, and its tree (the Tree
proto, read from the CAS) must hold the same names, file digests, executable
bits and symlink targets as the cold directory.

Transport: unary and server-streaming gRPC over curl's HTTP/2 with the
standard library's protobuf-free wire decoding, as tools/rbe/cache-zstd-probe.sh
does: no grpc package on a job that holds a farm credential.

Exit status: 0 every cached repository matches; 1 a mismatch or an invalid
entry; 2 a usage error or too many transport failures to tell.
"""

from __future__ import annotations

import argparse
import concurrent.futures
import dataclasses
import hashlib
import json
import os
import stat
import subprocess
import sys
import tempfile
from typing import Callable, Iterable, Optional

# RemoteRepoContentsCacheImpl.GUID per Bazel release: the synthetic Command's
# only argument, so every key depends on it. A .bazelversion bump adds its
# release here (scripts/bazel_rrc_test.go checks .bazelversion has one).
BAZEL_RRC_GUIDS = {
    "9.3.0": "06a53d89-9f52-46ed-8064-f7af6ba27769",
}
MARKER_FILE_PATH = ".recorded_inputs"
REPO_DIRECTORY_PATH = "repo_contents"
EMPTY_SHA256 = hashlib.sha256(b"").hexdigest()
GRPC_NOT_FOUND = 5
MAX_CHAIN = 64  # intermediate hops per repository
MAX_DIFFS = 20  # per repository, in the report


# --- protobuf wire format ---------------------------------------------------


def _varint(n: int) -> bytes:
    out = bytearray()
    while True:
        b = n & 0x7F
        n >>= 7
        if n:
            out.append(b | 0x80)
        else:
            out.append(b)
            return bytes(out)


def _field(num: int, payload: bytes) -> bytes:
    return _varint(num << 3 | 2) + _varint(len(payload)) + payload


def _field_varint(num: int, v: int) -> bytes:
    return _varint(num << 3) + _varint(v)


def _read_varint(b: bytes, i: int) -> tuple[int, int]:
    v = s = 0
    while True:
        if i >= len(b) or s > 63:
            raise ValueError("truncated varint")
        c = b[i]
        i += 1
        v |= (c & 0x7F) << s
        s += 7
        if c < 0x80:
            return v, i


def fields(b: bytes) -> Iterable[tuple[int, int, object]]:
    """Yields (field number, wire type, value) for one message."""
    i = 0
    while i < len(b):
        key, i = _read_varint(b, i)
        num, wire = key >> 3, key & 7
        if wire == 0:
            v, i = _read_varint(b, i)
        elif wire == 1:
            v, i = b[i : i + 8], i + 8
        elif wire == 2:
            n, i = _read_varint(b, i)
            v, i = b[i : i + n], i + n
        elif wire == 5:
            v, i = b[i : i + 4], i + 4
        else:
            raise ValueError("wire type %d" % wire)
        if i > len(b):
            raise ValueError("truncated field %d" % num)
        yield num, wire, v


@dataclasses.dataclass(frozen=True)
class Digest:
    hash: str
    size: int

    def encode(self) -> bytes:
        out = _field(1, self.hash.encode())
        if self.size:
            out += _field_varint(2, self.size)
        return out

    @staticmethod
    def decode(b: bytes) -> "Digest":
        h, size = "", 0
        for num, _, v in fields(b):
            if num == 1:
                h = bytes(v).decode()
            elif num == 2:
                size = int(v)
        return Digest(h, size)

    @staticmethod
    def of(data: bytes) -> "Digest":
        return Digest(hashlib.sha256(data).hexdigest(), len(data))


def command_bytes(guid: str) -> bytes:
    """RemoteRepoContentsCacheImpl.COMMAND, serialized in field order."""
    return (
        _field(1, guid.encode())  # arguments
        + _field(3, MARKER_FILE_PATH.encode())  # output_files
        + _field(4, REPO_DIRECTORY_PATH.encode())  # output_directories
        + _field(5, b"")  # platform: the default instance, set
        + _field(7, MARKER_FILE_PATH.encode())  # output_paths
        + _field(7, REPO_DIRECTORY_PATH.encode())
    )


def action_digest(guid: str, input_hash: str) -> Digest:
    """The AC key the cache uses for input_hash (buildAction)."""
    action = (
        _field(1, Digest.of(command_bytes(guid)).encode())  # command_digest
        + _field(2, Digest(EMPTY_SHA256, 0).encode())  # input_root_digest
        + _field(9, input_hash.encode("latin-1"))  # salt
        + _field(10, b"")  # platform
    )
    return Digest.of(action)


def _bazel_string(s: str) -> bytes:
    # Bazel holds file contents read as ISO-8859-1 in Java Strings and
    # Fingerprint.addString writes those Strings as UTF-8.
    return s.encode("utf-8")


def roll_forward(hash_: str, recorded_input_with_value: str) -> str:
    """RemoteRepoContentsCacheImpl.rollForwardHash."""
    out = hashlib.sha256()
    for s in (hash_, recorded_input_with_value):
        b = _bazel_string(s)
        out.update(_varint(len(b)) + b)
    return out.hexdigest()


# --- REAPI messages ---------------------------------------------------------


@dataclasses.dataclass
class ActionResult:
    exit_code: int = 0
    output_files: list = dataclasses.field(default_factory=list)  # (path, Digest, is_executable, contents)
    output_directories: list = dataclasses.field(default_factory=list)  # (path, tree Digest)
    symlinks: int = 0
    stdout_raw: bytes = b""
    stdout_digest: Optional[Digest] = None
    stderr_size: int = 0

    @staticmethod
    def decode(b: bytes) -> "ActionResult":
        r = ActionResult()
        for num, _, v in fields(b):
            if num == 2:
                path, dg, exe, contents = "", None, False, b""
                for n2, _, v2 in fields(v):
                    if n2 == 1:
                        path = bytes(v2).decode()
                    elif n2 == 2:
                        dg = Digest.decode(v2)
                    elif n2 == 4:
                        exe = bool(v2)
                    elif n2 == 5:
                        contents = bytes(v2)
                r.output_files.append((path, dg, exe, contents))
            elif num == 3:
                path, tree = "", None
                for n2, _, v2 in fields(v):
                    if n2 == 1:
                        path = bytes(v2).decode()
                    elif n2 == 3:
                        tree = Digest.decode(v2)
                r.output_directories.append((path, tree))
            elif num == 4:
                r.exit_code = int(v)
            elif num == 5:
                r.stdout_raw = bytes(v)
            elif num == 6:
                r.stdout_digest = Digest.decode(v)
            elif num in (7, 8):
                r.stderr_size += len(v)
            elif num in (10, 11, 12):
                r.symlinks += 1
        return r


@dataclasses.dataclass
class Directory:
    files: dict  # name -> (Digest, is_executable)
    dirs: dict  # name -> Digest
    symlinks: dict  # name -> target

    @staticmethod
    def decode(b: bytes) -> "Directory":
        d = Directory({}, {}, {})
        for num, _, v in fields(b):
            name, dg, exe, target = "", None, False, ""
            for n2, _, v2 in fields(v):
                if n2 == 1:
                    name = bytes(v2).decode("utf-8", "surrogateescape")
                elif n2 == 2 and num in (1, 2):
                    dg = Digest.decode(v2)
                elif n2 == 2 and num == 3:
                    target = bytes(v2).decode("utf-8", "surrogateescape")
                elif n2 == 4 and num == 1:
                    exe = bool(v2)
            if num == 1:
                d.files[name] = (dg, exe)
            elif num == 2:
                d.dirs[name] = dg
            elif num == 3:
                d.symlinks[name] = target
        return d


def decode_tree(b: bytes) -> tuple[Directory, dict]:
    """Tree -> (root, {Directory digest hash: Directory})."""
    root, children = None, {}
    for num, _, v in fields(b):
        if num == 1:
            root = Directory.decode(v)
        elif num == 2:
            children[hashlib.sha256(v).hexdigest()] = Directory.decode(v)
    if root is None:
        raise ValueError("Tree without a root")
    return root, children


# --- transport ----------------------------------------------------------------


class RPCError(Exception):
    def __init__(self, code: int, message: str):
        super().__init__("gRPC status %d: %s" % (code, message))
        self.code = code


def _frame(msg: bytes) -> bytes:
    return b"\0" + len(msg).to_bytes(4, "big") + msg


def _unframe(body: bytes) -> list[bytes]:
    out, i = [], 0
    while i < len(body):
        if i + 5 > len(body) or body[i] != 0:
            raise ValueError("not an uncompressed gRPC frame")
        n = int.from_bytes(body[i + 1 : i + 5], "big")
        out.append(body[i + 5 : i + 5 + n])
        i += 5 + n
    if i != len(body):
        raise ValueError("truncated gRPC frame")
    return out


class CurlTransport:
    """gRPC over curl's HTTP/2: grpcs:// with optional mTLS, or grpc://."""

    def __init__(self, endpoint: str, cert: str = "", key: str = "", ca: str = "", timeout: int = 60):
        if endpoint.startswith("grpcs://"):
            self.base = "https://" + endpoint[len("grpcs://") :]
            self.tls = True
        elif endpoint.startswith("grpc://"):
            self.base = "http://" + endpoint[len("grpc://") :]
            self.tls = False
        else:
            raise ValueError("endpoint must be grpcs:// or grpc://, got %r" % endpoint)
        self.base = self.base.rstrip("/")
        self.cert, self.key, self.ca, self.timeout = cert, key, ca, timeout

    def call(self, method: str, request: bytes) -> list[bytes]:
        with tempfile.TemporaryDirectory() as tmp:
            req, head, body = (os.path.join(tmp, n) for n in ("req", "head", "body"))
            with open(req, "wb") as f:
                f.write(_frame(request))
            cmd = ["curl", "-sS", "--connect-timeout", "10", "--max-time", str(self.timeout)]
            cmd += ["--http2"] if self.tls else ["--http2-prior-knowledge"]
            if self.cert:
                cmd += ["--cert", self.cert, "--key", self.key]
            if self.ca:
                cmd += ["--cacert", self.ca]
            cmd += [
                "-H", "content-type: application/grpc", "-H", "te: trailers",
                "--data-binary", "@" + req, "-D", head, "-o", body, "-w", "%{http_code}",
                self.base + "/" + method,
            ]  # fmt: skip
            p = subprocess.run(cmd, capture_output=True, text=True, check=False)
            if p.returncode != 0:
                raise RPCError(-1, "curl exit %d: %s" % (p.returncode, p.stderr.strip()))
            if p.stdout != "200":
                raise RPCError(-1, "HTTP %s" % p.stdout)
            headers = read_bytes(head).decode("latin-1").replace("\r", "")
            status, message = None, ""
            for line in headers.split("\n"):
                k, _, v = line.partition(":")
                if k.strip().lower() == "grpc-status":
                    status = int(v.strip())
                elif k.strip().lower() == "grpc-message":
                    message = v.strip()
            if status is None:
                raise RPCError(-1, "no grpc-status")
            if status != 0:
                raise RPCError(status, message)
            return _unframe(read_bytes(body))


class Cache:
    """The few REAPI reads the reader makes."""

    def __init__(self, rpc: Callable[[str, bytes], list[bytes]], instance: str):
        self.rpc, self.instance = rpc, instance

    def action_result(self, key: Digest) -> Optional[ActionResult]:
        req = (
            _field(1, self.instance.encode())
            + _field(2, key.encode())
            + _field_varint(3, 1)  # inline_stdout
            + _field(5, MARKER_FILE_PATH.encode())  # inline_output_files
        )
        try:
            msgs = self.rpc("build.bazel.remote.execution.v2.ActionCache/GetActionResult", req)
        except RPCError as e:
            if e.code == GRPC_NOT_FOUND:
                return None
            raise
        if len(msgs) != 1:
            raise RPCError(-1, "GetActionResult: %d messages" % len(msgs))
        return ActionResult.decode(msgs[0])

    def blob(self, d: Digest) -> bytes:
        if d.size == 0:
            return b""
        name = "%s/blobs/%s/%d" % (self.instance, d.hash, d.size) if self.instance else "blobs/%s/%d" % (d.hash, d.size)
        msgs = self.rpc("google.bytestream.ByteStream/Read", _field(1, name.encode()))
        data = b"".join(bytes(v) for m in msgs for num, _, v in fields(m) if num == 10)
        if Digest.of(data) != d:
            raise RPCError(-1, "blob %s/%d: got %d bytes that do not match" % (d.hash, d.size, len(data)))
        return data


# --- verification ---------------------------------------------------------------


@dataclasses.dataclass
class Result:
    repo: str
    status: str  # match, mismatch, invalid, not_cached, error
    detail: list = dataclasses.field(default_factory=list)


def read_marker(path: str) -> tuple[str, dict]:
    """(predeclared hash, {recorded input: its 'input value' line})."""
    text = read_bytes(path).decode("latin-1")
    lines = text.split("\n")
    recorded = {}
    for line in lines[1:]:
        if not line:
            continue
        inp, sep, _ = line.partition(" ")
        if sep:
            recorded[inp] = line
    return lines[0].strip(), recorded


def find_final(cache: Cache, guid: str, predeclared: str, recorded: dict) -> tuple[Optional[ActionResult], list]:
    """Walks the AC from predeclared like fetchFinalCacheEntry; returns (final entry or None, invalid reasons)."""
    invalid, current, seen = [], [predeclared], set()
    for _ in range(MAX_CHAIN):
        nxt = []
        for h in current:
            if h in seen:
                continue
            seen.add(h)
            ar = cache.action_result(action_digest(guid, h))
            if ar is None:
                continue
            if ar.exit_code != 0:
                invalid.append("entry %s: exit code %d" % (h, ar.exit_code))
                continue
            if len(ar.output_files) == 1 and len(ar.output_directories) == 1 and ar.symlinks == 0:
                return ar, invalid
            if ar.output_files or ar.output_directories or ar.symlinks or not (ar.stdout_raw or (ar.stdout_digest and ar.stdout_digest.size)):
                invalid.append("entry %s: neither a final nor an intermediate result" % h)
                continue
            stdout = ar.stdout_raw if ar.stdout_raw else cache.blob(ar.stdout_digest)
            for batch in stdout.decode("latin-1").splitlines():
                rolling = h
                for inp in batch.split(" "):
                    line = recorded.get(inp)
                    if line is None:
                        break  # the cold fetch recorded other inputs: not this branch
                    rolling = roll_forward(rolling, line)
                else:
                    nxt.append(rolling)
        if not nxt:
            return None, invalid
        current = nxt
    invalid.append("more than %d intermediate hops" % MAX_CHAIN)
    return None, invalid


def read_bytes(path: str) -> bytes:
    with open(path, "rb") as f:
        return f.read()


def _sha256_file(path: str) -> tuple[str, int]:
    h, n = hashlib.sha256(), 0
    with open(path, "rb") as f:
        while chunk := f.read(1 << 20):
            h.update(chunk)
            n += len(chunk)
    return h.hexdigest(), n


def compare_tree(root: Directory, children: dict, local: str) -> list:
    """Differences between the cached tree and the cold directory local."""
    diffs = []

    def walk(d: Directory, path: str, rel: str) -> None:
        if len(diffs) >= MAX_DIFFS:
            return
        try:
            names = set(os.listdir(path))
        except OSError as e:
            diffs.append("%s: cannot list the cold directory (%s)" % (rel or ".", e))
            return
        remote = set(d.files) | set(d.dirs) | set(d.symlinks)
        for n in sorted(remote - names):
            diffs.append("%s: only in the cache" % os.path.join(rel, n))
        for n in sorted(names - remote):
            diffs.append("%s: only in the cold fetch" % os.path.join(rel, n))
        for n in sorted(remote & names):
            if len(diffs) >= MAX_DIFFS:
                return
            p, r = os.path.join(path, n), os.path.join(rel, n)
            st = os.lstat(p)
            if n in d.symlinks:
                if not stat.S_ISLNK(st.st_mode):
                    diffs.append("%s: a symlink in the cache, not in the cold fetch" % r)
                elif os.readlink(p) != d.symlinks[n]:
                    diffs.append("%s: symlink to %r in the cache, %r cold" % (r, d.symlinks[n], os.readlink(p)))
                continue
            try:
                st = os.stat(p)  # the upload follows symlinks it does not keep
            except OSError as e:
                diffs.append("%s: dangling in the cold fetch (%s)" % (r, e))
                continue
            if n in d.files:
                dg, exe = d.files[n]
                if not stat.S_ISREG(st.st_mode):
                    diffs.append("%s: a file in the cache, not in the cold fetch" % r)
                    continue
                h, size = _sha256_file(p)
                if dg is None or (h, size) != (dg.hash, dg.size):
                    diffs.append("%s: content differs (cache %s/%s, cold %s/%d)" % (r, dg and dg.hash, dg and dg.size, h, size))
                if exe != bool(st.st_mode & stat.S_IXUSR):
                    diffs.append("%s: executable bit differs (cache %s)" % (r, exe))
            else:
                if not stat.S_ISDIR(st.st_mode):
                    diffs.append("%s: a directory in the cache, not in the cold fetch" % r)
                    continue
                child = children.get(d.dirs[n].hash)
                if child is None:
                    diffs.append("%s: the Tree lacks directory %s" % (r, d.dirs[n].hash))
                    continue
                walk(child, p, r)

    walk(root, local, "")
    return diffs


def verify_repo(cache: Cache, guid: str, external: str, name: str) -> Result:
    marker = os.path.join(external, "@%s.marker" % name)
    try:
        predeclared, recorded = read_marker(marker)
        final, invalid = find_final(cache, guid, predeclared, recorded)
        if invalid:
            return Result(name, "invalid", invalid)
        if final is None:
            return Result(name, "not_cached")
        diffs = []
        path, mdg, _, contents = final.output_files[0]
        dpath, tree = final.output_directories[0]
        if path != MARKER_FILE_PATH or dpath != REPO_DIRECTORY_PATH or tree is None or mdg is None:
            return Result(name, "invalid", ["final entry outputs %r and %r" % (path, dpath)])
        cached_marker = contents if contents else cache.blob(mdg)
        if cached_marker != read_bytes(marker):
            diffs.append("%s: marker file differs" % MARKER_FILE_PATH)
        root, children = decode_tree(cache.blob(tree))
        diffs += compare_tree(root, children, os.path.join(external, name))
        return Result(name, "mismatch" if diffs else "match", diffs[:MAX_DIFFS])
    except (RPCError, ValueError, OSError) as e:
        return Result(name, "error", [str(e)])


def repos(external: str) -> list:
    out = []
    for f in sorted(os.listdir(external)):
        if f.startswith("@") and f.endswith(".marker") and os.path.isdir(os.path.join(external, f[1:-7])):
            out.append(f[1:-7])
    return out


def verify(cache: Cache, guid: str, output_base: str, jobs: int) -> list:
    external = os.path.join(output_base, "external")
    names = repos(external)
    with concurrent.futures.ThreadPoolExecutor(max_workers=jobs) as pool:
        return list(pool.map(lambda n: verify_repo(cache, guid, external, n), names))


def summarize(results: list) -> dict:
    counts = {}
    for r in results:
        counts[r.status] = counts.get(r.status, 0) + 1
    return counts


def exit_status(results: list, max_error_fraction: float) -> int:
    counts = summarize(results)
    if counts.get("mismatch") or counts.get("invalid"):
        return 1
    if not results or counts.get("error", 0) > max_error_fraction * len(results):
        return 2
    return 0


def main(argv: Optional[list] = None) -> int:
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    ap.add_argument("--output-base", required=True, help="the cold fetch's output base")
    ap.add_argument("--endpoint", required=True, help="grpcs://host:port or grpc://host:port")
    ap.add_argument("--instance", default="oss")
    ap.add_argument("--cert", default="")
    ap.add_argument("--key", default="")
    ap.add_argument("--ca", default="")
    ap.add_argument("--bazel-version", required=True, help="the release that wrote the entries (.bazelversion)")
    ap.add_argument("--jobs", type=int, default=16)
    ap.add_argument("--max-error-fraction", type=float, default=0.01, help="transport errors tolerated before exit 2")
    ap.add_argument("--json", default="", help="write the full report here")
    ap.add_argument("--summary", default="", help="append a Markdown summary here (GITHUB_STEP_SUMMARY)")
    a = ap.parse_args(argv)
    guid = BAZEL_RRC_GUIDS.get(a.bazel_version)
    if guid is None:
        print("rrc-verify: no GUID for Bazel %s; add RemoteRepoContentsCacheImpl.GUID to BAZEL_RRC_GUIDS" % a.bazel_version, file=sys.stderr)
        return 2
    if bool(a.cert) != bool(a.key):
        print("rrc-verify: --cert and --key go together", file=sys.stderr)
        return 2
    cache = Cache(CurlTransport(a.endpoint, a.cert, a.key, a.ca).call, a.instance)
    results = verify(cache, guid, a.output_base, a.jobs)
    counts = summarize(results)
    bad = [r for r in results if r.status in ("mismatch", "invalid", "error")]
    lines = ["rrc-verify: %d repositories: %s" % (len(results), ", ".join("%s %d" % kv for kv in sorted(counts.items())))]
    for r in bad:
        lines.append("  %s %s: %s" % (r.status, r.repo, "; ".join(r.detail)))
    print("\n".join(lines))
    if a.json:
        with open(a.json, "w") as f:
            json.dump({"counts": counts, "results": [dataclasses.asdict(r) for r in results if r.status != "match"]}, f, indent=1)
    if a.summary:
        with open(a.summary, "a") as f:
            f.write("### Remote repo contents cache verification\n\n")
            f.write("| status | repositories |\n|---|---|\n")
            for k, v in sorted(counts.items()):
                f.write("| %s | %d |\n" % (k, v))
            for r in bad[:50]:
                f.write("\n- **%s** `%s`: %s" % (r.status, r.repo, "; ".join(r.detail)[:500]))
            f.write("\n")
    return exit_status(results, a.max_error_fraction)


if __name__ == "__main__":
    sys.exit(main())
