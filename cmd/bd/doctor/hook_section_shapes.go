package doctor

// bdShellSectionShapes lists the code of every section bd has written into a
// git hook since the section markers arrived (GH#1380): cmd/bd's
// generateHookSection as of each commit named below, from its
// "if command -v bd" line to the closing fi. In the code, @HOOK@ stands for
// the hook name and @TIMEOUT@ for the default timeout in seconds; timeout is
// the value those versions wrote, so one code appears once per value.
//
// isBdShellSectionBody counts a section in a non-shell hook as bd's only when
// its code is one of these, so repairing the hook never deletes a line bd did
// not write (GH#6348). bd no longer writes the section into a non-shell hook,
// but a shell hook that holds one can later get another interpreter, so when
// generateHookSection changes, append its new code here.
// TestGenerateHookSectionIsAKnownShape in cmd/bd fails until you do.
var bdShellSectionShapes = []struct {
	code    string
	timeout string
}{
	// 1b56f58b31 (GH#1380); 7c8cd89e5c changed only the END marker.
	{code: bdSectionCodeGH1380},
	// 1d7bf35219 added the timeout: 30 seconds, then 300 from 71a0360cd2
	// (GH#2732).
	{code: bdSectionCodeTimeout, timeout: "30"},
	{code: bdSectionCodeTimeout, timeout: "300"},
	// 85549f4115 added the gtimeout and perl fallbacks.
	{code: bdSectionCodeFallbacks, timeout: "300"},
	// 36f7e8a702 counted exit 142 as a timeout only under perl.
	{code: bdSectionCodePerlExitOnly, timeout: "300"},
	// 766e6c17f2 (#5522) validated the timeout and chose GNU timeout by its
	// --version banner.
	{code: bdSectionCodeGNUTimeout, timeout: "300"},
	// 33bed587e0 (#6561) accepted uutils timeout too.
	{code: bdSectionCodeUutilsTimeout, timeout: "300"},
	// b587154a48 (#7858) supervised bd from perl; generateHookSection still
	// writes this code.
	{code: bdSectionCodePerlSupervisor, timeout: "300"},
}

const bdSectionCodeGH1380 = `
if command -v bd >/dev/null 2>&1; then
  export BD_GIT_HOOK=1
  bd hooks run @HOOK@ "$@"
  _bd_exit=$?; if [ $_bd_exit -ne 0 ]; then exit $_bd_exit; fi
fi
`

const bdSectionCodeTimeout = `
if command -v bd >/dev/null 2>&1; then
  export BD_GIT_HOOK=1
  _bd_timeout=${BEADS_HOOK_TIMEOUT:-@TIMEOUT@}
  if command -v timeout >/dev/null 2>&1; then
    timeout "$_bd_timeout" bd hooks run @HOOK@ "$@"
    _bd_exit=$?
    if [ $_bd_exit -eq 124 ]; then
      echo >&2 "beads: hook '@HOOK@' timed out after ${_bd_timeout}s — continuing without beads"
      _bd_exit=0
    fi
  else
    bd hooks run @HOOK@ "$@"
    _bd_exit=$?
  fi
  if [ $_bd_exit -eq 3 ]; then
    echo >&2 "beads: database not initialized — skipping hook '@HOOK@'"
    _bd_exit=0
  fi
  if [ $_bd_exit -ne 0 ]; then exit $_bd_exit; fi
fi
`

