//go:build unix

// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/ca_permissions_unix.go@49d1df2f6)
// to OSS beads under the MIT license.

package httpclient

import (
	"bufio"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// caFileStat is what checkCAFilePermissions needs from a stat call, factored
// out of os.Lstat so a test can inject an ownership scenario (a leaf or
// ancestor owned by a uid other than root or the running one, or a
// root-owned 0755/0644 layout) that this sandbox cannot construct with real
// files — see statCAPathFn.
type caFileStat struct {
	mode os.FileMode
	uid  uint32
	gid  uint32
	// info is the underlying os.FileInfo the real statCAPathFn Lstat'd. It is
	// nil for a test-injected caFileStat, which is fine: only the leaf's
	// info (returned from checkCAFilePermissions itself) is ever consulted
	// for os.SameFile, and every test that injects synthetic ownership goes
	// through checkCAFilePermissions itself rather than reading this field
	// directly.
	info os.FileInfo
}

// statCAPathFn is swappable so a test can simulate ownership scenarios real
// files in the test sandbox cannot: a path owned by a uid other than root or
// the running one, or a root-owned layout on a host where every real file is
// owned by the test's own uid. It never follows the final path component
// (os.Lstat), so checkCAFilePermissions can detect a symlink at each hop
// itself rather than the kernel silently resolving it away.
var statCAPathFn = func(path string) (caFileStat, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return caFileStat{}, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		// Best effort: no ownership info available, only the mode.
		return caFileStat{mode: info.Mode(), info: info}, nil
	}
	return caFileStat{mode: info.Mode(), uid: st.Uid, gid: st.Gid, info: info}, nil
}

// openCAFileNoFollow opens real for reading with O_NOFOLLOW: real is what
// checkCAFilePermissions just confirmed is NOT itself a symlink, so if the
// open fails with ELOOP (or any other O_NOFOLLOW rejection) something
// replaced it with a symlink in the window between that check and this call,
// and the read must refuse rather than silently follow the swap.
func openCAFileNoFollow(real string) (*os.File, error) {
	fd, err := syscall.Open(real, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open (no-follow): %w", err)
	}
	return os.NewFile(uintptr(fd), real), nil
}

// isUserPrivateGroupFn is swappable so a test can simulate a "genuinely
// shared" group (finding 4's refusal case) without needing a second real
// POSIX group on the machine running the test.
var isUserPrivateGroupFn = isUserPrivateGroup

// userLookupIDFn and userLookupGroupIDFn are swappable so a test can drive
// isUserPrivateGroup's OWN name-comparison line (g.Name != u.Username)
// through both arms directly — calling isUserPrivateGroup itself, not the
// isUserPrivateGroupFn seam above, which would skip over that exact
// comparison rather than exercise it. The real os/user package has no way to
// manufacture a uid/gid/username combination on demand, so without this seam
// a test is stuck with whatever single account the host running it happens
// to have.
var (
	userLookupIDFn      = user.LookupId
	userLookupGroupIDFn = user.LookupGroupId
)

// groupHasNoSupplementaryMembersFn is swappable so a test can simulate
// membership scenarios (a group with an added member; a group whose
// membership cannot be determined) that this sandbox's real /etc/group may
// not exhibit either way. The real implementation reads /etc/group directly:
// Go's os/user package has no portable API for "list this group's
// supplementary members" (LookupGroup/LookupGroupId return only id and
// name), and getgrgid_r's own member list is what /etc/group's fourth field
// carries on every platform this build tag covers.
var groupHasNoSupplementaryMembersFn = groupHasNoSupplementaryMembers

