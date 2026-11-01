"""Tests for tools/bazel/rrc_verify.py (ga-vnycm2.26).

The golden keys below come from Bazel 9.3.0 itself: it wrote the entries
(`bazel test --nobuild --experimental_remote_repo_contents_cache
--remote_upload_local_results` into a throwaway NativeLink 1.7.1), and
rrc_verify found 1124 of 1124 cached repositories under the keys it computes.
Everything else runs against an in-memory cache.
"""

import hashlib
import os
import stat
import tempfile
import unittest

import rrc_verify as v

GUID = v.BAZEL_RRC_GUIDS["9.3.0"]

# gazelle++go_deps+com_github_aleksi_pointer, a go_repository: its marker
# (predeclared hash, then recorded inputs) and the chain of intermediate
# entries Bazel 9.3.0 wrote for it, one batch per hop.
POINTER_MARKER = (
    "d2e29605e9f6822328a5f4d0fcacfc80203bd61260f860e6f9a942ec96a5927b\n"
    "FILE:@@gazelle++go_deps+bazel_gazelle_go_repository_config//go.env f46153b50029d3d4a489ba97ee1b14e811f12f76a6e41fa6471c92ea6dc957a9\n"
    "REPO_MAPPING:gazelle+,bazel_gazelle_go_repository_cache gazelle++non_module_deps+bazel_gazelle_go_repository_cache\n"
    "FILE:@@gazelle++non_module_deps+bazel_gazelle_go_repository_cache//go.env f46153b50029d3d4a489ba97ee1b14e811f12f76a6e41fa6471c92ea6dc957a9\n"
    "REPO_MAPPING:gazelle+,bazel_gazelle_go_repository_tools +go_repository_tools+gazelle_go_repository_tools\n"
    "FILE:@@+go_repository_tools+gazelle_go_repository_tools//bin/fetch_repo 8b1288089311dc63c3d4b04c433d9ba11df5b09b5e8fdb1f8db0804f360ca6c0\n"
    "ENV:GO_REPOSITORY_EPHEMERAL_MODCACHE \\0\n"
    "FILE:@@+go_repository_tools+gazelle_go_repository_tools//bin/gazelle 1b764f6d9ad08e324bdcab429c3105815b8914f289d594acfc791bdac2bc2b2c\n"
    "FILE:@@gazelle++go_deps+bazel_gazelle_go_repository_config//WORKSPACE cc988347dd32755bac20223ea21cd982da6da90d9113dee8ccd4c4b88b3983db\n"
)
# (input hash, its AC key, the intermediate entry's stdout)
POINTER_CHAIN = [
    ("d2e29605e9f6822328a5f4d0fcacfc80203bd61260f860e6f9a942ec96a5927b",
     "add188cbb3ec90ac802738e80e42516a17ba75e7563efffa80f64fee38edf866",
     "FILE:@@gazelle++go_deps+bazel_gazelle_go_repository_config//go.env REPO_MAPPING:gazelle+,bazel_gazelle_go_repository_cache\n"),
    ("8bc57512298f35dd223a75991454f01b20e0581fc5b3cb9ad7e26521a3b89db2",
     "5a07101d8884c3f86496450121a048660f511b33076205395149ef6c369ef719",
     "FILE:@@gazelle++non_module_deps+bazel_gazelle_go_repository_cache//go.env REPO_MAPPING:gazelle+,bazel_gazelle_go_repository_tools\n"),
    ("07b34597f53e981474b3e4952e51c5caa2ba3877f61ad0cc2fe39c84f51acc8f",
     "f7eac27fd1a860cc4563bf18f42f0672d533e171341e7293aae8b5f5d8cdebc3",
     "FILE:@@+go_repository_tools+gazelle_go_repository_tools//bin/fetch_repo ENV:GO_REPOSITORY_EPHEMERAL_MODCACHE\n"),
    ("1ef1aa4b4cb73c42f39465f8feba3ae846c412e5a0a7f2790264264c1941df6e",
     "531c6579bec5634c2744fdd5b90ea123739d431991472877bffe3548c86697b1",
     "FILE:@@+go_repository_tools+gazelle_go_repository_tools//bin/gazelle\n"),
    ("edc467b59551f6058310c401062c51315e9a1c3b2bc8ec0a57d7411d7ca4b6dc",
     "94a6fc01bd63b6c1506919e676603784eb73e9049b121c4580aaf6e75893d276",
     "FILE:@@gazelle++go_deps+bazel_gazelle_go_repository_config//WORKSPACE\n"),
]  # fmt: skip
POINTER_FINAL_HASH = "3ceee7642907f9b23db2d1944d05314384151ca509d432eca651bdee2d61997c"
POINTER_FINAL_KEY = v.Digest("b0ca33d505c031b1ee5aa17138c14135803b0989ef76f3203999a316a933e2f2", 206)


