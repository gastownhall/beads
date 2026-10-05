//go:build unix

// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/ca_permissions_unix_test.go@49d1df2f6)
// to OSS beads under the MIT license.

package httpclient

import (
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestCheckCAFilePermissionsRefusesSymlinkIntoWorldWritableDir is finding 3:
// a symlink whose own directory is fine but whose REAL target sits inside a
// world-writable directory must be refused — checking only the symlink's own
// location would miss the swap this check exists to catch.
func TestCheckCAFilePermissionsRefusesSymlinkIntoWorldWritableDir(t *testing.T) {
	realDir := t.TempDir()
	if err := os.Chmod(realDir, 0o777); err != nil {
		t.Fatalf("chmod real dir 0777: %v", err)
	}
	realPath := filepath.Join(realDir, "ca.pem")
	if err := os.WriteFile(realPath, []byte("x"), 0o600); err != nil {
		t.Fatalf("write real file: %v", err)
	}

	linkDir := t.TempDir()
	if err := os.Chmod(linkDir, 0o700); err != nil {
		t.Fatalf("chmod link dir: %v", err)
	}
	link := filepath.Join(linkDir, "ca.pem")
	if err := os.Symlink(realPath, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	if _, _, err := checkCAFilePermissions(link); err == nil {
		t.Fatal("checkCAFilePermissions accepted a symlink into a world-writable directory")
	}
}

// TestCheckCAFilePermissionsRefusesSymlinkItselfInWorldWritableDir is finding
// 2's complement to TestCheckCAFilePermissionsRefusesSymlinkIntoWorldWritableDir
// above: here the symlink's TARGET is perfectly fine (its own directory is
// 0700), but the symlink ENTRY itself sits inside a world-writable directory.
// Only checking the resolved target's ancestors (what a single
// filepath.EvalSymlinks call would do) would miss this: anyone could unlink
// and replace the symlink entry itself, in the world-writable linkDir, to
// point somewhere else entirely. checkCAFilePermissionsHop must check the
// symlink's OWN containing directory before following it.
func TestCheckCAFilePermissionsRefusesSymlinkItselfInWorldWritableDir(t *testing.T) {
	linkDir := t.TempDir()
	if err := os.Chmod(linkDir, 0o777); err != nil {
		t.Fatalf("chmod link dir 0777: %v", err)
	}

	realDir := t.TempDir()
	if err := os.Chmod(realDir, 0o700); err != nil {
		t.Fatalf("chmod real dir: %v", err)
	}
	realPath := filepath.Join(realDir, "ca.pem")
	if err := os.WriteFile(realPath, []byte("x"), 0o600); err != nil {
		t.Fatalf("write real file: %v", err)
	}

	link := filepath.Join(linkDir, "ca.pem")
	if err := os.Symlink(realPath, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	if _, _, err := checkCAFilePermissions(link); err == nil {
		t.Fatal("checkCAFilePermissions accepted a symlink sitting in a world-writable, non-sticky directory even though its target is fine")
	}
}

// TestCheckCAFilePermissionsRefusesWorldWritableGrandparent is finding 3: a
// world-writable GRANDparent (not just the immediate parent) must be
// refused — an attacker who can write the grandparent can rename the parent
// out of the way and replace it.
func TestCheckCAFilePermissionsRefusesWorldWritableGrandparent(t *testing.T) {
	grandparent := t.TempDir()
	if err := os.Chmod(grandparent, 0o777); err != nil {
		t.Fatalf("chmod grandparent 0777: %v", err)
	}
	parent := filepath.Join(grandparent, "safe-looking-parent")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}
	path := filepath.Join(parent, "ca.pem")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	if _, _, err := checkCAFilePermissions(path); err == nil {
		t.Fatal("checkCAFilePermissions accepted a file whose grandparent directory is world-writable")
	}
}

// TestCheckCAFilePermissionsAllowsStickyWorldWritableAncestor confirms the
// sticky-directory exception: an ancestor with mode +t, however
// world-writable, is exempt, because only that entry's own owner may remove
// or rename it from under a sticky directory.
func TestCheckCAFilePermissionsAllowsStickyWorldWritableAncestor(t *testing.T) {
	sticky := t.TempDir()
	if err := os.Chmod(sticky, 0o777|os.ModeSticky); err != nil {
		t.Fatalf("chmod sticky world-writable: %v", err)
	}
	parent := filepath.Join(sticky, "safe-parent")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}
	path := filepath.Join(parent, "ca.pem")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	if _, _, err := checkCAFilePermissions(path); err != nil {
		t.Errorf("checkCAFilePermissions refused a sticky world-writable ancestor: %v", err)
	}
}

// TestCheckCAFilePermissionsRefusesFileOwnedByAnotherUser is finding 3's
// ownership rule, exercised through the injectable statCAPathFn since this
// sandbox cannot chown a file to a second real uid without root: a file
// owned by neither root nor the running uid must be refused, even though its
// mode bits alone (0600) would otherwise look fine.
func TestCheckCAFilePermissionsRefusesFileOwnedByAnotherUser(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	restore := statCAPathFn
	t.Cleanup(func() { statCAPathFn = restore })
	const otherUID = 0xFFFE // an implausible uid, never root, never the test runner
	statCAPathFn = func(p string) (caFileStat, error) {
		fi, err := restore(p)
		if err != nil {
			return fi, err
		}
		if p == path {
			fi.uid = otherUID
		}
		return fi, nil
	}

	_, _, err := checkCAFilePermissions(path)
	if err == nil {
		t.Fatal("checkCAFilePermissions accepted a file owned by a uid other than root or the running one")
	}
	if !strings.Contains(err.Error(), "owned by uid") {
		t.Errorf("error %q does not explain the ownership failure", err)
	}
}

// TestCheckCAFilePermissionsAcceptsRootOwnedLayout is finding 3's positive
// case: the /etc/bd-style layout (root-owned, 0755 directories, 0644
// files) must be accepted. Exercised through the injectable statCAPathFn,
// since this sandbox has no real root-owned tree to point at.
func TestCheckCAFilePermissionsAcceptsRootOwnedLayout(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	restore := statCAPathFn
	t.Cleanup(func() { statCAPathFn = restore })
	statCAPathFn = func(p string) (caFileStat, error) {
		fi, err := restore(p)
		if err != nil {
			return fi, err
		}
		// Simulate root ownership at every level, root-owned-layout modes.
		fi.uid, fi.gid = 0, 0
		if fi.mode.IsDir() {
			fi.mode = (fi.mode &^ os.ModePerm) | 0o755
		} else {
			fi.mode = (fi.mode &^ os.ModePerm) | 0o644
		}
		return fi, nil
	}

	if _, _, err := checkCAFilePermissions(path); err != nil {
		t.Errorf("checkCAFilePermissions refused a root-owned 0755/0644 layout: %v", err)
	}
}

// TestCheckCAFilePermissionsRefusesStickyAncestorOwnedByAnotherUser is
// finding 3: the sticky-directory exemption in checkOwnerAndMode covers the
// MODE/writability check only — it must never let a sticky ancestor owned by
// neither root nor the running euid pass. Exercised through the injectable
// statCAPathFn, since this sandbox cannot chown a real directory to a second
// uid without root.
func TestCheckCAFilePermissionsRefusesStickyAncestorOwnedByAnotherUser(t *testing.T) {
	sticky := t.TempDir()
	if err := os.Chmod(sticky, 0o777|os.ModeSticky); err != nil {
		t.Fatalf("chmod sticky world-writable: %v", err)
	}
	path := filepath.Join(sticky, "ca.pem")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	restore := statCAPathFn
	t.Cleanup(func() { statCAPathFn = restore })
	const otherUID = 0xFFFE // an implausible uid, never root, never the test runner
	statCAPathFn = func(p string) (caFileStat, error) {
		fi, err := restore(p)
		if err != nil {
			return fi, err
		}
		if p == sticky {
			fi.uid = otherUID
		}
		return fi, nil
	}

	_, _, err := checkCAFilePermissions(path)
	if err == nil {
		t.Fatal("checkCAFilePermissions accepted a sticky ancestor directory owned by a uid other than root or the running one")
	}
	if !strings.Contains(err.Error(), "owned by uid") {
		t.Errorf("error %q does not explain the ownership failure", err)
	}
}

// TestCheckOwnerAndModeNeverExemptsLeafFileSticky kills the mutant that would
// drop the "isDir &&" half of checkOwnerAndMode's sticky-exemption condition:
// called directly with isDir=false (a leaf file, never a directory) and a
// mode that is both world-writable AND carries the sticky bit, the leaf must
// still be refused. Sticky has no protective meaning for a non-directory, and
// checkCAFilePermissionsHop's own structure means isDir is always false for
// the leaf — but that guarantee is worth pinning down directly, in case a
// future change routes some other isDir=false caller through this function.
func TestCheckOwnerAndModeNeverExemptsLeafFileSticky(t *testing.T) {
	fi := caFileStat{mode: os.FileMode(0o777) | os.ModeSticky, uid: uint32(os.Geteuid())} //nolint:gosec // always non-negative
	err := checkOwnerAndMode("/fake/ca.pem", fi, false)
	if err == nil {
		t.Fatal("checkOwnerAndMode exempted a non-directory (leaf) with the sticky bit set from the world-writable check")
	}
	if !strings.Contains(err.Error(), "world-writable") {
		t.Errorf("error %q does not explain the world-writable refusal", err)
	}
}

// TestIsUserPrivateGroupFalseForRootGroup is finding 5's explicit root
// carve-out: root's own primary group (commonly also named "root") must
// never qualify for the user-private-group exemption, whatever the name and
// membership checks below it would otherwise conclude.
func TestIsUserPrivateGroupFalseForRootGroup(t *testing.T) {
	if isUserPrivateGroup(0, 0) {
		t.Error("isUserPrivateGroup reported true for root:root")
	}
}

// TestIsUserPrivateGroupNameComparisonExercisedDirectly is finding 5's
// explicit ask: exercise isUserPrivateGroup's own name-comparison line
// (g.Name != u.Username) through both arms, calling isUserPrivateGroup
// itself — never stubbing isUserPrivateGroupFn, which would bypass the
// comparison under test rather than exercise it. It uses the
// userLookupIDFn/userLookupGroupIDFn seams (not isUserPrivateGroupFn) to
// manufacture a uid/gid/username combination this sandbox's single real
// account cannot provide on demand.
func TestIsUserPrivateGroupNameComparisonExercisedDirectly(t *testing.T) {
	restoreUser := userLookupIDFn
	restoreGroup := userLookupGroupIDFn
	restoreMembers := groupHasNoSupplementaryMembersFn
	t.Cleanup(func() {
		userLookupIDFn = restoreUser
		userLookupGroupIDFn = restoreGroup
		groupHasNoSupplementaryMembersFn = restoreMembers
	})

	const fakeUID, fakeGID = 4242, 4242
	fakeUIDStr := strconv.Itoa(fakeUID)
	fakeGIDStr := strconv.Itoa(fakeGID)
	userLookupIDFn = func(uid string) (*user.User, error) {
		if uid == fakeUIDStr {
			return &user.User{Uid: fakeUIDStr, Gid: fakeGIDStr, Username: "alice"}, nil
		}
		return restoreUser(uid)
	}
	// Stubbed to always report "no supplementary members" for fakeGID, in
	// BOTH arms below: membership is a separate check from the name
	// comparison under test here, and the real, unstubbed
	// groupHasNoSupplementaryMembers would fail closed (false) for a gid that
	// does not exist in this host's real /etc/group regardless of the name
	// comparison's outcome — masking a mutant that neuters the name check
	// instead of actually exercising it.
	groupHasNoSupplementaryMembersFn = func(gid uint32) bool {
		if gid == fakeGID {
			return true
		}
		return restoreMembers(gid)
	}

	// Arm 1: the group's name does NOT match the owner's username -> refused.
	// A mutant that drops, inverts, or no-ops the "g.Name != u.Username"
	// comparison would accept this.
	userLookupGroupIDFn = func(gid string) (*user.Group, error) {
		if gid == fakeGIDStr {
			return &user.Group{Gid: fakeGIDStr, Name: "shared-group"}, nil
		}
		return restoreGroup(gid)
	}
	if isUserPrivateGroup(fakeUID, fakeGID) {
		t.Error("isUserPrivateGroup reported true when the group's name does not match the owner's username")
	}

	// Arm 2: the group's name DOES match the username -> accepted.
	userLookupGroupIDFn = func(gid string) (*user.Group, error) {
		if gid == fakeGIDStr {
			return &user.Group{Gid: fakeGIDStr, Name: "alice"}, nil
		}
		return restoreGroup(gid)
	}
	if !isUserPrivateGroup(fakeUID, fakeGID) {
		t.Error("isUserPrivateGroup reported false when username, group name, and gid all matched and the group had no supplementary members")
	}
}

// TestGroupHasNoSupplementaryMembersAgainstRealEtcGroup exercises the real,
// unstubbed groupHasNoSupplementaryMembers against this host's actual
// /etc/group, without going through groupHasNoSupplementaryMembersFn or
// isUserPrivateGroupFn at all: a group with a real, non-empty members field
// must return false, and (when the running user's own primary group follows
// the Debian/OpenSSH user-private-group convention) that group's empty
// members field must return true.
func TestGroupHasNoSupplementaryMembersAgainstRealEtcGroup(t *testing.T) {
	data, err := os.ReadFile("/etc/group")
	if err != nil {
		t.Skipf("cannot read /etc/group on this host: %v", err)
	}

	var sharedGID uint64
	foundShared := false
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, ":")
		if len(fields) < 4 || strings.TrimSpace(fields[3]) == "" {
			continue
		}
		gid, err := strconv.ParseUint(fields[2], 10, 32)
		if err != nil {
			continue
		}
		sharedGID = gid
		foundShared = true
		break
	}
	if !foundShared {
		t.Skip("no group with supplementary members found in /etc/group on this host")
	}
	if groupHasNoSupplementaryMembers(uint32(sharedGID)) {
		t.Errorf("groupHasNoSupplementaryMembers(%d) reported true for a group with real members listed in /etc/group", sharedGID)
	}

	ownGID := uint32(os.Getgid()) //nolint:gosec // always non-negative
	if !groupHasNoSupplementaryMembers(ownGID) {
		t.Skip("running user's own primary group is not a private (memberless) group on this host")
	}
}

// TestIsUserPrivateGroupTrueForRunningUsersOwnGroup documents the positive
// case directly: the running uid's own primary gid, looked up for real,
// reports as a user-private group on a host using the Debian/OpenSSH
// convention (this one does — see CLAUDE.md's note on Ubuntu
// user-private-groups).
func TestIsUserPrivateGroupTrueForRunningUsersOwnGroup(t *testing.T) {
	uid := uint32(os.Getuid()) //nolint:gosec // always non-negative
	gid := uint32(os.Getgid()) //nolint:gosec // always non-negative
	if !isUserPrivateGroup(uid, gid) {
		t.Skip("this host's running user does not use the Debian/OpenSSH user-private-group convention")
	}
}

// TestIsUserPrivateGroupFalseForUnknownIDs confirms the function fails
// closed (not private) rather than erroring or panicking when the ids do not
// resolve to a real account/group at all.
func TestIsUserPrivateGroupFalseForUnknownIDs(t *testing.T) {
	if isUserPrivateGroup(0xFFFFFFF0, 0xFFFFFFF1) {
		t.Error("isUserPrivateGroup reported true for ids that cannot possibly resolve to a real account")
	}
}