// isUserPrivateGroup reports the Debian/OpenSSH "user private group"
// convention: uid's own primary group is gid, that group's name is exactly
// uid's username, AND the group lists no OTHER member in /etc/group. Under
// Ubuntu's default user-private-groups scheme this is true for every
// ordinary account, which is what makes a umask-002 ~/.config/bd (0775
// directories, 0664 files) exactly as private as a umask-022 layout would
// be — group-write only reaches a group whose sole member, by convention, is
// the file's own owner. A file whose group is some OTHER group the owner
// merely belongs to (a real shared group, "docker", "sudo") does not get
// this exemption, whatever its name looks like.
//
// Root (uid 0) never qualifies, regardless of what the name/membership
// checks below would otherwise conclude: gid 0 is commonly named "root" too,
// so a naive name comparison would treat root's own primary group as
// "private" — but root's group is a system convention, not evidence that no
// other account can write through it, and CA hygiene should never lean on
// that assumption.
func isUserPrivateGroup(uid, gid uint32) bool {
	if uid == 0 {
		return false
	}
	u, err := userLookupIDFn(strconv.FormatUint(uint64(uid), 10))
	if err != nil {
		return false
	}
	if u.Gid != strconv.FormatUint(uint64(gid), 10) {
		return false
	}
	g, err := userLookupGroupIDFn(strconv.FormatUint(uint64(gid), 10))
	if err != nil {
		return false
	}
	if g.Name != u.Username {
		return false
	}
	return groupHasNoSupplementaryMembersFn(gid)
}

// groupHasNoSupplementaryMembers reports whether gid's own /etc/group entry
// lists no supplementary members by name — the actual membership half of the
// user-private-group convention, beyond gid/name matching alone: a group
// literally named after its "owner" that other accounts have ALSO been added
// to (in /etc/group's fourth, comma-separated field) is not private,
// whatever its name suggests. When /etc/group cannot be read, or gid is not
// found in it at all, this refuses (returns false) rather than assuming the
// group is private: a hygiene check that cannot positively confirm isolation
// must not grant the exemption that isolation is the whole justification
// for.
func groupHasNoSupplementaryMembers(gid uint32) bool {
	f, err := os.Open("/etc/group")
	if err != nil {
		return false
	}
	defer f.Close() //nolint:errcheck // read-only fd

	gidStr := strconv.FormatUint(uint64(gid), 10)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, ":")
		if len(fields) < 4 || fields[2] != gidStr {
			continue
		}
		members := strings.TrimSpace(fields[3])
		return members == ""
	}
	if err := scanner.Err(); err != nil {
		return false
	}
	return false // gid not found in /etc/group: cannot confirm -> refuse
}

// maxCASymlinkHops bounds how many symlink hops checkCAFilePermissions will
// follow before refusing outright, guarding against a symlink loop (or an
// adversarial chain) turning this check into an unbounded walk.
const maxCASymlinkHops = 40

// checkCAFilePermissions refuses a CA file, or any ancestor directory up to
// /, that a party other than its owner (root, or the uid running this
// process) could have written — the same hygiene ssh requires of
// known_hosts and authorized_keys. A CA file REPLACES the entire trust root
// for a target; anyone who can write it, or swap it out from a writable
// ancestor, can make this process trust a CA of their choosing, a full
// impersonation of that target.
//
// path is resolved ONE SYMLINK HOP AT A TIME rather than through a single
// filepath.EvalSymlinks call: EvalSymlinks would resolve the whole chain
// silently, and the containing directory of any symlink component along the
// ORIGINAL path is never itself examined — a symlink sitting in a
// world-writable directory, pointing at an otherwise pristine target, would
// pass unnoticed, because only the target's own ancestors would ever be
// checked. This walk instead stats each path with Lstat, and whenever a hop
// turns out to be a symlink, checks THAT symlink's own containing directory
// (the same ownership/writability rules as any other ancestor) before
// following it to wherever it points.
//
// On success it returns the fully resolved, non-symlink real path and the
// os.FileInfo checkCAFilePermissions stat'd for it, so the caller (readCAFile)
// can open that exact path and compare what it opened against what was
// checked here — closing the gap between this function's stat and a
// separate, independent open of the same path string.
//
// Every ancestor directory up to / is checked, not just the immediate
// parent: a writable grandparent lets an attacker rename the parent out of
// the way and replace it, which a parent-only check would never see. A
// sticky ancestor directory (mode +t, e.g. /tmp) is exempt from the
// writability check ONLY: anyone may create an entry there, but only that
// entry's own owner may remove or rename it, so a sticky world-writable
// directory does not let another user replace this file's directory entry
// from above — but the directory's OWNER must still be root or the running
// euid, exactly as for any other ancestor; sticky is not a license to be
// owned by anyone. The leaf file itself gets no exemption at all, sticky bit
// or not: sticky has no protective meaning for a file that is not a
// directory, and checkOwnerAndMode's own isDir gate means this exemption can
// never reach the leaf regardless of what bits its mode happens to carry.
//
// Group-write is refused unless the group is the owner's OWN user-private
// group (isUserPrivateGroup) — see its doc. World-write is never allowed,
// for the leaf file or any non-sticky ancestor.
func checkCAFilePermissions(path string) (string, os.FileInfo, error) {
	return checkCAFilePermissionsHop(path, 0)
}