# --- an in-memory cache ---------------------------------------------------------


def enc_directory(files=(), dirs=(), symlinks=()):
    out = b""
    for name, dg, exe in sorted(files):
        node = v._field(1, name.encode()) + v._field(2, dg.encode())
        if exe:
            node += v._field_varint(4, 1)
        out += v._field(1, node)
    for name, dg in sorted(dirs):
        out += v._field(2, v._field(1, name.encode()) + v._field(2, dg.encode()))
    for name, target in sorted(symlinks):
        out += v._field(3, v._field(1, name.encode()) + v._field(2, target.encode()))
    return out


class FakeCache:
    """An AC (input hash -> ActionResult bytes) and a CAS, behind Cache's rpc."""

    def __init__(self):
        self.ac, self.cas = {}, {}

    def put_blob(self, data):
        d = v.Digest.of(data)
        self.cas[d.hash] = data
        return d

    def tree_of(self, path):
        """Uploads path's files and returns the Tree digest (as Bazel would)."""
        children = []

        def directory(p):
            files, dirs, links = [], [], []
            for n in sorted(os.listdir(p)):
                q = os.path.join(p, n)
                st = os.lstat(q)
                if stat.S_ISLNK(st.st_mode):
                    links.append((n, os.readlink(q)))
                elif stat.S_ISDIR(st.st_mode):
                    raw = directory(q)
                    children.append(raw)
                    dirs.append((n, v.Digest.of(raw)))
                else:
                    data = v.read_bytes(q)
                    files.append((n, self.put_blob(data), bool(st.st_mode & stat.S_IXUSR)))
            return enc_directory(files, dirs, links)

        root = directory(path)
        tree = v._field(1, root) + b"".join(v._field(2, c) for c in children)
        return self.put_blob(tree)

    def put_final(self, input_hash, marker, tree, exit_code=0):
        md = self.put_blob(marker)
        ar = (
            v._field(2, v._field(1, v.MARKER_FILE_PATH.encode()) + v._field(2, md.encode()))
            + v._field(3, v._field(1, v.REPO_DIRECTORY_PATH.encode()) + v._field(3, tree.encode()))
        )
        if exit_code:
            ar += v._field_varint(4, exit_code)
        self.ac[input_hash] = ar

    def put_intermediate(self, input_hash, stdout, raw=True):
        if raw:
            self.ac[input_hash] = v._field(5, stdout.encode()) + v._field(6, v.Digest.of(stdout.encode()).encode())
        else:
            self.ac[input_hash] = v._field(6, self.put_blob(stdout.encode()).encode())

    def rpc(self, method, request):
        if method.endswith("ActionCache/GetActionResult"):
            key = None
            for num, _, val in v.fields(request):
                if num == 2:
                    key = v.Digest.decode(val)
            for h, ar in self.ac.items():
                if v.action_digest(GUID, h) == key:
                    return [ar]
            raise v.RPCError(v.GRPC_NOT_FOUND, "not found")
        if method.endswith("ByteStream/Read"):
            name = bytes(next(val for num, _, val in v.fields(request) if num == 1)).decode()
            _, _, h, size = name.split("/")
            data = self.cas.get(h)
            if data is None:
                raise v.RPCError(v.GRPC_NOT_FOUND, "no blob")
            return [v._field(10, data[: len(data) // 2]), v._field(10, data[len(data) // 2 :])]
        raise AssertionError(method)


def write(path, data, mode=0o644):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "wb") as f:
        f.write(data)
    os.chmod(path, mode)


class Fixture:
    """A cold output base with one repository, cached as fetched."""

    def __init__(self, test, name="repo", marker=b"h0\n"):
        self.tmp = tempfile.TemporaryDirectory()
        test.addCleanup(self.tmp.cleanup)
        self.ext = os.path.join(self.tmp.name, "external")
        self.name = name
        self.dir = os.path.join(self.ext, name)
        write(os.path.join(self.dir, "BUILD.bazel"), b"filegroup(name = 'x')\n")
        write(os.path.join(self.dir, "bin", "tool"), b"#!/bin/sh\n", 0o755)
        write(os.path.join(self.dir, "sub", "deep", "data.txt"), b"data\n")
        os.makedirs(os.path.join(self.dir, "empty"))
        os.symlink("bin/tool", os.path.join(self.dir, "link"))
        self.marker = marker
        write(os.path.join(self.ext, "@%s.marker" % name), marker)
        self.cache = FakeCache()

    def seed(self, input_hash="h0"):
        self.cache.put_final(input_hash, self.marker, self.cache.tree_of(self.dir))

    def verify(self):
        return v.verify_repo(v.Cache(self.cache.rpc, "oss"), GUID, self.ext, self.name)


class KeysTest(unittest.TestCase):
    def test_action_digest_matches_bazel_9_3_0(self):
        for h, key, _ in POINTER_CHAIN:
            self.assertEqual(v.action_digest(GUID, h).hash, key)
        self.assertEqual(v.action_digest(GUID, POINTER_FINAL_HASH), POINTER_FINAL_KEY)

    def test_roll_forward_matches_bazel_9_3_0(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        path = os.path.join(tmp.name, "m")
        write(path, POINTER_MARKER.encode())
        predeclared, recorded = v.read_marker(path)
        self.assertEqual(predeclared, POINTER_CHAIN[0][0])
        h = predeclared
        for i, (want, _, stdout) in enumerate(POINTER_CHAIN):
            self.assertEqual(h, want, "hop %d" % i)
            for inp in stdout.strip().split(" "):
                h = v.roll_forward(h, recorded[inp])
        self.assertEqual(h, POINTER_FINAL_HASH)

    def test_guid_for_every_release_is_a_uuid(self):
        for release, guid in v.BAZEL_RRC_GUIDS.items():
            self.assertRegex(guid, r"^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$", release)

    def test_bazelversion_has_a_guid(self):
        root = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(v.__file__))))
        path = os.path.join(root, ".bazelversion")
        version = v.read_bytes(path).decode().strip()
        self.assertIn(version, v.BAZEL_RRC_GUIDS, "a .bazelversion bump must add RemoteRepoContentsCacheImpl.GUID")


