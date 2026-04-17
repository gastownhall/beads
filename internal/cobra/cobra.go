// Package cobra provides a minimal drop-in replacement for github.com/spf13/cobra.
// It supports the subset of the cobra API used by beads' CLI commands.
package cobra

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ShellCompDirective is a bit mask to control behavior of shell completions.
type ShellCompDirective int

const ShellCompDirectiveNoFileComp ShellCompDirective = 4

// PositionalArgs is a validator for positional arguments.
type PositionalArgs func(cmd *Command, args []string) error

// NoArgs returns an error if any args are provided.
func NoArgs(cmd *Command, args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("%q accepts no arguments", cmd.Use)
	}
	return nil
}

// ArbitraryArgs accepts any number of args.
func ArbitraryArgs(_ *Command, _ []string) error { return nil }

// ExactArgs returns an error if there are not exactly n args.
func ExactArgs(n int) PositionalArgs {
	return func(cmd *Command, args []string) error {
		if len(args) != n {
			return fmt.Errorf("%q accepts %d arg(s), got %d", cmd.Use, n, len(args))
		}
		return nil
	}
}

// MinimumNArgs returns an error if fewer than n args are provided.
func MinimumNArgs(n int) PositionalArgs {
	return func(cmd *Command, args []string) error {
		if len(args) < n {
			return fmt.Errorf("%q requires at least %d arg(s), got %d", cmd.Use, n, len(args))
		}
		return nil
	}
}

// MaximumNArgs returns an error if more than n args are provided.
func MaximumNArgs(n int) PositionalArgs {
	return func(cmd *Command, args []string) error {
		if len(args) > n {
			return fmt.Errorf("%q accepts at most %d arg(s), got %d", cmd.Use, n, len(args))
		}
		return nil
	}
}

// RangeArgs returns an error if the number of args is not within the range.
func RangeArgs(min, max int) PositionalArgs {
	return func(cmd *Command, args []string) error {
		if len(args) < min || len(args) > max {
			return fmt.Errorf("%q accepts between %d and %d arg(s), got %d", cmd.Use, min, max, len(args))
		}
		return nil
	}
}

// Group is a logical group of commands for help display.
type Group struct {
	ID    string
	Title string
}

// Command is the central point of the CLI framework.
type Command struct {
	// Exported fields (set by callers)
	Use        string
	Short      string
	Long       string
	Example    string
	Aliases    []string
	Deprecated string
	GroupID    string
	Hidden     bool

	// Silencing fields — accepted but no-op in this shim.
	SilenceUsage      bool
	SilenceErrors     bool
	DisableFlagParsing bool

	Args              PositionalArgs
	ValidArgsFunction func(cmd *Command, args []string, toComplete string) ([]string, ShellCompDirective)

	Run              func(cmd *Command, args []string)
	RunE             func(cmd *Command, args []string) error
	PreRun           func(cmd *Command, args []string)
	PostRun          func(cmd *Command, args []string)
	PersistentPreRun func(cmd *Command, args []string)
	PersistentPreRunE func(cmd *Command, args []string) error
	PersistentPostRun func(cmd *Command, args []string)

	// internal
	inReader  io.Reader
	outWriter io.Writer
	ctx       context.Context
	parent   *Command
	children []*Command
	groups   []*Group
	flags    *FlagSet
	pflags   *FlagSet
	helpFunc func(*Command, []string)
	setArgs  []string // overrides os.Args when non-nil
}

// InOrStdin returns the reader used for stdin.
func (c *Command) InOrStdin() io.Reader {
	if c.inReader != nil {
		return c.inReader
	}
	return os.Stdin
}

// ErrOrStderr returns the writer used for stderr.
func (c *Command) ErrOrStderr() io.Writer {
	return os.Stderr
}

// OutOrStdout returns the writer used for stdout.
func (c *Command) OutOrStdout() io.Writer {
	if c.outWriter != nil {
		return c.outWriter
	}
	return os.Stdout
}

