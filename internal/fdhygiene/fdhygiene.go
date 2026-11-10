// Package fdhygiene keeps descriptors that bd did not open from leaking into
// the long-lived children bd spawns (the managed dolt sql-server and the
// dbproxy child).
//
// Go's os/exec wires only fds 0/1/2 plus Cmd.ExtraFiles into a child, and the
// Go runtime sets FD_CLOEXEC on everything it opens itself. It does not close
// inherited fds above 2 that lack FD_CLOEXEC — those pass straight through to
// the child. A caller holding such an fd (a shell `exec 9>lock`, a systemd
// unit, a CI harness, an editor terminal) therefore pins it for the entire
// lifetime of a detached server bd starts, which outlives the caller. See
// GH#4634: an flock held on fd 9 by a sync script was inherited by a
// cold-started sql-server and read as "held" by every subsequent run until the
// server was restarted.
//
// Go gives no hook between fork and exec, so the descriptors have to be marked
// in the parent. See MarkInheritedCloexec for why that is safe here.
package fdhygiene

// MarkInheritedCloexec sets FD_CLOEXEC on every open descriptor above 2 that
// does not already have it, so the next exec from this process does not hand
// them to the child. It returns the descriptors it changed, in ascending
// order, for logging; a nil return means nothing needed marking. Errors are
// not reported: this is best-effort hardening on a spawn path that must still
// start the server if the fd table cannot be enumerated.
//
// On Windows this is a no-op — handles are non-inheritable unless explicitly
// passed, so there is nothing to sanitize.
//
// Why marking in the parent is safe, given that another goroutine may open or
// close descriptors concurrently:
//
//   - The operation is monotonic. It only ever *adds* FD_CLOEXEC; it never
//     clears it. If fd N is recycled between the scan and the fcntl, the fd
//     that gets marked is a Go-opened one, which the runtime already opened
//     CLOEXEC — so the write is a no-op rather than a leak. Restoring the
//     original flags after the spawn would invert this and could clear
//     FD_CLOEXEC on a recycled Go descriptor, so the flags are deliberately
//     left set.
//   - Nothing is missed. Descriptors inherited from bd's own parent — the only
//     ones that can lack FD_CLOEXEC — exist from process start and are
//     therefore visible to any scan. Descriptors opened concurrently by Go
//     already carry the flag.
//   - bd never passes descriptors to a child on purpose. There is no fd-passing
//     feature whose contract this could break, and Cmd.ExtraFiles is unaffected
//     (os/exec dups those in the child after exec-time flags are applied).
func MarkInheritedCloexec() []int {
	return markInheritedCloexec()
}