class VerifyRepoTest(unittest.TestCase):
    def test_match(self):
        f = Fixture(self)
        f.seed()
        r = f.verify()
        self.assertEqual((r.status, r.detail), ("match", []))

    def test_not_cached(self):
        f = Fixture(self)
        self.assertEqual(f.verify().status, "not_cached")

    def test_content_poisoned(self):
        f = Fixture(self)
        f.seed()
        write(os.path.join(f.dir, "sub", "deep", "data.txt"), b"other\n")
        r = f.verify()
        self.assertEqual(r.status, "mismatch")
        self.assertIn("sub/deep/data.txt: content differs", " ".join(r.detail))

    def test_extra_and_missing_files(self):
        f = Fixture(self)
        f.seed()
        write(os.path.join(f.dir, "init.go"), b"package x\n")
        os.remove(os.path.join(f.dir, "BUILD.bazel"))
        detail = " ".join(f.verify().detail)
        self.assertIn("init.go: only in the cold fetch", detail)
        self.assertIn("BUILD.bazel: only in the cache", detail)

    def test_executable_bit(self):
        f = Fixture(self)
        f.seed()
        os.chmod(os.path.join(f.dir, "bin", "tool"), 0o644)
        self.assertIn("bin/tool: executable bit differs", " ".join(f.verify().detail))

    def test_symlink_target(self):
        f = Fixture(self)
        f.seed()
        os.remove(os.path.join(f.dir, "link"))
        os.symlink("sub", os.path.join(f.dir, "link"))
        self.assertIn("link: symlink to", " ".join(f.verify().detail))

    def test_empty_directory(self):
        f = Fixture(self)
        f.seed()
        os.rmdir(os.path.join(f.dir, "empty"))
        self.assertIn("empty: only in the cache", " ".join(f.verify().detail))

    def test_marker_differs(self):
        f = Fixture(self)
        f.seed()
        write(os.path.join(f.ext, "@repo.marker"), b"h0\nENV:X 1\n")
        r = f.verify()
        self.assertEqual(r.status, "mismatch")
        self.assertIn("marker file differs", " ".join(r.detail))

    def test_intermediate_chain(self):
        marker = b"p0\nENV:A 1\nFILE:@@x//y abc\nENV:B \\0\n"
        f = Fixture(self, marker=marker)
        h1 = v.roll_forward(v.roll_forward("p0", "ENV:A 1"), "FILE:@@x//y abc")
        h2 = v.roll_forward(h1, "ENV:B \\0")
        f.cache.put_intermediate("p0", "ENV:Z\nENV:A FILE:@@x//y\n")  # the first batch is another fetch's
        f.cache.put_intermediate(h1, "ENV:B\n", raw=False)
        f.seed(h2)
        r = f.verify()
        self.assertEqual((r.status, r.detail), ("match", []))

    def test_intermediate_without_a_matching_batch(self):
        f = Fixture(self, marker=b"p0\nENV:A 1\n")
        f.cache.put_intermediate("p0", "ENV:Q\n")
        f.seed(v.roll_forward("p0", "ENV:A 2"))  # another value of A: not this fetch's
        self.assertEqual(f.verify().status, "not_cached")

    def test_invalid_exit_code(self):
        f = Fixture(self)
        f.cache.put_final("h0", f.marker, f.cache.tree_of(f.dir), exit_code=1)
        self.assertEqual(f.verify().status, "invalid")

    def test_invalid_shape(self):
        f = Fixture(self)
        f.cache.ac["h0"] = v._field_varint(4, 0) + v._field(7, b"stderr")
        self.assertEqual(f.verify().status, "invalid")

    def test_tampered_blob_is_an_error(self):
        f = Fixture(self)
        f.seed()
        tree = next(d for d in f.cache.cas if f.cache.cas[d].startswith(b"\x0a"))
        f.cache.cas[tree] = f.cache.cas[tree] + b"\x00"
        self.assertEqual(f.verify().status, "error")