func checkCAFilePermissionsHop(path string, hops int) (string, os.FileInfo, error) {
	if hops > maxCASymlinkHops {
		return "", nil, fmt.Errorf("too many levels of symbolic links resolving %s", path)
	}

	fi, err := statCAPathFn(path)
	if err != nil {
		return "", nil, err
	}

	if fi.mode&os.ModeSymlink != 0 {
		dir := filepath.Dir(path)
		if err := checkAncestors(dir); err != nil {
			return "", nil, err
		}
		target, err := readLinkTarget(path, dir)
		if err != nil {
			return "", nil, err
		}
		return checkCAFilePermissionsHop(target, hops+1)
	}

	if fi.mode.IsDir() {
		return "", nil, fmt.Errorf("is a directory, not a PEM file")
	}
	if err := checkOwnerAndMode(path, fi, false); err != nil {
		return "", nil, err
	}
	if err := checkAncestors(filepath.Dir(path)); err != nil {
		return "", nil, err
	}
	return path, fi.info, nil
}

// checkAncestors walks every ancestor directory of dir up to /, applying
// checkOwnerAndMode to each. An ancestor that is itself a symlink is
// followed the same way checkCAFilePermissionsHop follows the leaf: the
// symlink entry's OWN containing directory (and everything above it) is
// checked BEFORE following it, and the walk then continues separately from
// its resolved target. hops bounds the combined recursion (both "following
// an ancestor symlink" and "walking up one more parent" consume the same
// budget) against a symlink loop among ancestors.
func checkAncestors(dir string) error {
	return checkAncestorsHop(dir, 0)
}

func checkAncestorsHop(dir string, hops int) error {
	if hops > maxCASymlinkHops {
		return fmt.Errorf("too many levels of symbolic links resolving ancestor directories of %s", dir)
	}

	dfi, err := statCAPathFn(dir)
	if err != nil {
		return fmt.Errorf("stat %s: %w", dir, err)
	}

	if dfi.mode&os.ModeSymlink != 0 {
		parentOfLink := filepath.Dir(dir)
		if err := checkAncestorsHop(parentOfLink, hops+1); err != nil {
			return err
		}
		target, err := readLinkTarget(dir, parentOfLink)
		if err != nil {
			return err
		}
		return checkAncestorsHop(target, hops+1)
	}

	if err := checkOwnerAndMode(dir, dfi, true); err != nil {
		return err
	}
	parent := filepath.Dir(dir)
	if parent == dir {
		return nil
	}
	return checkAncestorsHop(parent, hops+1)
}