// Name returns the first word of Use.
func (c *Command) Name() string {
	name := c.Use
	if i := strings.Index(name, " "); i >= 0 {
		return name[:i]
	}
	return name
}

// Flags returns the flag set for this command's local flags.
func (c *Command) Flags() *FlagSet {
	if c.flags == nil {
		c.flags = newFlagSet()
	}
	return c.flags
}

// PersistentFlags returns the flag set for flags that persist across subcommands.
func (c *Command) PersistentFlags() *FlagSet {
	if c.pflags == nil {
		c.pflags = newFlagSet()
	}
	return c.pflags
}

// AddCommand adds one or more subcommands.
func (c *Command) AddCommand(cmds ...*Command) {
	for _, sub := range cmds {
		sub.parent = c
		c.children = append(c.children, sub)
	}
}

// AddGroup adds a command group for help display.
func (c *Command) AddGroup(groups ...*Group) {
	c.groups = append(c.groups, groups...)
}

// Commands returns the child commands.
func (c *Command) Commands() []*Command {
	return c.children
}

// Find finds a subcommand by name or alias.
func (c *Command) Find(name string) *Command {
	for _, sub := range c.children {
		if sub.Name() == name {
			return sub
		}
		for _, alias := range sub.Aliases {
			if alias == name {
				return sub
			}
		}
	}
	return nil
}

// Parent returns the parent command.
func (c *Command) Parent() *Command { return c.parent }

// Root returns the root command.
func (c *Command) Root() *Command {
	r := c
	for r.parent != nil {
		r = r.parent
	}
	return r
}

// SetArgs overrides os.Args for this command (used in tests).
func (c *Command) SetArgs(args []string) {
	c.setArgs = args
}

// SetHelpFunc sets a custom help function.
func (c *Command) SetHelpFunc(f func(*Command, []string)) {
	c.helpFunc = f
}

// InitDefaultHelpCmd sets up help command infrastructure.
// This is a no-op in the shim; help is handled by Execute.
func (c *Command) InitDefaultHelpCmd() {}

// Usage prints usage to stderr.
func (c *Command) Usage() error {
	c.printHelp(os.Stderr)
	return nil
}

// CalledAs returns the name or alias used to invoke this command.
func (c *Command) CalledAs() string { return c.Name() }

// MarkFlagRequired marks a flag as required. In this shim, validation is not
// enforced at flag-parse time, but required flags should be checked in Run.
func (c *Command) MarkFlagRequired(name string) error { return nil }

// MarkFlagsMutuallyExclusive marks flags as mutually exclusive.
// This shim accepts the call but does not enforce exclusivity at parse time.
func (c *Command) MarkFlagsMutuallyExclusive(flagNames ...string) {}

// MarkFlagsRequiredTogether marks flags as required together.
// This shim accepts the call but does not enforce at parse time.
func (c *Command) MarkFlagsRequiredTogether(flagNames ...string) {}

// IsAvailableCommand reports whether the command is available for use.
func (c *Command) IsAvailableCommand() bool {
	return c.Deprecated == ""
}

// UseLine returns the full usage line for the command.
func (c *Command) UseLine() string {
	if c.parent != nil {
		return c.parent.UseLine() + " " + c.Use
	}
	return c.Use
}

// NonInheritedFlags returns only the flags defined directly on this command.
func (c *Command) NonInheritedFlags() *FlagSet {
	if c.flags == nil {
		return newFlagSet()
	}
	return c.flags
}

// Groups returns the registered command groups.
func (c *Command) Groups() []*Group { return c.groups }

// Context returns the command's context, or context.Background() if not set.
func (c *Command) Context() context.Context {
	if c.ctx != nil {
		return c.ctx
	}
	return context.Background()
}

// SetContext sets the command context.
func (c *Command) SetContext(ctx context.Context) { c.ctx = ctx }

