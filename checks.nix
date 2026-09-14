# Flake checks. Run all with `nix flake check`; one with
# `nix build .#checks.<system>.<name> -L`.
pkgs:
let
  inherit (pkgs) lib;

  # hook-timeout-backends: run the tracked hook shims against REAL timeout
  # implementations.
  #
  # cmd/bd/hooks_timeout_process_test.go pins the shim's selection logic with
  # scripted helpers, and TestTrackedManagedHookSectionsMatchGenerator holds
  # the tracked .githooks/* byte-equal to the generator. This check closes the
  # remaining gap — what the helpers on real hosts actually do — by running the
  # tracked sections against the binaries themselves. That is where the
  # surprises live: uutils prints a different banner (GH#5541); busybox exits
  # 143 on expiry, not 124, so allowlisting it as-is would turn every timeout
  # into a failed hook; toybox exits 125 from --version.
  #
  # The matrix is derived from a model of the contract, so a change to the
  # allowlist is one line here and the binaries either agree or they do not.
  # Reading .githooks/* rather than building bd keeps this at about one
  # deadline of wall time and independent of the Go build.
  #
  # Each case: one hook, one shell (the `sh`s a `#!/usr/bin/env sh` hook meets
  # in the wild), a PATH of exactly one timeout implementation plus optionally
  # perl, BEADS_HOOK_TIMEOUT=1, and a fake `bd` that blocks on a held-open fifo
  # until a helper kills it. The fake records the signal that killed it, which
  # is how the check asserts the backend and not just "it finished":
  #   coreutils backend → TERM from timeout, shim reports the deadline, exit 0
  #   perl backend      → ALRM from perl's alarm, same report, exit 0
  #   no backend        → the fake returns at once; shim warns it is running
  #                       without a deadline, exit 0
  # A non-zero hook exit blocks the user's push, so every case asserts exit 0.
  # No clock is involved: if nothing kills the fake, the harness's own
  # `timeout 10` (on its full PATH, not the shim's) fails the case.

  # The identity allowlist in cmd/bd/hooks.go, as data.
  allowlisted = [
    "gnu-coreutils"
    "uutils-coreutils"
  ];

  implementations = {
    gnu-coreutils = pkgs.coreutils;
    uutils-coreutils = pkgs.uutils-coreutils-noprefix;
    none = null;
  }
  // lib.optionalAttrs pkgs.stdenv.isLinux {
    busybox = pkgs.busybox;
    toybox = pkgs.toybox;
  };

  shells = {
    dash = "${pkgs.dash}/bin/dash";
    bash = "${pkgs.bash}/bin/bash";
  }
  // lib.optionalAttrs pkgs.stdenv.isLinux {
    busybox-ash = "${pkgs.busybox}/bin/sh";
  };

  hooks = [
    "pre-commit"
    "post-merge"
    "pre-push"
    "post-checkout"
    "prepare-commit-msg"
  ];

  backendFor =
    impl: withPerl:
    if lib.elem impl allowlisted then
      "coreutils"
    else if withPerl then
      "perl"
    else
      "none";

  # One `run_case` line per point in the matrix, all backgrounded.
  cases = lib.concatLists (
    lib.mapAttrsToList (
      impl: pkg:
      lib.concatMap
        (
          withPerl:
          lib.concatLists (
            lib.mapAttrsToList (
              shellName: shell:
              map (hook: {
                name = "${hook}/${shellName}/${impl}${lib.optionalString withPerl "+perl"}";
                inherit hook shell;
                backend = backendFor impl withPerl;
                path = lib.optional (pkg != null) "${pkg}/bin" ++ lib.optional withPerl "${pkgs.perl}/bin";
              }) hooks
            ) shells
          )
        )
        [
          false
          true
        ]
    ) implementations
  );

  caseLine =
    c:
    "run_case ${
      lib.escapeShellArgs [
        c.name
        c.hook
        c.backend
        c.shell
      ]
    } ${lib.escapeShellArgs c.path} &\n";

  trackedHooks = lib.fileset.toSource {
    root = ./.;
    fileset = ./.githooks;
  };