const bdSectionCodeFallbacks = `
if command -v bd >/dev/null 2>&1; then
  export BD_GIT_HOOK=1
  _bd_timeout=${BEADS_HOOK_TIMEOUT:-@TIMEOUT@}
  if command -v timeout >/dev/null 2>&1; then
    timeout "$_bd_timeout" bd hooks run @HOOK@ "$@"
    _bd_exit=$?
  elif command -v gtimeout >/dev/null 2>&1; then
    gtimeout "$_bd_timeout" bd hooks run @HOOK@ "$@"
    _bd_exit=$?
  elif command -v perl >/dev/null 2>&1; then
    perl -e 'alarm shift; exec @ARGV' "$_bd_timeout" bd hooks run @HOOK@ "$@"
    _bd_exit=$?
  else
    echo >&2 "beads: hook '@HOOK@' running without timeout; install coreutils or perl to enable BEADS_HOOK_TIMEOUT"
    bd hooks run @HOOK@ "$@"
    _bd_exit=$?
  fi
  if [ $_bd_exit -eq 124 ] || [ $_bd_exit -eq 142 ]; then
    echo >&2 "beads: hook '@HOOK@' timed out after ${_bd_timeout}s — continuing without beads"
    _bd_exit=0
  fi
  if [ $_bd_exit -eq 3 ]; then
    echo >&2 "beads: database not initialized — skipping hook '@HOOK@'"
    _bd_exit=0
  fi
  if [ $_bd_exit -ne 0 ]; then exit $_bd_exit; fi
fi
`

const bdSectionCodePerlExitOnly = `
if command -v bd >/dev/null 2>&1; then
  export BD_GIT_HOOK=1
  _bd_timeout=${BEADS_HOOK_TIMEOUT:-@TIMEOUT@}
  _bd_used_perl=0
  if command -v timeout >/dev/null 2>&1; then
    timeout "$_bd_timeout" bd hooks run @HOOK@ "$@"
    _bd_exit=$?
  elif command -v gtimeout >/dev/null 2>&1; then
    gtimeout "$_bd_timeout" bd hooks run @HOOK@ "$@"
    _bd_exit=$?
  elif command -v perl >/dev/null 2>&1; then
    _bd_used_perl=1
    perl -e 'alarm shift; exec @ARGV' "$_bd_timeout" bd hooks run @HOOK@ "$@"
    _bd_exit=$?
  else
    echo >&2 "beads: hook '@HOOK@' running without timeout; install coreutils or perl to enable BEADS_HOOK_TIMEOUT"
    bd hooks run @HOOK@ "$@"
    _bd_exit=$?
  fi
  if [ $_bd_exit -eq 124 ] || { [ $_bd_used_perl -eq 1 ] && [ $_bd_exit -eq 142 ]; }; then
    echo >&2 "beads: hook '@HOOK@' timed out after ${_bd_timeout}s — continuing without beads"
    _bd_exit=0
  fi
  if [ $_bd_exit -eq 3 ]; then
    echo >&2 "beads: database not initialized — skipping hook '@HOOK@'"
    _bd_exit=0
  fi
  if [ $_bd_exit -ne 0 ]; then exit $_bd_exit; fi
fi
`

