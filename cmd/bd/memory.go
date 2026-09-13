package main

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/memoryapi"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage/kvkeys"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/memoryops"
)

// openMemories hands back the persistent-memory role for whichever route this
// invocation is on, each through its OWN capability accessor — the store's for
// the direct route and the provider's for the proxied one.
//
// directRequirement is the message `ensureDirectMode` reports when a workspace
// is reachable by neither route. It is per-verb because the shipped text names
// the verb.
func openMemories(directRequirement string) (memoryops.Memories, error) {
	if usesProxiedServer() {
		return proxiedMemories()
	}
	if err := ensureDirectMode(directRequirement); err != nil {
		return nil, err
	}
	return store.Memories()
}

// proxiedMemories hands back the guarded persistent-memory surface for this
// invocation's proxied-server provider, through the provider's OWN capability
// accessor — the same two-step proxiedWorkspaceConfig performs.
func proxiedMemories() (memoryops.Memories, error) {
	if uowProvider == nil {
		return nil, errors.New("proxied-server UOW provider not initialized")
	}
	return memoriesFromProvider(uowProvider)
}

// memoriesFromProvider is that accessor step for a provider the caller names.
//
// It takes the provider rather than reading the global one because `bd prime`
// opens a provider SCOPED to its read — prime is in noDbCommands, so the root
// pre-run opens nothing — and one spelling of "ask this provider for the memory
// surface" is the whole point of having an accessor at all.
func memoriesFromProvider(provider uow.UnitOfWorkProvider) (memoryops.Memories, error) {
	src, ok := provider.(uow.MemoriesSource)
	if !ok {
		return nil, fmt.Errorf("proxied-server provider %T does not offer the persistent-memory surface", provider)
	}
	return src.Memories()
}

// noteDirectMemoryWrite marks the invocation as having written, which is what
// the auto-commit epilogue in main.go keys on.
//
// It is DIRECT-ROUTE ONLY, and both halves of that matter. A direct memory
// write lands in the Dolt working set and nothing else commits it, so a verb
// that forgets to call this stores a memory that exists until the process exits
// and then sits uncommitted — visible to the session that wrote it and to
// nothing after. A proxied write already committed inside the role's unit of
// work, so flagging it there would ask the epilogue to commit a second time on
// a route with nothing outstanding.
//
// The RunEs cannot make that distinction themselves: openMemories hides which
// route they are on, which is the point. So the guard lives here, once, the way
// noteDirectConfigWrite does for the settings plane.
func noteDirectMemoryWrite() {
	if !usesProxiedServer() {
		commandDidWrite.Store(true)
	}
}

// memoryPrefix is prepended (after kvPrefix) to all memory keys.
const memoryPrefix = kvkeys.MemoryPrefix

// memoryKeyFlag allows explicit key override for bd remember.
var memoryKeyFlag string

// matchesKnownCommand reports whether insight is a single bare word that
// matches the name or an alias of a top-level bd command. It is used to catch
// `bd remember <subcommand>` mistakes before they become accidental memories.
// Multi-word insights (the normal case) always pass, since they contain
// whitespace and so cannot be a single command token.
func matchesKnownCommand(cmd *cobra.Command, insight string) (string, bool) {
	word := strings.TrimSpace(insight)
	if word == "" || strings.ContainsAny(word, " \t\r\n") {
		return "", false
	}
	for _, c := range cmd.Root().Commands() {
		if strings.EqualFold(c.Name(), word) {
			return c.Name(), true
		}
		for _, alias := range c.Aliases {
			if strings.EqualFold(alias, word) {
				return c.Name(), true
			}
		}
	}
	return "", false
}

// memoryForceFlag is `bd remember --force`: write even when the corpus budget
// would be crossed. It only has meaning when memories.budget-chars is set.
var memoryForceFlag bool

// memoryConfigInt reads an integer config key (stubbable for tests), the way
// primeConfigInt does for the prime-side injection caps.
var memoryConfigInt = func(key string) int {
	return config.GetInt(key)
}