class ReportTest(unittest.TestCase):
    def test_exit_status(self):
        R = v.Result
        self.assertEqual(v.exit_status([R("a", "match"), R("b", "not_cached")], 0.01), 0)
        self.assertEqual(v.exit_status([R("a", "match"), R("b", "mismatch")], 0.01), 1)
        self.assertEqual(v.exit_status([R("a", "invalid")], 0.01), 1)
        self.assertEqual(v.exit_status([R("a", "error")] + [R(str(i), "match") for i in range(200)], 0.01), 0)
        self.assertEqual(v.exit_status([R("a", "error"), R("b", "match")], 0.01), 2)
        self.assertEqual(v.exit_status([], 0.01), 2)

    def test_repos_lists_marked_repositories(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        ext = os.path.join(tmp.name, "external")
        for n in ("a", "b+c"):
            os.makedirs(os.path.join(ext, n))
            write(os.path.join(ext, "@%s.marker" % n), b"h\n")
        os.makedirs(os.path.join(ext, "unmarked"))
        write(os.path.join(ext, "@gone.marker"), b"h\n")
        self.assertEqual(v.repos(ext), ["a", "b+c"])

    def test_unknown_release_is_a_usage_error(self):
        self.assertEqual(v.main(["--output-base", "/x", "--endpoint", "grpc://x:1", "--bazel-version", "0.0.0"]), 2)


if __name__ == "__main__":
    unittest.main()