const bdSectionCodeGNUTimeout = `
if command -v bd >/dev/null 2>&1; then
  export BD_GIT_HOOK=1
  _bd_timeout=${BEADS_HOOK_TIMEOUT:-@TIMEOUT@}
  case "$_bd_timeout" in
    *[!0-9]*|'') _bd_timeout_invalid=1 ;;
    *[1-9]*) _bd_timeout_invalid=0 ;;
    *) _bd_timeout_invalid=1 ;;
  esac
  if [ "$_bd_timeout_invalid" -eq 1 ]; then
    echo >&2 "beads: invalid BEADS_HOOK_TIMEOUT; using @TIMEOUT@ seconds"
    _bd_timeout=@TIMEOUT@
  fi
  _bd_timeout_backend=none
  _bd_timeout_command=
  for _bd_timeout_candidate in timeout gtimeout; do
    if command -v "$_bd_timeout_candidate" >/dev/null 2>&1; then
      if _bd_timeout_version="$("$_bd_timeout_candidate" --version 2>/dev/null)"; then
        case "$_bd_timeout_version" in
          "timeout (GNU coreutils) "*) _bd_timeout_command=$_bd_timeout_candidate; break ;;
        esac
      fi
    fi
  done
  if [ -n "$_bd_timeout_command" ]; then
    _bd_timeout_backend=coreutils
    if "$_bd_timeout_command" -- "$_bd_timeout" bd hooks run @HOOK@ "$@"; then
      _bd_exit=0
    else
      _bd_exit=$?
    fi
  elif command -v perl >/dev/null 2>&1; then
    _bd_timeout_backend=perl
    if perl -e 'alarm shift; exec @ARGV' -- "$_bd_timeout" bd hooks run @HOOK@ "$@"; then
      _bd_exit=0
    else
      _bd_exit=$?
    fi
  else
    echo >&2 "beads: hook '@HOOK@' running without timeout; install coreutils or perl to enable BEADS_HOOK_TIMEOUT"
    if bd hooks run @HOOK@ "$@"; then
      _bd_exit=0
    else
      _bd_exit=$?
    fi
  fi
  if { [ "$_bd_timeout_backend" = coreutils ] && [ "$_bd_exit" -eq 124 ]; } || { [ "$_bd_timeout_backend" = perl ] && [ "$_bd_exit" -eq 142 ]; }; then
    echo >&2 "beads: hook '@HOOK@' timed out after ${_bd_timeout}s — continuing without beads"
    _bd_exit=0
  fi
  if [ "$_bd_exit" -eq 3 ]; then
    echo >&2 "beads: database not initialized — skipping hook '@HOOK@'"
    _bd_exit=0
  fi
  if [ "$_bd_exit" -ne 0 ]; then exit "$_bd_exit"; fi
fi
`

const bdSectionCodeUutilsTimeout = `
if command -v bd >/dev/null 2>&1; then
  export BD_GIT_HOOK=1
  _bd_timeout=${BEADS_HOOK_TIMEOUT:-@TIMEOUT@}
  case "$_bd_timeout" in
    *[!0-9]*|'') _bd_timeout_invalid=1 ;;
    *[1-9]*) _bd_timeout_invalid=0 ;;
    *) _bd_timeout_invalid=1 ;;
  esac
  if [ "$_bd_timeout_invalid" -eq 1 ]; then
    echo >&2 "beads: invalid BEADS_HOOK_TIMEOUT; using @TIMEOUT@ seconds"
    _bd_timeout=@TIMEOUT@
  fi
  _bd_timeout_backend=none
  _bd_timeout_command=
  for _bd_timeout_candidate in timeout gtimeout; do
    if command -v "$_bd_timeout_candidate" >/dev/null 2>&1; then
      if _bd_timeout_version="$("$_bd_timeout_candidate" --version 2>/dev/null)"; then
        case "$_bd_timeout_version" in
          "timeout (GNU coreutils) "*|"timeout (uutils coreutils) "*) _bd_timeout_command=$_bd_timeout_candidate; break ;;
        esac
      fi
    fi
  done
  if [ -n "$_bd_timeout_command" ]; then
    _bd_timeout_backend=coreutils
    if "$_bd_timeout_command" -- "$_bd_timeout" bd hooks run @HOOK@ "$@"; then
      _bd_exit=0
    else
      _bd_exit=$?
    fi
  elif command -v perl >/dev/null 2>&1; then
    _bd_timeout_backend=perl
    if perl -e 'alarm shift; exec @ARGV' -- "$_bd_timeout" bd hooks run @HOOK@ "$@"; then
      _bd_exit=0
    else
      _bd_exit=$?
    fi
  else
    echo >&2 "beads: hook '@HOOK@' running without timeout; install coreutils or perl to enable BEADS_HOOK_TIMEOUT"
    if bd hooks run @HOOK@ "$@"; then
      _bd_exit=0
    else
      _bd_exit=$?
    fi
  fi
  if { [ "$_bd_timeout_backend" = coreutils ] && [ "$_bd_exit" -eq 124 ]; } || { [ "$_bd_timeout_backend" = perl ] && [ "$_bd_exit" -eq 142 ]; }; then
    echo >&2 "beads: hook '@HOOK@' timed out after ${_bd_timeout}s — continuing without beads"
    _bd_exit=0
  fi
  if [ "$_bd_exit" -eq 3 ]; then
    echo >&2 "beads: database not initialized — skipping hook '@HOOK@'"
    _bd_exit=0
  fi
  if [ "$_bd_exit" -ne 0 ]; then exit "$_bd_exit"; fi
fi
`

