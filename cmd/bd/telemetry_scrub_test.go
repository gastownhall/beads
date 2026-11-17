package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// telemetryArgsFixture is a command tree shaped like bd's: a root carrying
// bd's persistent flags and subcommands binding the flags that take issue
// content, search text and credentials, including the -p shorthand that
// means --password on federation add-peer and --priority or --prefix
// elsewhere. Executing it records what commandTelemetryArgs renders for the
// command cobra resolved, from the PersistentPreRunE position where main.go
// starts the command span.
type telemetryArgsFixture struct {
	root *cobra.Command
	got  string
}

func newTelemetryArgsFixture() *telemetryArgsFixture {
	fx := &telemetryArgsFixture{}
	noop := func(*cobra.Command, []string) error { return nil }
	fx.root = &cobra.Command{
		Use:           "bd",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			fx.got = commandTelemetryArgs(cmd)
			return nil
		},
		RunE: noop,
	}
	persistent := fx.root.PersistentFlags()
	persistent.Bool("json", false, "")
	persistent.BoolP("quiet", "q", false, "")
	persistent.BoolP("verbose", "v", false, "")
	persistent.String("actor", "", "")

	create := &cobra.Command{Use: "create [title]", RunE: noop}
	create.Flags().String("title", "", "")
	create.Flags().StringP("description", "d", "", "")
	create.Flags().String("notes", "", "")
	create.Flags().StringSliceP("labels", "l", nil, "")
	create.Flags().IntP("priority", "p", 2, "")

	list := &cobra.Command{Use: "list", RunE: noop}
	list.Flags().String("title", "", "")
	list.Flags().IntP("limit", "n", 50, "")

	comments := &cobra.Command{Use: "comments [issue-id]", RunE: noop}
	comments.AddCommand(&cobra.Command{Use: "add <issue-id> <text>", RunE: noop})

	ready := &cobra.Command{Use: "ready", RunE: noop}
	ready.Flags().IntP("priority", "p", 0, "")

	initCmd := &cobra.Command{Use: "init", RunE: noop}
	initCmd.Flags().StringP("prefix", "p", "", "")
	initCmd.Flags().String("remote", "", "")

	addPeer := &cobra.Command{Use: "add-peer <name> <url>", RunE: noop}
	addPeer.Flags().String("user", "", "")
	addPeer.Flags().StringP("password", "p", "", "")
	federation := &cobra.Command{Use: "federation"}
	federation.AddCommand(addPeer)

	fx.root.AddCommand(
		create, list, comments, ready, initCmd, federation,
		&cobra.Command{Use: "search [query]", RunE: noop},
		// bd mail hands its arguments to a delegate unparsed.
		&cobra.Command{Use: "mail", DisableFlagParsing: true, RunE: noop},
	)
	return fx
}

// telemetryArgs runs argv through a fresh fixture and returns bd.args.
func telemetryArgs(t *testing.T, argv ...string) string {
	t.Helper()
	fx := newTelemetryArgsFixture()
	fx.got = "<PersistentPreRunE did not run>"
	fx.root.SetArgs(argv)
	if err := fx.root.Execute(); err != nil {
		t.Fatalf("bd %q: %v", argv, err)
	}
	return fx.got
}

