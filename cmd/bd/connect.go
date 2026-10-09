package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	bdhttp "github.com/steveyegge/beads/backend/http"
	"github.com/steveyegge/beads/cmd/bd/doctor"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/httpclient"
	"github.com/steveyegge/beads/internal/metrics"
)

// Written fresh for OSS beads S6 (no bd-enterprise source copied): richer
// connect flows (interactive login, server discovery, storage-profile
// prompts) are explicitly out of scope for this lift (see DESIGN.txt sec
// 3.1). This is the minimal OSS connect DESIGN.txt asks for: verify a
// server, then record it.

var (
	connectExpectProjectID string
	connectCAFile          string
	connectForce           bool
	connectAllowPlaintext  bool
	connectClear           bool
)

var connectCmd = &cobra.Command{
	Use:     "connect [url]",
	GroupID: "setup",
	Short:   "Attach this workspace to a remote bd serve over HTTP",
	Long: `Attach this workspace to a remote bd serve over HTTP.

bd connect verifies the server first (a handshake: api_version, wire
revision, and — when --expect-project-id is given — workspace identity) and
writes nothing until that succeeds. On success it writes the per-user
activation sidecar (.beads/http_target.json, never git-tracked) and sets this
workspace's metadata.json to backend "http".

A credential, if the server requires one, is never read from a flag or
recorded on disk by this command. It comes from the same ladder every http
request does: the BEADS_HTTP_TOKEN environment variable, then
BEADS_HTTP_TOKEN_COMMAND (a helper that prints a token), then the credentials
file's [host:port] section, then no credential at all — which the tip OSS
server's loopback-trust posture answers legitimately, not as a failure. Both
variables name the server they are for, host[:port]=value (for example
BEADS_HTTP_TOKEN=bd.example.com=<token>); a bare value is refused, since it
would be sent to whatever server the workspace's http_target.json names.

--allow-plaintext IS recorded on disk, in the sidecar alongside the server
url: once granted here, a credential may cross this one server in the clear
on every later command too, not only this one, without re-passing the flag
or setting BEADS_HTTP_ALLOW_INSECURE=1 yourself. A later connect to a
DIFFERENT url starts that grant over; it is never carried forward to a
server it was not given for.

Examples:
  bd connect http://127.0.0.1:8080                         # loopback, no TLS needed
  bd connect https://bd.example.com --expect-project-id p1 # pin workspace identity
  bd connect https://bd.example.com --ca-file ./ca.pem     # trust only this CA
  bd connect --clear                                       # forget the server, restoring the previous backend
`,
	Args:          cobra.MaximumNArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	// connect is how an http workspace gets its FIRST local state: there is
	// no local database to find yet (none ever exists for this backend), so
	// it opts out of PersistentPreRunE's store init the same way doctor,
	// schema and worktree do, via skipStoreAnnotation rather than editing
	// the central noDbCommands list (see that annotation's own doc comment).
	Annotations: map[string]string{skipStoreAnnotation: "1"},
	RunE: func(cmd *cobra.Command, args []string) error {
		evt := metrics.NewCommandEvent("connect")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		// connect, like init, is how a workspace gets its FIRST local state:
		// there may be no .beads directory yet anywhere above the CWD, so
		// this resolves the same way init's own target does (BEADS_DIR, then
		// the worktree fallback, then CWD/.beads) rather than
		// beads.GetRepoContextAllowingNoGit(), which answers "no .beads
		// directory found" with a hard error because every OTHER caller of
		// it (bd context, the proxied context route) is read-only and has
		// nothing to create.
		beadsDir := resolveInitBeadsDir()
		if beadsDir == "" {
			return HandleError("cannot resolve a workspace directory for connect (set BEADS_DIR, or run from inside a writable directory)")
		}

		if connectClear {
			if len(args) != 0 {
				return HandleError("connect --clear takes no url argument")
			}
			// Read the sidecar's PreviousBackend BEFORE removing it: it is
			// the backend metadata.json selected right before this
			// workspace connected (bee-ghosttrack CHANGES_REQUESTED on
			// #7288, should-fix 1). A missing sidecar (ErrNotConnected) or
			// any other read failure just means there is nothing to
			// restore, matching the "nothing was configured" case below.
			priorTarget, loadErr := httpclient.LoadTarget(beadsDir)
			removed, err := httpclient.RemoveTarget(beadsDir)
			if err != nil {
				return HandleError("clearing %s: %v", httpclient.TargetFileName, err)
			}
			restoredBackend := ""
			if removed && loadErr == nil && priorTarget.PreviousBackend != "" {
				if cfg, cfgErr := configfile.Load(beadsDir); cfgErr == nil && cfg != nil && cfg.GetBackend() == httpclient.Backend {
					cfg.Backend = priorTarget.PreviousBackend
					if saveErr := cfg.Save(beadsDir); saveErr == nil {
						restoredBackend = priorTarget.PreviousBackend
					}
					// A save failure is not fatal to --clear: the sidecar is
					// already gone (or never existed), which is the part
					// that actually detaches the workspace from the
					// server; metadata.json just keeps saying "http" and
					// the manual fallback message below still applies.
				}
			}
			if jsonOutput {
				return outputJSON(map[string]any{"cleared": removed, "restored_backend": restoredBackend})
			}
			switch {
			case restoredBackend != "":
				fmt.Printf("Removed %s and restored this workspace's backend selection in metadata.json to %q.\n", httpclient.TargetFileName, restoredBackend)
			case removed:
				// Not `bd config set backend ...`: that writes the database's
				// own config table, never metadata.json, and must first open
				// the very store this workspace can no longer reach.
				fmt.Printf("Removed %s. This workspace's backend selection in metadata.json is unchanged; run `bd connect <url>` to reconnect, or set \"backend\" in %s to pick a different backend.\n", httpclient.TargetFileName, filepath.Join(beadsDir, configfile.ConfigFileName))
			default:
				fmt.Println("No http target was configured; nothing to clear.")
			}
			return nil
		}

		if len(args) != 1 {
			return HandleError("connect requires a <url> argument (or --clear)")
		}
		rawURL := args[0]

		parsed, err := url.Parse(rawURL)
		if err != nil || parsed.Host == "" {
			// A parse failure has no *url.URL to redact, so there is nothing
			// safer to print than the raw argument; a parse that succeeded
			// but found no host (e.g. "http://user:pass@/path") does have
			// one, and .Redacted() strips its userinfo before this message
			// repeats it.
			if err != nil {
				return HandleError("invalid url %q", rawURL)
			}
			return HandleError("invalid url %q", parsed.Redacted())
		}
		// redactedURL is what every print/wrap of the connect target uses from
		// here on: rawURL is the user's literal argument, which — unlike
		// parsed.Redacted() — still carries a userinfo password verbatim
		// (http://user:pass@host/...) if one was pasted into the URL. Nothing
		// past this point should format rawURL again.
		redactedURL := parsed.Redacted()
		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			return HandleError("url %q must use http or https, got %q", redactedURL, parsed.Scheme)
		}
		// L1 (host safety, functional requirement 1): a bearer credential sent
		// in the clear to anything but loopback is a token leaked to every
		// network hop between here and the server. Refuse by default; require
		// an explicit opt-in rather than a warning nobody reads at connect
		// time, since connect time is the one moment a human is looking.
		if parsed.Scheme == "http" && !configfile.IsLocalHostString(parsed.Hostname()) && !connectAllowPlaintext {
			return HandleError(
				"refusing to connect to %s over plain http: %s is not loopback, and any bearer credential this workspace sends would cross the network unencrypted; use https://, or pass --allow-plaintext to connect anyway",
				redactedURL, parsed.Hostname())
		}

		existing, err := configfile.Load(beadsDir)
		if err != nil {
			return HandleError("reading %s: %v", configfile.ConfigFileName, err)
		}
		if existing != nil {
			if b := existing.GetBackend(); b != "" && b != httpclient.Backend && !connectForce {
				return HandleError("this workspace already selects backend %q; pass --force to switch it to %q (existing local data, if any, is left in place but will no longer be used)", b, httpclient.Backend)
			}
		}

		// effectiveExpectedProjectID carries the PRIOR sidecar's pin forward
		// into this run, so a bare re-connect (no --expect-project-id) to a
		// DIFFERENT server still gets caught: without this, every connect
		// built a brand-new Target starting from connectExpectProjectID alone
		// (almost always "" on a re-run), which the "nothing was pinned yet"
		// branch below would then adopt from whatever server just answered —
		// silently re-pinning the workspace to a different project with no
		// refusal at all. --force (regardless of --expect-project-id) drops
		// the prior pin entirely and lets the new server's answer win, which
		// is the explicit "I mean it" escape hatch this check otherwise has
		// none of.
		effectiveExpectedProjectID := connectExpectProjectID
		if !connectForce {
			priorTarget, loadErr := httpclient.LoadTarget(beadsDir)
			if loadErr != nil && !errors.Is(loadErr, httpclient.ErrNotConnected) {
				return HandleError("reading existing %s: %v", httpclient.TargetFileName, loadErr)
			}
			if loadErr == nil && priorTarget.ExpectProjectID != "" {
				switch {
				case effectiveExpectedProjectID == "":
					effectiveExpectedProjectID = priorTarget.ExpectProjectID
				case effectiveExpectedProjectID != priorTarget.ExpectProjectID:
					return HandleError(
						"this workspace is already connected to project %q; pass --force to re-pin it to %q, or pass --expect-project-id %q to confirm the existing pin",
						priorTarget.ExpectProjectID, effectiveExpectedProjectID, priorTarget.ExpectProjectID)
				}
			}
		}

		var caFile string
		dialOpts := httpclient.DialOptions{
			UserAgent: "bd/" + Version + " " + httpclient.WireUserAgentSuffix,
			// The L1 refusal above already gated this exact combination
			// (plain http, non-loopback) on --allow-plaintext; carrying the
			// flag through here keeps this command's own Handshake probe
			// (which dials with the same ambient bearer ladder a credential
			// might come from) from re-refusing what the flag already
			// permitted. See DialOptions.AllowInsecureCredential.
			AllowInsecureCredential: connectAllowPlaintext,
		}
		if connectCAFile != "" {
			abs, err := filepath.Abs(connectCAFile)
			if err != nil {
				return HandleError("resolving --ca-file %q: %v", connectCAFile, err)
			}
			caFile = abs
			// Verify against EXACTLY this file, bypassing BEADS_HTTP_CA_FILE
			// entirely: a connect run with the env var set to a different CA
			// must not verify against the env's CA while writing the flag's
			// unverified one to the sidecar (see DialOptionsForFile's doc).
			dialOpts, err = httpclient.DialOptionsForFile(caFile, dialOpts)
			if err != nil {
				return HandleError("--ca-file %q: %v", connectCAFile, err)
			}
		}

		target := httpclient.Target{BaseURL: parsed, ExpectProjectID: effectiveExpectedProjectID, CAFile: caFile, AllowInsecureCredential: connectAllowPlaintext}

		ctx := context.Background()
		snapshot, err := httpclient.Handshake(ctx, target, dialOpts)
		if err != nil {
			return HandleError("connecting to %s: %v", redactedURL, err)
		}
		if target.ExpectProjectID == "" {
			// Nothing was pinned (a genuine first connect, or --force
			// dropped the prior pin above): record what the server said so
			// this workspace is tied to THIS project from here on.
			target.ExpectProjectID = snapshot.ProjectId
		} else if target.ExpectProjectID != snapshot.ProjectId {
			// effectiveExpectedProjectID is either an explicit
			// --expect-project-id or the prior sidecar's pin carried
			// forward above; either way the server disagrees, and --force
			// was not given (the only way to reach this line with a
			// mismatched prior pin AND --force would have cleared
			// effectiveExpectedProjectID's carry-forward, not left it set).
			return HandleError("server at %s owns project %q, not the pinned %q", redactedURL, snapshot.ProjectId, target.ExpectProjectID)
		}

		if err := os.MkdirAll(beadsDir, 0o700); err != nil {
			return HandleError("creating %s: %v", beadsDir, err)
		}
		// A fresh .beads directory (the common case: this backend has no
		// local database to have created one earlier) has no .gitignore yet.
		// Ensure one before writing the sidecar, the same way bd init and
		// doctor --fix do, so http_target.json (which carries a bearer
		// credential's host and, soon, DialOptions) is never a candidate for
		// `git add .` in the first commit after connect.
		if err := doctor.EnsureGitignoreForBeadsDir(beadsDir); err != nil {
			return HandleError("ensuring %s/.gitignore: %v", beadsDir, err)
		}
		// bdhttp.Attach writes both of this backend's files: the gitignored
		// identity sidecar (SaveTarget — which server, which project; see
		// NewFromConfig/NewReadOnlyFromConfig in internal/httpclient/store.go,
		// which read identity from the sidecar alone, never from
		// metadata.json) and metadata.json's backend SELECTION alone
		// (git-tracked, the same file every clone of this repo shares). It
		// leaves every other existing metadata.json field untouched — in
		// particular cfg.Database/cfg.ProjectID, which belong to whatever
		// backend this workspace selected before, and which a future
		// `bd connect --clear` switching back to that backend (dolt) still
		// needs intact.
		if err := bdhttp.Attach(beadsDir, target); err != nil {
			return HandleError("%v", err)
		}

		if jsonOutput {
			return outputJSON(map[string]any{
				"url":           redactedURL,
				"project_id":    target.ExpectProjectID,
				"bd_version":    snapshot.BdVersion,
				"api_version":   snapshot.ApiVersion,
				"wire_revision": snapshot.WireRevision,
				"ca_file":       caFile,
				"capabilities":  snapshot.Capabilities,
			})
		}
		fmt.Printf("Connected %s to %s\n", beadsDir, redactedURL)
		fmt.Printf("  project:        %s\n", target.ExpectProjectID)
		fmt.Printf("  bd serve:       %s (api %s, wire revision %d)\n", snapshot.BdVersion, snapshot.ApiVersion, snapshot.WireRevision)
		if caFile != "" {
			fmt.Printf("  ca_file:        %s\n", caFile)
		}
		return nil
	},
}

func init() {
	connectCmd.Flags().StringVar(&connectExpectProjectID, "expect-project-id", "", "pin the workspace identity the server must present at every handshake")
	connectCmd.Flags().StringVar(&connectCAFile, "ca-file", "", "trust ONLY this PEM file for this server (resolved and recorded as an absolute path)")
	connectCmd.Flags().BoolVar(&connectForce, "force", false, "allow switching a workspace that already selects a different backend")
	connectCmd.Flags().BoolVar(&connectAllowPlaintext, "allow-plaintext", false, "allow a plain http connection to a non-loopback host; remembered in the sidecar for every later command against this server")
	connectCmd.Flags().BoolVar(&connectClear, "clear", false, "remove this workspace's http activation sidecar and restore its previous backend selection in metadata.json")
	rootCmd.AddCommand(connectCmd)
}