// memoryCorpusBudget resolves the memories.budget-chars ceiling. 0 — the
// default — means OFF, and so does a negative value: the budget is opt-in, and
// a nonsense setting must not turn into a refusal the operator never asked for.
func memoryCorpusBudget() int {
	budget := memoryConfigInt("memories.budget-chars")
	if budget < 0 {
		return 0
	}
	return budget
}

// memoryCorpusChars sums len(key)+len(content) over a corpus.
//
// The unit is BYTES (Go len), deliberately the same unit prime's
// --max-memory-chars cap counts in: the budget exists to bound what prime
// injects, so measuring it in a second unit would let a corpus sit inside the
// budget and still blow the injection cap.
func memoryCorpusChars(memories map[string]string) int {
	total := 0
	for k, v := range memories {
		total += len(k) + len(v)
	}
	return total
}

// projectedMemoryCorpusChars is that sum AFTER a write of content under key
// would apply. An OVERWRITE replaces the key's old content in the sum instead
// of adding to it — the delta, not the sum — which is what makes editing an
// existing memory possible at all once a corpus is near its ceiling.
func projectedMemoryCorpusChars(existing map[string]string, key, content string) int {
	total := memoryCorpusChars(existing)
	if old, ok := existing[key]; ok {
		total -= len(key) + len(old)
	}
	return total + len(key) + len(content)
}

// memoryBudgetLine renders the ONE stderr line the budget check emits, and
// says whether the write must be refused.
//
// Three outcomes, in order of severity:
//   - over budget, no --force: refuse, naming the override.
//   - over budget with --force: write, but SAY SO — a forced crossing that
//     printed nothing would make the budget invisible exactly when it matters.
//   - at or above 80% of budget and within it: write, warn. That is the whole
//     point of a budget over a tripwire: the warning arrives while there is
//     still room to act on it.
//
// Below 80%, and whenever the budget is off, it returns "" and the command is
// silent — byte-identical to the pre-budget behavior.
//
// The percentage FLOORS within the budget and CEILINGS over it, and the
// rounding is not cosmetic: floor(201*100/200) is 100, and "100%" is exactly
// the at-budget reading this code deliberately ALLOWS — so a floored refusal
// would report the number of the case it is not. Ceiling above the line makes
// every over-budget line read 101% or more, and the split is on the VALUE, not
// on --force, so the same projection never reports two different percentages
// depending on a flag. Both forms are int64 arithmetic, so a large budget
// cannot overflow the comparison.
func memoryBudgetLine(projected, budget int, force bool) (line string, refuse bool) {
	if budget <= 0 {
		return "", false
	}
	pct := int(int64(projected) * 100 / int64(budget))
	if projected > budget {
		pct = int((int64(projected)*100 + int64(budget) - 1) / int64(budget))
	}
	switch {
	case projected > budget && !force:
		return fmt.Sprintf("bd remember: memory corpus would be %d chars, budget is %d (%d%%) — refused; use --force to override", projected, budget, pct), true
	case projected > budget:
		return fmt.Sprintf("bd remember: memory corpus at %d of %d chars (%d%%) — over budget, written anyway (--force)", projected, budget, pct), false
	case int64(projected)*5 >= int64(budget)*4:
		return fmt.Sprintf("bd remember: memory corpus at %d of %d chars (%d%%)", projected, budget, pct), false
	}
	return "", false
}

// rememberBudgetVerdict is the whole budget decision as ONE pure function:
// given the corpus as it stands, the write about to be made, the budget and
// --force, it answers with the line to print (empty = say nothing) and whether
// the write must be refused. The command around it only has to fetch the
// corpus and obey.
//
// Keeping it pure is what lets every branch — off, boundary, overwrite delta,
// force, warn — be pinned by a test that needs neither a database nor cgo.
func rememberBudgetVerdict(existing map[string]string, key, content string, budget int, force bool) (line string, refuse bool) {
	if budget <= 0 {
		return "", false
	}
	return memoryBudgetLine(projectedMemoryCorpusChars(existing, key, content), budget, force)
}