const bdSectionCodePerlSupervisor = `
if command -v bd >/dev/null 2>&1; then
  export BD_GIT_HOOK=1
  _bd_timeout=${BEADS_HOOK_TIMEOUT:-@TIMEOUT@}
  case "$_bd_timeout" in
    *[!0-9]*|'') _bd_timeout_invalid=1 ;;
    *[1-9]*) _bd_timeout_invalid=0 ;;
    *) _bd_timeout_invalid=1 ;;
  esac
  if [ "$_bd_timeout_invalid" -eq 1 ]; then
    echo >&2 "beads: invalid BEADS_HOOK_TIMEOUT; using @TIMEOUT@ seconds"
    _bd_timeout=@TIMEOUT@
  fi
  _bd_timeout_backend=none
  _bd_timeout_command=
  for _bd_timeout_candidate in timeout gtimeout; do
    if command -v "$_bd_timeout_candidate" >/dev/null 2>&1; then
      if _bd_timeout_version="$("$_bd_timeout_candidate" --version 2>/dev/null)"; then
        case "$_bd_timeout_version" in
          "timeout (GNU coreutils) "*|"timeout (uutils coreutils) "*) _bd_timeout_command=$_bd_timeout_candidate; break ;;
        esac
      fi
    fi
  done
  if [ -n "$_bd_timeout_command" ]; then
    _bd_timeout_backend=coreutils
    if "$_bd_timeout_command" -- "$_bd_timeout" bd hooks run @HOOK@ "$@"; then
      _bd_exit=0
    else
      _bd_exit=$?
    fi
  elif command -v perl >/dev/null 2>&1; then
    _bd_timeout_backend=perl
    if perl -e '
      my $t = shift;
      my $pid = fork;
      defined $pid or die "beads: fork failed: $!\n";
      if (!$pid) { setpgrp(0, 0); exec { $ARGV[0] } @ARGV; warn "beads: cannot run $ARGV[0]: $!\n"; exit 127 }
      my ($sig, $sent, $done) = ("TERM", 0, 0);
      $SIG{ALRM} = sub { return if $done; kill $sig, -$pid; ($sig, $sent) = ("KILL", 1); alarm 2 };
      $SIG{$_} = sub { kill $_[0], -$pid unless $done } for qw(HUP INT TERM);
      alarm $t;
      $done = waitpid $pid, 0;
      alarm 0;
      exit 142 if $sent;
      exit($? & 127 ? 128 + ($? & 127) : $? >> 8);
    ' -- "$_bd_timeout" bd hooks run @HOOK@ "$@"; then
      _bd_exit=0
    else
      _bd_exit=$?
    fi
  else
    echo >&2 "beads: hook '@HOOK@' running without timeout; install coreutils or perl to enable BEADS_HOOK_TIMEOUT"
    if bd hooks run @HOOK@ "$@"; then
      _bd_exit=0
    else
      _bd_exit=$?
    fi
  fi
  if { [ "$_bd_timeout_backend" = coreutils ] && [ "$_bd_exit" -eq 124 ]; } || { [ "$_bd_timeout_backend" = perl ] && [ "$_bd_exit" -eq 142 ]; }; then
    echo >&2 "beads: hook '@HOOK@' timed out after ${_bd_timeout}s — continuing without beads"
    _bd_exit=0
  fi
  if [ "$_bd_exit" -eq 3 ]; then
    echo >&2 "beads: database not initialized — skipping hook '@HOOK@'"
    _bd_exit=0
  fi
  if [ "$_bd_exit" -ne 0 ]; then exit "$_bd_exit"; fi
fi
`