// UsageString returns the usage message as a string.
func (c *Command) UsageString() string {
	var buf bytes.Buffer
	c.printHelp(&buf)
	return buf.String()
}

// Help prints the help for this command.
func (c *Command) Help() error {
	c.printHelp(os.Stdout)
	return nil
}

// Execute runs the command tree, dispatching to the appropriate subcommand.
func (c *Command) Execute() error {
	var args []string
	if c.setArgs != nil {
		args = c.setArgs
	} else {
		args = os.Args[1:]
	}
	err := c.execute(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
	return nil
}

// execute is the internal dispatch function.
func (c *Command) execute(args []string) error {
	// Walk subcommands
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		// Check if it's a subcommand name or alias
		if sub := c.Find(args[0]); sub != nil {
			return sub.execute(args[1:])
		}
		// "help" is always a valid subcommand
		if args[0] == "help" {
			if len(args) > 1 {
				if sub := c.Find(args[1]); sub != nil {
					sub.printHelp(os.Stdout)
					return nil
				}
			}
			c.printHelp(os.Stdout)
			return nil
		}
	}

	// Check for --help / -h before parsing
	for _, a := range args {
		if a == "--help" || a == "-h" {
			c.printHelp(os.Stdout)
			return nil
		}
	}

	// Parse flags
	positional, err := c.parseFlags(args)
	if err != nil {
		return fmt.Errorf("%s: %w", c.Name(), err)
	}

	// Validate positional args
	if c.Args != nil {
		if err := c.Args(c, positional); err != nil {
			return err
		}
	}

	// Build pre/post run chain
	if err := c.runPreHooks(positional); err != nil {
		return err
	}

	// Run
	var runErr error
	if c.RunE != nil {
		runErr = c.RunE(c, positional)
	} else if c.Run != nil {
		c.Run(c, positional)
	} else if len(positional) == 0 {
		// No runner and no args — show help
		c.printHelp(os.Stdout)
	}

	c.runPostHooks(positional)
	return runErr
}

// runPreHooks walks from root to c and runs PersistentPreRun(E) hooks, then PreRun.
func (c *Command) runPreHooks(args []string) error {
	// Collect ancestor chain (root first)
	chain := []*Command{}
	for cur := c; cur != nil; cur = cur.parent {
		chain = append([]*Command{cur}, chain...)
	}
	for _, cmd := range chain {
		if cmd == c {
			continue
		}
		if cmd.PersistentPreRunE != nil {
			if err := cmd.PersistentPreRunE(c, args); err != nil {
				return err
			}
		} else if cmd.PersistentPreRun != nil {
			cmd.PersistentPreRun(c, args)
		}
	}
	// Run the command's own PersistentPreRun(E) then PreRun
	if c.PersistentPreRunE != nil {
		if err := c.PersistentPreRunE(c, args); err != nil {
			return err
		}
	} else if c.PersistentPreRun != nil {
		c.PersistentPreRun(c, args)
	}
	if c.PreRun != nil {
		c.PreRun(c, args)
	}
	return nil
}

// runPostHooks runs PostRun and PersistentPostRun from c up to root.
func (c *Command) runPostHooks(args []string) {
	if c.PostRun != nil {
		c.PostRun(c, args)
	}
	for cur := c; cur != nil; cur = cur.parent {
		if cur.PersistentPostRun != nil {
			cur.PersistentPostRun(c, args)
			break
		}
	}
}

