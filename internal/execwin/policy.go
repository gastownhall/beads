package execwin

// The Windows process-creation flag bits the console policy needs,
// duplicated here so the policy core is testable on every platform
// (golang.org/x/sys/windows is only importable on Windows).
// execwin_windows_test.go pins each one against the x/sys/windows constant
// it mirrors.
const (
	// flagCreateNoWindow is CREATE_NO_WINDOW: the child gets a console of
	// its own, but it is not visible.
	flagCreateNoWindow uint32 = 0x08000000
	// flagDetachedProcess is DETACHED_PROCESS: the child gets no console
	// at all, so every console program it spawns allocates a visible one.
	flagDetachedProcess uint32 = 0x00000008
	// flagCreateNewConsole is CREATE_NEW_CONSOLE: the child gets a new,
	// visible console.
	flagCreateNewConsole uint32 = 0x00000010
	// flagCreateNewProcessGroup is CREATE_NEW_PROCESS_GROUP: the Windows
	// analog of Unix Setpgid, and the flag the long-lived children need to
	// keep.
	flagCreateNewProcessGroup uint32 = 0x00000200
)

// hideCreationFlags is Hide's policy, as a pure seam so it can be tested on
// any platform: given whether the parent has a console attached, and the
// CreationFlags the caller already chose, it returns the flags the child
// should be created with and whether they changed.
//
// An attached parent keeps the caller's flags: the child inherits the
// console it can see. An explicit console choice (DETACHED_PROCESS,
// CREATE_NEW_CONSOLE, or a flag that already suppresses the window) is
// never second-guessed. Otherwise CREATE_NO_WINDOW is added, preserving
// every bit the caller set.
func hideCreationFlags(attached bool, flags uint32) (uint32, bool) {
	if attached {
		return flags, false
	}
	if flags != 0 {
		// Caller-owned attributes: AC-3 of bd-l5o8ld merges them into a
		// copy. Until then Hide leaves any explicit attributes alone.
		return flags, false
	}
	return flagCreateNoWindow, true
}

// backgroundCreationFlags is BackgroundSysProcAttr's Windows policy: a
// hidden console of the child's own (CREATE_NO_WINDOW) plus the process
// group detachment the long-lived children exist for
// (CREATE_NEW_PROCESS_GROUP), and deliberately no DETACHED_PROCESS, which
// would leave the child console-less and make its own console-subsystem
// grandchildren visible.
func backgroundCreationFlags() uint32 {
	return flagCreateNoWindow | flagCreateNewProcessGroup
}