// rememberBareKeyPath implements the desire-path / footgun guard for
// `bd remember <bare-slug>` (no --key): a bare slug naming an EXISTING memory
// is recalled instead of stored; a bare slug naming nothing is refused. The
// caller only invokes it when memoryKeyFlag == "" and the insight round-trips
// through memoryapi.DeriveKey unchanged, having already read the key.
func rememberBareKeyPath(key, insight, existing string) error {
	if existing != "" {
		if jsonOutput {
			return outputJSON(map[string]interface{}{
				"key":    key,
				"value":  existing,
				"found":  true,
				"action": "recalled",
			})
		}
		fmt.Fprintf(os.Stderr,
			"(recalled %q -- a bare existing key READS. To overwrite: `bd remember \"<new content>\" --key %s`)\n",
			key, key)
		fmt.Printf("%s\n", existing)
		return nil
	}
	return HandleErrorRespectJSON(
		"no memory named %q to recall -- and refusing to store a bare key-like token as its own content. "+
			"`bd remember` WRITES (its positional arg is CONTENT, not a key). "+
			"To store it anyway: `bd remember %q --key %s`. To browse keys: `bd memories`",
		key, insight, key)
}

// printRememberResult renders the `bd remember` success output.
func printRememberResult(verb, key, insight string) error {
	if jsonOutput {
		return outputJSON(map[string]string{
			"key":    key,
			"value":  insight,
			"action": strings.ToLower(verb),
		})
	}
	fmt.Printf("%s [%s]: %s\n", verb, key, truncateMemory(insight, 80))
	return nil
}