// readLinkTarget reads the symlink at link, whose containing directory dir
// has already passed checkAncestors, and returns the path it points to. A
// relative target is joined onto dir's PHYSICAL path, not its text: the
// kernel resolves the target from the directory the link really lives in, so
// when dir itself runs through a symlinked directory, a lexical
// filepath.Join(dir, target) collapses any ".." in the target against the
// wrong parent (a/link -> b/c with b/c/x -> ../y names b/y, never a/y).
// Resolving dir in one EvalSymlinks call is safe here because every symlink
// along it was already checked hop by hop on the way in.
//
// A target with a ".." after any other component is refused outright,
// relative or absolute. The kernel takes that ".." from wherever the
// component before it really leads, through that component's own symlinks,
// while filepath.Join and the walk's own filepath.Dir drop the pair
// textually, so the directories the kernel actually passes through are never
// checked: with x/d -> ../e/s, x/L -> d/../y names e/y, not x/y, and an
// absolute x/L -> /r/x/d/../y is even opened at /r/e/y while only /r/x and
// its ancestors are checked, never /r/e. A leading run of ".." is kept: it is
// taken from dir's physical path, which has no symlinks left to take it
// through.
func readLinkTarget(link, dir string) (string, error) {
	target, err := os.Readlink(link)
	if err != nil {
		return "", fmt.Errorf("readlink %s: %w", link, err)
	}
	if hasInteriorDotDot(target) {
		return "", fmt.Errorf(
			`symlink %s points at %q, whose ".." follows another component; the kernel resolves that ".." through the earlier component's own symlinks, which this check would skip, so rewrite the link without the inner ".."`,
			link, target)
	}
	if filepath.IsAbs(target) {
		return target, nil
	}
	physical, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", dir, err)
	}
	return filepath.Join(physical, target), nil
}

// hasInteriorDotDot reports whether target has a ".." component after some
// other component, setting aside "." and the empty components a leading or
// doubled "/" leaves: the shape readLinkTarget refuses.
func hasInteriorDotDot(target string) bool {
	named := false
	for _, elem := range strings.Split(target, "/") {
		switch elem {
		case "", ".":
		case "..":
			if named {
				return true
			}
		default:
			named = true
		}
	}
	return false
}

// checkOwnerAndMode applies the ownership and writability rules to one path
// (the leaf file, or one ancestor directory) already stat'd into fi.
//
// The owner check ALWAYS runs, sticky directory or not: sticky only changes
// who may remove or rename an ENTRY inside the directory, not who owns the
// directory itself, so exempting a sticky directory from the owner check
// too would accept a sticky directory owned by neither root nor the running
// euid — exactly the case F3 flags. Only the mode/writability check below is
// ever skipped for sticky, and only for a directory: isDir is false for
// every call on the leaf file, so the leaf can never reach that exemption
// regardless of what mode bits it happens to carry.
func checkOwnerAndMode(p string, fi caFileStat, isDir bool) error {
	noun := "file"
	if isDir {
		noun = "directory"
	}

	euid := uint32(os.Geteuid())
	if fi.uid != 0 && fi.uid != euid {
		return fmt.Errorf(
			"%s %s is owned by uid %d, neither root nor the uid running this process (%d); an owner who is neither could have written it to a CA this process should not trust",
			noun, p, fi.uid, euid)
	}

	if isDir && fi.mode&os.ModeSticky != 0 {
		// Mode/writability check exempted: anyone may create an entry in a
		// sticky directory, but only that entry's own owner may remove or
		// rename it, so a sticky world-writable directory does not let
		// another user replace this file's directory entry from above.
		// Ownership was already checked unconditionally above.
		return nil
	}

	mode := fi.mode.Perm()
	if mode&0o002 != 0 {
		return fmt.Errorf("%s %s has mode %04o, world-writable; chmod o-w it", noun, p, mode)
	}
	if mode&0o020 != 0 && !isUserPrivateGroupFn(fi.uid, fi.gid) {
		return fmt.Errorf(
			"%s %s has mode %04o, group-writable by a group that is not its owner's own user-private group; chmod g-w it, or move it under a group whose only member is the owner",
			noun, p, mode)
	}
	return nil
}