// parseFlags parses the args slice using this command's flags and persistent
// flags inherited from all ancestors. Returns positional args.
func (c *Command) parseFlags(args []string) ([]string, error) {
	// Collect all flag sets: persistent flags from root down, then local flags
	sets := []*FlagSet{}
	for cur := c; cur != nil; cur = cur.parent {
		if cur.pflags != nil {
			sets = append([]*FlagSet{cur.pflags}, sets...)
		}
	}
	if c.flags != nil {
		sets = append(sets, c.flags)
	}

	// Merge into one lookup map
	byName := map[string]*flagEntry{}
	byShort := map[string]*flagEntry{}
	for _, fs := range sets {
		for name, f := range fs.byName {
			byName[name] = f
		}
		for short, f := range fs.byShort {
			byShort[short] = f
		}
	}

	var positional []string
	i := 0
	for i < len(args) {
		arg := args[i]
		if arg == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if strings.HasPrefix(arg, "--") {
			name := arg[2:]
			var value string
			hasValue := false
			if idx := strings.IndexByte(name, '='); idx >= 0 {
				value = name[idx+1:]
				name = name[:idx]
				hasValue = true
			}
			f, ok := byName[name]
			if !ok {
				return nil, fmt.Errorf("unknown flag: --%s", name)
			}
			if !hasValue {
				if f.isBool() {
					value = "true"
				} else {
					i++
					if i >= len(args) {
						return nil, fmt.Errorf("flag --%s requires an argument", name)
					}
					value = args[i]
				}
			}
			if err := f.val.Set(value); err != nil {
				return nil, fmt.Errorf("invalid value %q for flag --%s: %w", value, name, err)
			}
			f.changed = true
		} else if strings.HasPrefix(arg, "-") && len(arg) > 1 {
			// Short flag(s)
			shorts := arg[1:]
			for j := 0; j < len(shorts); j++ {
				ch := string(shorts[j])
				f, ok := byShort[ch]
				if !ok {
					return nil, fmt.Errorf("unknown shorthand flag: -%s", ch)
				}
				if f.isBool() {
					_ = f.val.Set("true")
					f.changed = true
				} else {
					// Remaining chars are the value, or next arg
					rest := shorts[j+1:]
					var value string
					if rest != "" {
						value = rest
					} else {
						i++
						if i >= len(args) {
							return nil, fmt.Errorf("flag -%s requires an argument", ch)
						}
						value = args[i]
					}
					if err := f.val.Set(value); err != nil {
						return nil, fmt.Errorf("invalid value %q for flag -%s: %w", value, ch, err)
					}
					f.changed = true
					break
				}
			}
		} else {
			positional = append(positional, arg)
		}
		i++
	}
	return positional, nil
}

// printHelp writes a basic help message to w.
func (c *Command) printHelp(w io.Writer) {
	if c.helpFunc != nil {
		c.helpFunc(c, nil)
		return
	}
	desc := c.Long
	if desc == "" {
		desc = c.Short
	}
	if desc != "" {
		fmt.Fprintln(w, desc)
		fmt.Fprintln(w)
	}
	fmt.Fprintf(w, "Usage:\n  %s\n", c.Use)
	if len(c.children) > 0 {
		fmt.Fprintln(w, "\nAvailable Commands:")
		// Group by GroupID
		grouped := map[string][]*Command{}
		var noGroup []*Command
		for _, sub := range c.children {
			if sub.Deprecated != "" {
				continue
			}
			if sub.GroupID != "" {
				grouped[sub.GroupID] = append(grouped[sub.GroupID], sub)
			} else {
				noGroup = append(noGroup, sub)
			}
		}
		for _, g := range c.groups {
			subs := grouped[g.ID]
			if len(subs) == 0 {
				continue
			}
			fmt.Fprintf(w, "\n%s\n", g.Title)
			for _, sub := range subs {
				fmt.Fprintf(w, "  %-20s %s\n", sub.Name(), sub.Short)
			}
		}
		if len(noGroup) > 0 {
			for _, sub := range noGroup {
				fmt.Fprintf(w, "  %-20s %s\n", sub.Name(), sub.Short)
			}
		}
	}
	// Print flags
	allFlags := []*flagEntry{}
	if c.pflags != nil {
		for _, f := range c.pflags.byName {
			if !f.hidden {
				allFlags = append(allFlags, f)
			}
		}
	}
	if c.flags != nil {
		for _, f := range c.flags.byName {
			if !f.hidden {
				allFlags = append(allFlags, f)
			}
		}
	}
	if len(allFlags) > 0 {
		sort.Slice(allFlags, func(i, j int) bool { return allFlags[i].name < allFlags[j].name })
		fmt.Fprintln(w, "\nFlags:")
		for _, f := range allFlags {
			short := ""
			if f.short != "" {
				short = "-" + f.short + ", "
			}
			fmt.Fprintf(w, "  %s--%-20s %s\n", short, f.name, f.usage)
		}
	}
}