// printMemoriesResult renders the `bd memories` output.
func printMemoriesResult(memories map[string]string, search string) error {
	if jsonOutput {
		return outputJSON(memories)
	}

	if len(memories) == 0 {
		if search != "" {
			fmt.Printf("No memories matching %q\n", search)
		} else {
			fmt.Println("No memories stored. Use 'bd remember \"insight\"' to add one.")
		}
		return nil
	}

	keys := make([]string, 0, len(memories))
	for k := range memories {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	if search != "" {
		fmt.Printf("Memories matching %q:\n\n", search)
	} else {
		fmt.Printf("Memories (%d):\n\n", len(memories))
	}
	for _, k := range keys {
		v := memories[k]
		fmt.Printf("  %s\n", k)
		fmt.Printf("    %s\n\n", truncateMemory(v, 120))
	}
	return nil
}

// printForgetNotFound renders the `bd forget` missing-key output (including
// the SilentExit contract).
func printForgetNotFound(key string) error {
	if jsonOutput {
		if jerr := outputJSON(map[string]string{
			"key":   key,
			"found": "false",
		}); jerr != nil {
			return jerr
		}
		return SilentExit()
	}
	fmt.Fprintf(os.Stderr, "No memory with key %q\n", key)
	return SilentExit()
}

// printForgetResult renders the `bd forget` success output.
func printForgetResult(key, existing string) error {
	if jsonOutput {
		return outputJSON(map[string]string{
			"key":     key,
			"deleted": "true",
		})
	}
	fmt.Printf("Forgot [%s]: %s\n", key, truncateMemory(existing, 80))
	return nil
}

// printRecallResult renders the `bd recall` output (including the not-found
// SilentExit contract).
func printRecallResult(key, value string) error {
	if jsonOutput {
		if jerr := outputJSON(map[string]interface{}{
			"key":   key,
			"value": value,
			"found": value != "",
		}); jerr != nil {
			return jerr
		}
		if value == "" {
			return SilentExit()
		}
		return nil
	}
	if value == "" {
		fmt.Fprintf(os.Stderr, "No memory with key %q\n", key)
		return SilentExit()
	}
	fmt.Printf("%s\n", value)
	return nil
}

// rememberCmd stores a memory.
var rememberCmd = &cobra.Command{
	Use:   `remember "<insight>"`,
	Short: "Store a persistent memory",
	Long: `Store a memory that persists across sessions and account rotations.

Memories are injected at prime time (bd prime) so you have them
in every session without manual loading.

The positional arg is the memory CONTENT (the key is auto-generated from it
unless --key is given). As a convenience, if the arg is a bare key naming an
existing memory, it is RECALLED instead of stored (same as 'bd recall');
a bare key naming nothing is refused. Use --key to store slug-like content.

Corpus budget (opt-in, off by default): set the memories.budget-chars config
key to a byte ceiling for the whole memory corpus (sum of len(key)+len(content),
the unit bd prime's --max-memory-chars counts in). A write that would cross it
is refused, and --force overrides; a write that lands at or above 80% of the
budget warns. With the key unset nothing changes.

Examples:
  bd remember "always run tests with -race flag"
  bd remember "Dolt phantom DBs hide in three places" --key dolt-phantoms
  bd remember "auth module uses JWT not sessions" --key auth-jwt
  bd remember dolt-phantoms        # bare existing key: reads it (= bd recall)`,
	GroupID:       "setup",
	Args:          cobra.ExactArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		CheckReadonly("remember") // also covers CheckMigrationFreeze (dc-6jaq)

		evt := metrics.NewCommandEvent("remember")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		insight := args[0]

		// Guard against a subcommand-like first argument being silently stored
		// as memory content. `bd remember` is a leaf command, so a mistaken
		// `bd remember recall` (or any bare bd command name) would otherwise
		// store the word "recall" as a memory instead of doing what the user
		// intended (GH#4401). A genuine insight is a phrase, so only a single
		// bare word that matches a known command is treated as suspect, and an
		// explicit --key signals deliberate intent and bypasses the guard.
		//
		// It stays at the FRONT DOOR and stays FIRST: it reads the cobra command
		// tree, which no role can see, and it must answer before any storage is
		// opened so that `bd remember list` in a directory with no workspace
		// still says "looks like a command".
		if memoryKeyFlag == "" {
			if name, ok := matchesKnownCommand(cmd, insight); ok {
				return HandleErrorWithHintRespectJSON(
					fmt.Sprintf("%q looks like a command, not something to remember", insight),
					fmt.Sprintf("Did you mean 'bd %s'? To store %q as a memory anyway, give it an explicit key: bd remember %q --key <key>", name, insight, insight),
				)
			}
		}

		memories, err := openMemories("remember requires direct database access")
		if err != nil {
			return HandleError("%v", err)
		}

		// Desire path + footgun guard: `bd remember <x>` is a WRITE whose positional arg is
		// the CONTENT, not a key -- but "remember X" reads as a getter in English, so agents
		// routinely type `bd remember some-key` meaning "do you remember X?". The tell-tale of
		// a mistyped read is content that round-trips through the key derivation unchanged (a
		// bare slug); real prose insights never do. When that happens and no explicit --key was
		// given:
		//   - the key EXISTS  -> pave the desire path: recall it instead of writing
		//   - no such key     -> refuse; storing a key-like token as its own content would
		//                        create a junk memory that hides the mistake
		// Passing --key states write intent and bypasses both branches.
		//
		// It stays ABOVE the role because it decides WHETHER TO WRITE AT ALL, and
		// because it exists to disambiguate English: an HTTP POST is not ambiguous
		// and must not inherit it. The read below is a plain Recall, so this whole
		// branch touches nothing.
		//
		// `derived != ""` is load-bearing and is not decoration: DeriveKey("")
		// is "", so without it every empty or unslugifiable insight would satisfy
		// derived == insight and be routed into a "recall" of the empty key. The
		// shipped code was saved from that by an empty-content check that ran
		// first; that check is the role's now, so the condition has to say it.
		derived := memoryapi.DeriveKey(insight)
		if memoryKeyFlag == "" && derived != "" && derived == insight {
			recalled, err := memories.Recall(rootCtx, memoryops.RecallRequest{Key: derived})
			if err != nil {
				return HandleErrorRespectJSON("recalling memory: %v", err)
			}
			return rememberBareKeyPath(derived, insight, recalled.Value)
		}

		// Corpus budget (memories.budget-chars, OPT-IN, 0 = off). The
		// wheelhouse-side tripwire only REPORTS the corpus size, so the corpus
		// re-trips red every few days and nothing ever stops the write that
		// crossed the line. This is the stop.
		//
		// It sits here, between the read-shaped branches above and the write
		// below, because it is the last thing that can decide NOT to write —
		// and above the role because a budget is a front-door policy, not a
		// property of the memory plane (an HTTP POST must not inherit it).
		//
		// When the budget is off the whole block is skipped: no extra List, no
		// output, nothing. Behavior is then byte-identical to the pre-budget
		// command, which is what TestRememberBudgetVerdictOffIsSilent and the embedded
		// budget_off_is_silent case pin.
		budgetKey := memoryKeyFlag
		if budgetKey == "" {
			budgetKey = derived
		}
		var budgetLine string
		if budget := memoryCorpusBudget(); budget > 0 && budgetKey != "" && strings.TrimSpace(insight) != "" {
			// ADVISORY, not atomic: this List and the Remember below are
			// separate transactions, so two concurrent writers can both measure
			// a corpus inside the budget and both land, leaving it over. The
			// budget is a guardrail against a corpus drifting past its ceiling
			// one deliberate write at a time, not a hard limit — no locking.
			corpus, err := memories.List(rootCtx, memoryops.ListRequest{})
			if err != nil {
				return HandleErrorRespectJSON("reading the memory corpus for the budget check: %v", err)
			}
			line, refuse := rememberBudgetVerdict(corpus.Memories, budgetKey, insight, budget, memoryForceFlag)
			if refuse {
				// The budget line IS the refusal sentence, so it prints as
				// itself: routing it through HandleError would prefix it with
				// "Error: " and reword text agents will match on.
				//
				// It is said ONCE, on exactly one stream, the way
				// HandleErrorWithHintRespectJSON does it: --json gets only the
				// machine-readable envelope on stdout (printing the prose to
				// stderr as well would make the same refusal arrive twice),
				// and every other route gets the bare sentence on stderr,
				// where it can never corrupt structured output.
				if jsonOutput {
					jsonStdoutError(line, "raise memories.budget-chars, forget a memory, or pass --force")
				} else {
					fmt.Fprintln(os.Stderr, line)
				}
				return SilentExit()
			}
			// Not a refusal: the write happens first and the line reports the
			// corpus that now exists, so no line ever describes a write that
			// did not land.
			budgetLine = line
		}

		result, err := memories.Remember(rootCtx, memoryops.RememberRequest{Key: memoryKeyFlag, Content: insight})
		if err != nil {
			// The role's two refusals ARE this command's shipped sentences —
			// "memory content cannot be empty" and "could not generate key from
			// content; use --key to specify one" — so they print as themselves.
			// Wrapping would reword output an agent may be matching on into
			// "storing memory: validation failed: ..." to say the same thing.
			if errors.Is(err, memoryops.ErrValidation) {
				return HandleErrorRespectJSON("%s", strings.TrimPrefix(err.Error(), memoryops.ErrValidation.Error()+": "))
			}
			return HandleErrorRespectJSON("storing memory: %v", err)
		}
		noteDirectMemoryWrite()

		if budgetLine != "" {
			fmt.Fprintln(os.Stderr, budgetLine)
		}

		// Remembered versus Updated is Replaced, observed in the SAME
		// transaction as the write. The shipped code read the row first and
		// described a moment that had already passed.
		verb := "Remembered"
		if result.Replaced {
			verb = "Updated"
		}
		return printRememberResult(verb, result.Key, result.Value)
	},
}