// TestCommandTelemetryArgsCarryNoContent pins #8505: bd.args is the command
// path plus the sorted long names of the flags that were set, and nothing a
// user typed as a value or an argument reaches it. The value scrubber this
// replaced redacted credentials but passed titles, descriptions, labels and
// search text through verbatim.
func TestCommandTelemetryArgsCarryNoContent(t *testing.T) {
	const secret = "SECRET-8505"
	cases := []struct {
		name string
		argv []string
		want string
	}{
		{"issue repro: positional title, -d description", []string{"create", secret + "-title", "-d", secret + "-desc", "-q"}, "create --description --quiet"},
		{"issue repro: list --title", []string{"list", "--limit", "1", "--title", secret}, "list --limit --title"},
		{"equals forms and a repeated slice flag", []string{"create", "--title=" + secret, "-l", "a," + secret, "-l", secret, "--notes", secret}, "create --labels --notes --title"},
		{"flags sorted whatever the command-line order", []string{"create", "-q", "--title", secret, "-d", secret}, "create --description --quiet --title"},
		{"persistent flag before the subcommand", []string{"-q", "create", secret}, "create --quiet"},
		{"search text", []string{"search", secret, "and more " + secret}, "search"},
		{"nested command path", []string{"--json", "comments", "add", "bd-1", secret}, "comments add --json"},
		{"actor value", []string{"--actor", secret, "ready"}, "ready --actor"},
		{"value shaped like a flag", []string{"create", "--title", "--password=" + secret}, "create --title"},
		{"value shaped like a flag the command binds", []string{"create", "-d", "--notes=" + secret}, "create --description"},
		{"arguments after --", []string{"create", "--", "--notes", secret}, "create"},
		{"concatenated shorthand value", []string{"create", "-d" + secret}, "create --description"},
		{"shorthand equals value", []string{"create", "-d=" + secret}, "create --description"},
		{"boolean shorthand cluster ending in a value flag", []string{"create", "-qd" + secret}, "create --description --quiet"},
		{"boolean shorthand cluster, separate value", []string{"create", "-qd", secret}, "create --description --quiet"},
		{"federation password", []string{"federation", "add-peer", "partner", "h:3306/db", "--user", "admin", "--password", secret}, "federation add-peer --password --user"},
		{"federation password equals form", []string{"federation", "add-peer", "partner", "h:3306/db", "--password=" + secret}, "federation add-peer --password"},
		{"federation password -p", []string{"federation", "add-peer", "partner", "h:3306/db", "-p", secret}, "federation add-peer --password"},
		{"federation password -p concatenated", []string{"federation", "add-peer", "partner", "h:3306/db", "-p" + secret}, "federation add-peer --password"},
		{"federation password -p=", []string{"federation", "add-peer", "partner", "h:3306/db", "-p=" + secret}, "federation add-peer --password"},
		{"federation password in a -qp cluster", []string{"federation", "add-peer", "partner", "h:3306/db", "-qp" + secret}, "federation add-peer --password --quiet"},
		{"federation password in a -vp cluster, separate value", []string{"federation", "add-peer", "partner", "h:3306/db", "-vp", secret}, "federation add-peer --password --verbose"},
		{"-p as --priority", []string{"ready", "-p", "1"}, "ready --priority"},
		{"-p as --prefix", []string{"init", "-p", secret}, "init --prefix"},
		{"command that disables flag parsing", []string{"mail", "send", "mayor/", "-s", secret}, "mail"},
		{"root command", []string{"--json"}, "--json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := telemetryArgs(t, tc.argv...)
			if strings.Contains(got, secret) {
				t.Fatalf("CONTENT LEAK: bd.args for %q = %q", tc.argv, got)
			}
			if got != tc.want {
				t.Fatalf("bd.args for %q = %q, want %q", tc.argv, got, tc.want)
			}
		})
	}
}

// TestCommandTelemetryArgsDropConnectionStrings feeds bd.args connection
// strings that leaked through, or nearly slipped past, the value scrubber that
// used to guard the attribute: percent-encoded userinfo and query keys, an
// escaped space, a quoted value, a \v separator, overlapping secrets and a
// malformed escape. As a positional argument or a flag value none of it
// reaches the attribute, because no argument or value does.
func TestCommandTelemetryArgsDropConnectionStrings(t *testing.T) {
	for _, dsn := range []string{
		"postgres://bts:s3cr3t-pw@127.0.0.1:5432/db",
		"postgres://u:SUPER%2ASECRET@h:5432/db",
		"postgres://u@h:5432/db?pass%77ord=SUPERSECRET",
		"postgres://bts@db.example/beads%zz?password=SUPERSECRET&sslmode=require",
		"postgres://u:foo@h:5432/db?password=fooACTUAL",
		`host=h user=u password=SUPER\ SECRET dbname=db`,
		"host=h user=u password='se cret SUPERSECRET' dbname=db",
		"host=h user=u\vpassword=SUPERSECRET dbname=db",
		"bts:s3cr3t-pw@tcp(127.0.0.1:3306)/db",
	} {
		for _, tc := range []struct {
			argv []string
			want string
		}{
			{[]string{"init", dsn}, "init"},
			{[]string{"init", "--remote", dsn}, "init --remote"},
			{[]string{"init", "--remote=" + dsn}, "init --remote"},
		} {
			if got := telemetryArgs(t, tc.argv...); got != tc.want {
				t.Errorf("bd.args for %q = %q, want %q", tc.argv, got, tc.want)
			}
		}
	}
}

// TestCommandTelemetryArgsNilCommand keeps the helper total: no command, no
// attribute text.
func TestCommandTelemetryArgsNilCommand(t *testing.T) {
	if got := commandTelemetryArgs(nil); got != "" {
		t.Fatalf("commandTelemetryArgs(nil) = %q, want empty", got)
	}
}