// ---- FlagSet ----------------------------------------------------------------

type flagEntry struct {
	name    string
	short   string
	usage   string
	val     flagValue
	hidden  bool
	changed bool
}

func (f *flagEntry) isBool() bool {
	_, ok := f.val.(*boolVal)
	return ok
}

type flagValue interface {
	String() string
	Set(string) error
	Type() string
}

// FlagSet holds a set of flag definitions for a command.
type FlagSet struct {
	byName  map[string]*flagEntry
	byShort map[string]*flagEntry
}

func newFlagSet() *FlagSet {
	return &FlagSet{
		byName:  make(map[string]*flagEntry),
		byShort: make(map[string]*flagEntry),
	}
}

func (fs *FlagSet) add(name, short, usage string, val flagValue) {
	f := &flagEntry{name: name, short: short, usage: usage, val: val}
	fs.byName[name] = f
	if short != "" {
		fs.byShort[short] = f
	}
}

// HasFlags reports whether the FlagSet has any flags defined.
func (fs *FlagSet) HasFlags() bool { return len(fs.byName) > 0 }

// FlagUsages returns a string of flag usages for display in help.
func (fs *FlagSet) FlagUsages() string {
	var sb strings.Builder
	entries := make([]*flagEntry, 0, len(fs.byName))
	for _, f := range fs.byName {
		if !f.hidden {
			entries = append(entries, f)
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
	for _, f := range entries {
		short := ""
		if f.short != "" {
			short = "-" + f.short + ", "
		}
		fmt.Fprintf(&sb, "  %s--%-20s %s\n", short, f.name, f.usage)
	}
	return sb.String()
}

// Set programmatically sets a flag's value by name (used in tests and internal calls).
func (fs *FlagSet) Set(name, value string) error {
	f, ok := fs.byName[name]
	if !ok {
		return fmt.Errorf("flag %q not found", name)
	}
	if err := f.val.Set(value); err != nil {
		return err
	}
	f.changed = true
	return nil
}

// Changed reports whether a flag was explicitly set.
func (fs *FlagSet) Changed(name string) bool {
	if f, ok := fs.byName[name]; ok {
		return f.changed
	}
	return false
}

// Lookup returns the Flag for the given name, or nil.
func (fs *FlagSet) Lookup(name string) *Flag {
	if f, ok := fs.byName[name]; ok {
		return &Flag{Name: name, entry: f}
	}
	return nil
}

// MarkHidden hides a flag from help output.
func (fs *FlagSet) MarkHidden(name string) error {
	if f, ok := fs.byName[name]; ok {
		f.hidden = true
	}
	return nil
}

// Flag is a minimal wrapper returned by Lookup.
type Flag struct {
	Name    string
	Changed bool
	entry   *flagEntry
}

// --- bool ---

type boolVal struct{ v *bool }

func (b *boolVal) String() string { return strconv.FormatBool(*b.v) }
func (b *boolVal) Set(s string) error {
	v, err := strconv.ParseBool(s)
	if err != nil {
		return err
	}
	*b.v = v
	return nil
}
func (b *boolVal) Type() string { return "bool" }

func (fs *FlagSet) BoolVar(p *bool, name string, value bool, usage string) {
	*p = value
	fs.add(name, "", usage, &boolVal{p})
}
func (fs *FlagSet) BoolVarP(p *bool, name, short string, value bool, usage string) {
	*p = value
	fs.add(name, short, usage, &boolVal{p})
}
func (fs *FlagSet) Bool(name string, value bool, usage string) *bool {
	p := new(bool)
	fs.BoolVar(p, name, value, usage)
	return p
}
func (fs *FlagSet) BoolP(name, short string, value bool, usage string) *bool {
	p := new(bool)
	fs.BoolVarP(p, name, short, value, usage)
	return p
}
func (fs *FlagSet) GetBool(name string) (bool, error) {
	f, ok := fs.byName[name]
	if !ok {
		return false, fmt.Errorf("flag %q not found", name)
	}
	v, err := strconv.ParseBool(f.val.String())
	return v, err
}

// --- string ---

type stringVal struct{ v *string }

func (s *stringVal) String() string  { return *s.v }
func (s *stringVal) Set(v string) error { *s.v = v; return nil }
func (s *stringVal) Type() string    { return "string" }

func (fs *FlagSet) StringVar(p *string, name, value, usage string) {
	*p = value
	fs.add(name, "", usage, &stringVal{p})
}
func (fs *FlagSet) StringVarP(p *string, name, short, value, usage string) {
	*p = value
	fs.add(name, short, usage, &stringVal{p})
}
func (fs *FlagSet) String(name, value, usage string) *string {
	p := new(string)
	fs.StringVar(p, name, value, usage)
	return p
}
func (fs *FlagSet) StringP(name, short, value, usage string) *string {
	p := new(string)
	fs.StringVarP(p, name, short, value, usage)
	return p
}
func (fs *FlagSet) GetString(name string) (string, error) {
	f, ok := fs.byName[name]
	if !ok {
		return "", fmt.Errorf("flag %q not found", name)
	}
	return f.val.String(), nil
}

// --- int ---

type intVal struct{ v *int }

func (iv *intVal) String() string     { return strconv.Itoa(*iv.v) }
func (iv *intVal) Set(s string) error {
	v, err := strconv.Atoi(s)
	if err != nil {
		return err
	}
	*iv.v = v
	return nil
}
func (iv *intVal) Type() string { return "int" }

func (fs *FlagSet) IntVar(p *int, name string, value int, usage string) {
	*p = value
	fs.add(name, "", usage, &intVal{p})
}
func (fs *FlagSet) IntVarP(p *int, name, short string, value int, usage string) {
	*p = value
	fs.add(name, short, usage, &intVal{p})
}
func (fs *FlagSet) Int(name string, value int, usage string) *int {
	p := new(int)
	fs.IntVar(p, name, value, usage)
	return p
}
func (fs *FlagSet) IntP(name, short string, value int, usage string) *int {
	p := new(int)
	fs.IntVarP(p, name, short, value, usage)
	return p
}
func (fs *FlagSet) GetInt(name string) (int, error) {
	f, ok := fs.byName[name]
	if !ok {
		return 0, fmt.Errorf("flag %q not found", name)
	}
	v, err := strconv.Atoi(f.val.String())
	return v, err
}

// --- float64 ---

type float64Val struct{ v *float64 }

func (fv *float64Val) String() string { return strconv.FormatFloat(*fv.v, 'f', -1, 64) }
func (fv *float64Val) Set(s string) error {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return err
	}
	*fv.v = v
	return nil
}
func (fv *float64Val) Type() string { return "float64" }

func (fs *FlagSet) Float64Var(p *float64, name string, value float64, usage string) {
	*p = value
	fs.add(name, "", usage, &float64Val{p})
}
func (fs *FlagSet) Float64(name string, value float64, usage string) *float64 {
	p := new(float64)
	fs.Float64Var(p, name, value, usage)
	return p
}
func (fs *FlagSet) GetFloat64(name string) (float64, error) {
	f, ok := fs.byName[name]
	if !ok {
		return 0, fmt.Errorf("flag %q not found", name)
	}
	v, err := strconv.ParseFloat(f.val.String(), 64)
	return v, err
}

// --- duration ---

type durationVal struct{ v *time.Duration }

func (dv *durationVal) String() string { return dv.v.String() }
func (dv *durationVal) Set(s string) error {
	v, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*dv.v = v
	return nil
}
func (dv *durationVal) Type() string { return "duration" }

func (fs *FlagSet) DurationVar(p *time.Duration, name string, value time.Duration, usage string) {
	*p = value
	fs.add(name, "", usage, &durationVal{p})
}
func (fs *FlagSet) Duration(name string, value time.Duration, usage string) *time.Duration {
	p := new(time.Duration)
	fs.DurationVar(p, name, value, usage)
	return p
}
func (fs *FlagSet) DurationVarP(p *time.Duration, name, short string, value time.Duration, usage string) {
	*p = value
	fs.add(name, short, usage, &durationVal{p})
}
func (fs *FlagSet) DurationP(name, short string, value time.Duration, usage string) *time.Duration {
	p := new(time.Duration)
	fs.DurationVarP(p, name, short, value, usage)
	return p
}
func (fs *FlagSet) GetDuration(name string) (time.Duration, error) {
	f, ok := fs.byName[name]
	if !ok {
		return 0, fmt.Errorf("flag %q not found", name)
	}
	v, err := time.ParseDuration(f.val.String())
	return v, err
}

// --- string slice ---

type stringSliceVal struct{ v *[]string }

func (sv *stringSliceVal) String() string { return strings.Join(*sv.v, ",") }
func (sv *stringSliceVal) Set(s string) error {
	// Split on comma; accumulate across multiple --flag uses
	parts := strings.Split(s, ",")
	*sv.v = append(*sv.v, parts...)
	return nil
}
func (sv *stringSliceVal) Type() string { return "stringSlice" }

func (fs *FlagSet) StringSliceVar(p *[]string, name string, value []string, usage string) {
	*p = value
	fs.add(name, "", usage, &stringSliceVal{p})
}
func (fs *FlagSet) StringSliceVarP(p *[]string, name, short string, value []string, usage string) {
	*p = value
	fs.add(name, short, usage, &stringSliceVal{p})
}
func (fs *FlagSet) StringSlice(name string, value []string, usage string) *[]string {
	p := new([]string)
	fs.StringSliceVar(p, name, value, usage)
	return p
}
func (fs *FlagSet) StringSliceP(name, short string, value []string, usage string) *[]string {
	p := new([]string)
	fs.StringSliceVarP(p, name, short, value, usage)
	return p
}
func (fs *FlagSet) GetStringSlice(name string) ([]string, error) {
	f, ok := fs.byName[name]
	if !ok {
		return nil, fmt.Errorf("flag %q not found", name)
	}
	raw := f.val.String()
	if raw == "" {
		return []string{}, nil
	}
	return strings.Split(raw, ","), nil
}

// --- string array (accumulate without splitting on comma) ---

type stringArrayVal struct{ v *[]string }

func (av *stringArrayVal) String() string { return strings.Join(*av.v, ",") }
func (av *stringArrayVal) Set(s string) error {
	*av.v = append(*av.v, s)
	return nil
}
func (av *stringArrayVal) Type() string { return "stringArray" }

func (fs *FlagSet) StringArrayVar(p *[]string, name string, value []string, usage string) {
	*p = value
	fs.add(name, "", usage, &stringArrayVal{p})
}
func (fs *FlagSet) StringArray(name string, value []string, usage string) *[]string {
	p := new([]string)
	fs.StringArrayVar(p, name, value, usage)
	return p
}
func (fs *FlagSet) GetStringArray(name string) ([]string, error) {
	f, ok := fs.byName[name]
	if !ok {
		return nil, fmt.Errorf("flag %q not found", name)
	}
	raw := f.val.String()
	if raw == "" {
		return []string{}, nil
	}
	return strings.Split(raw, ","), nil
}