in
{
  hook-timeout-backends =
    pkgs.runCommand "bd-hook-timeout-backends"
      {
        # Only .githooks/* is an input: nothing else in the repo changes what
        # this check measures.
        githooks = "${trackedHooks}/.githooks";
      }
      ''
        # 1. Shims under test: the managed section of each tracked hook, as
        #    bd installs it into a fresh repo (shebang + section).
        shims="$TMPDIR/shims"
        mkdir -p "$shims"
        for hook in ${lib.escapeShellArgs hooks}; do
          {
            echo '#!/usr/bin/env sh'
            sed -n '/^# --- BEGIN BEADS INTEGRATION/,/^# --- END BEADS INTEGRATION/p' "$githooks/$hook"
          } > "$shims/$hook"
          grep -q 'bd hooks run' "$shims/$hook" || { echo "no managed section in .githooks/$hook"; exit 1; }
        done

        # 2. The fake bd the shim will find on its restricted PATH. It blocks
        #    until killed and records the signal, re-raising it so it dies the
        #    way a real bd would (143 / 142), not by a made-up exit code.
        fake="$TMPDIR/fakebin"
        mkdir -p "$fake"
        cat > "$fake/bd" <<'FAKE'
        #!/bin/sh
        trap 'echo TERM > "$FAKE_BD_SIGNAL"; trap - TERM; kill -TERM $$' TERM
        trap 'echo ALRM > "$FAKE_BD_SIGNAL"; trap - ALRM; kill -ALRM $$' ALRM
        [ -n "$FAKE_BD_BLOCK" ] && read -r _
        exit 0
        FAKE
        chmod +x "$fake/bd"
        mkfifo "$TMPDIR/hang"
        exec 3<> "$TMPDIR/hang"

        # 3. The matrix.
        results="$TMPDIR/results"
        mkdir -p "$results"
        run_case() {
          name=$1 hook=$2 backend=$3 shell=$4
          shift 4
          case_dir="$TMPDIR/cases/$name"
          mkdir -p "$case_dir/bin"
          ln -s "$shell" "$case_dir/bin/sh"
          path="$fake:$case_dir/bin"
          for dir in "$@"; do path="$path:$dir"; done
          block=1
          [ "$backend" = none ] && block=
          signal_file="$case_dir/signal"
          # `|| rc=$?`: the builder runs under set -e, and a bare assignment
          # from a failing command would end this subshell before it reports.
          rc=0
          out=$(timeout 10 env PATH="$path" BEADS_HOOK_TIMEOUT=1 \
                  FAKE_BD_BLOCK="$block" FAKE_BD_SIGNAL="$signal_file" \
                  "$case_dir/bin/sh" "$shims/$hook" origin https://example.invalid 2>&1 <&3) || rc=$?
          signal=$(cat "$signal_file" 2>/dev/null || true)
          verdict=ok
          if [ "$rc" -eq 124 ]; then
            verdict="FAIL: nothing killed the fake bd (harness safety net fired)"
          elif [ "$rc" -ne 0 ]; then
            verdict="FAIL: hook exit $rc (would block the push): $out"
          else
            case "$backend" in
              coreutils)
                case "$out" in *"timed out after 1s"*) ;; *) verdict="FAIL: no deadline report: $out" ;; esac
                [ "$signal" = TERM ] || verdict="FAIL: expected coreutils timeout (TERM), fake saw '$signal'"
                ;;
              perl)
                case "$out" in *"timed out after 1s"*) ;; *) verdict="FAIL: no deadline report: $out" ;; esac
                [ "$signal" = ALRM ] || verdict="FAIL: expected perl alarm (ALRM), fake saw '$signal'"
                ;;
              none)
                case "$out" in *"running without timeout"*) ;; *) verdict="FAIL: no unbounded warning: $out" ;; esac
                ;;
            esac
          fi
          printf '%-52s %-10s %s\n' "$name" "$backend" "$verdict" > "$results/$(echo "$name" | tr / _)"
        }

        ${lib.concatMapStrings caseLine cases}
        wait

        printf '%-52s %-10s %s\n' CASE BACKEND RESULT
        sort "$results"/*
        echo
        reported=$(ls "$results" | wc -l)
        echo "$reported of ${toString (lib.length cases)} cases reported, $(grep -l FAIL "$results"/* | wc -l) failed"
        if [ "$reported" -ne ${toString (lib.length cases)} ]; then
          echo "some cases never reported a verdict"
          exit 1
        fi
        if grep -q FAIL "$results"/*; then
          exit 1
        fi
        touch "$out"
      '';
}