// memoriesCmd lists and searches memories.
var memoriesCmd = &cobra.Command{
	Use:   "memories [search]",
	Short: "List or search persistent memories",
	Long: `List all memories, or search by keyword.

Examples:
  bd memories              # list all memories
  bd memories dolt         # search for memories about dolt
  bd memories "race flag"  # search for a phrase`,
	GroupID:       "setup",
	Args:          cobra.MaximumNArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		evt := metrics.NewCommandEvent("memories")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		var search string
		if len(args) > 0 {
			search = args[0]
		}

		memories, err := openMemories("memories requires direct database access")
		if err != nil {
			return HandleError("%v", err)
		}
		// The term goes to the role RAW. Case folding is List's, so the two
		// routes cannot come to disagree about what matches — which is the
		// whole reason the filter moved down.
		result, err := memories.List(rootCtx, memoryops.ListRequest{Search: search})
		if err != nil {
			return HandleErrorRespectJSON("listing memories: %v", err)
		}

		// The ECHO, on the other hand, has always been lowercased: `bd memories
		// FOO` prints `No memories matching "foo"`. It is a wart — a front door
		// should say back what the user typed — but it is shipped output, and
		// this commit is a convergence, not a change. Fixing it is a one-liner
		// with its own test, like truncateMemory's rune splitting.
		return printMemoriesResult(result.Memories, strings.ToLower(search))
	},
}

// forgetCmd removes a memory.
var forgetCmd = &cobra.Command{
	Use:   "forget <key>",
	Short: "Remove a persistent memory",
	Long: `Remove a memory by its key.

Use 'bd memories' to see available keys.

Examples:
  bd forget dolt-phantoms
  bd forget auth-jwt`,
	GroupID:       "setup",
	Args:          cobra.ExactArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		CheckReadonly("forget")

		evt := metrics.NewCommandEvent("forget")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		memories, err := openMemories("forget requires direct database access")
		if err != nil {
			return HandleError("%v", err)
		}
		// No pre-read here, deliberately: the value printed below is the one
		// the role's transaction actually deleted, not the one an earlier read
		// happened to see.
		result, err := memories.Forget(rootCtx, memoryops.ForgetRequest{Key: args[0]})
		if err != nil {
			return HandleErrorRespectJSON("forgetting memory: %v", err)
		}
		if !result.Found {
			return printForgetNotFound(result.Key)
		}
		noteDirectMemoryWrite()

		return printForgetResult(result.Key, result.Value)
	},
}

// recallCmd retrieves a specific memory by key.
var recallCmd = &cobra.Command{
	Use:   "recall <key>",
	Short: "Retrieve a specific memory",
	Long: `Retrieve the full content of a memory by its key.

Examples:
  bd recall dolt-phantoms
  bd recall auth-jwt`,
	GroupID:       "setup",
	Args:          cobra.ExactArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		evt := metrics.NewCommandEvent("recall")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		memories, err := openMemories("recall requires direct database access")
		if err != nil {
			return HandleError("%v", err)
		}
		result, err := memories.Recall(rootCtx, memoryops.RecallRequest{Key: args[0]})
		if err != nil {
			return HandleErrorRespectJSON("recalling memory: %v", err)
		}

		return printRecallResult(result.Key, result.Value)
	},
}

// truncateMemory shortens a string to maxLen for display.
func truncateMemory(s string, maxLen int) string {
	// Replace newlines with spaces for single-line display
	s = strings.ReplaceAll(s, "\n", " ")
	return truncate(s, maxLen)
}

func init() {
	rememberCmd.Flags().StringVar(&memoryKeyFlag, "key", "", "Explicit key for the memory (auto-generated from content if not set). If a memory with this key already exists, it will be updated in place")
	rememberCmd.Flags().BoolVar(&memoryForceFlag, "force", false, "Store even when the write would cross the memories.budget-chars corpus budget (no effect when the budget is unset)")

	rootCmd.AddCommand(rememberCmd)
	rootCmd.AddCommand(memoriesCmd)
	rootCmd.AddCommand(forgetCmd)
	rootCmd.AddCommand(recallCmd)
}
