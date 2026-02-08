package main

import (
	"cmp"
	"fmt"
	"slices"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/ui"
)

// lipgloss styles for the thanks page using Ayu theme
var (
	thanksTitleStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(ui.ColorWarn)

	thanksSubtitleStyle = lipgloss.NewStyle().
				Foreground(ui.ColorMuted)

	thanksSectionStyle = lipgloss.NewStyle().
				Foreground(ui.ColorAccent).
				Bold(true)

	thanksNameStyle = lipgloss.NewStyle().
			Foreground(ui.ColorPass)

	thanksLabelStyle = lipgloss.NewStyle().
				Foreground(ui.ColorWarn)

	thanksDimStyle = lipgloss.NewStyle().
			Foreground(ui.ColorMuted)
)

// thanksBoxStyle returns a box style with dynamic width
func thanksBoxStyle(width int) lipgloss.Style {
	return lipgloss.NewStyle().
		BorderStyle(lipgloss.DoubleBorder()).
		BorderForeground(ui.ColorMuted).
		Padding(1, 4).
		Width(width - 4).
		Align(lipgloss.Center)
}

// Static list of human contributors to the beads project.
// To update: run `git shortlog -sn --all` in the beads repo.
// Map of contributor name -> commit count, sorted by contribution count descending.
var beadsContributors = map[string]int{
	"Steve Yegge":            2959,
	"matt wilkie":            64,
	"Ryan Snodgrass":         43,
	"Travis Cline":           9,
	"David Laing":            7,
	"Ryan Newton":            6,
	"Joshua Shanks":          6,
	"Daan van Etten":         5,
	"Augustinas Malinauskas": 4,
	"Matteo Landi":           4,
	"Baishampayan Ghose":     4,
	"Charles P. Cross":       4,
	"Abhinav Gupta":          3,
	"Brian Williams":         3,
	"Marco Del Pin":          3,
	"Willi Ballenthin":       3,
	"Ben Lovell":             2,
	"Ben Madore":             2,
	"Dane Bertram":           2,
	"Dennis Schön":           2,
	"Troy Gaines":            2,
	"Zoe Gagnon":             2,
	"Peter Schilling":        2,
	"Adam Spiers":            1,
	"Aodhan Hayter":          1,
	"Assim Elhammouti":       1,
	"Bryce Roche":            1,
	"Caleb Leak":             1,
	"David Birks":            1,
	"Dean Giberson":          1,
	"Eli":                    1,
	"Graeme Foster":          1,
	"Gurdas Nijor":           1,
	"Jimmy Stridh":           1,
	"Joel Klabo":             1,
	"Johannes Zillmann":      1,
	"John Lam":               1,
	"Jonathan Berger":        1,
	"Joshua Park":            1,
	"Juan Vargas":            1,
	"Kasper Zutterman":       1,
	"Kris Hansen":            1,
	"Logan Thomas":           1,
	"Lon Lundgren":           1,
	"Mark Wotton":            1,
	"Markus Flür":            1,
	"Michael Shuffett":       1,
	"Midworld Kim":           1,
	"Nikolai Prokoschenko":   1,
	"Peter Loron":            1,
	"Rod Davenport":          1,
	"Serhii":                 1,
	"Shaun Cutts":            1,
	"Sophie Smithburg":       1,
	"Tim Haasdyk":            1,
	"Travis Lyons":           1,
	"Yaakov Nemoy":           1,
	"Yunsik Kim":             1,
	"Zachary Rosen":          1,
}

var thanksCmd = &cobra.Command{
	Use:    "thanks",
	Short:  "Thank the human contributors to beads",
	Hidden: true, // Deprecated: use 'bd info --thanks' instead
	Long: `Display a thank you page listing all human contributors to the beads project.

DEPRECATED: Use 'bd info --thanks' instead.

Examples:
  bd info --thanks       # Show thank you page (preferred)
  bd thanks              # Legacy command (still works)
`,
	Run: func(cmd *cobra.Command, args []string) {
		printThanksPage()
	},
}

// getContributorsSorted returns contributors sorted by commit count descending
func getContributorsSorted() []string {
	type kv struct {
		name    string
		commits int
	}
	var sorted []kv
	for name, commits := range beadsContributors {
		sorted = append(sorted, kv{name, commits})
	}
	slices.SortFunc(sorted, func(a, b kv) int {
		return cmp.Compare(b.commits, a.commits) // descending order
	})
	names := make([]string, len(sorted))
	for i, kv := range sorted {
		names[i] = kv.name
	}
	return names
}

// printThanksPage displays the thank you page
func printThanksPage() {
	fmt.Println()

	// get sorted contributors and split into top 20 and rest
	allContributors := getContributorsSorted()
	topN := 20
	if topN > len(allContributors) {
		topN = len(allContributors)
	}

	topContributors := allContributors[:topN]
	additionalContributors := allContributors[topN:]

	// calculate content width based on featured contributors columns
	contentWidth := calculateColumnsWidth(topContributors, 4) + 4 // +4 for indent

	// build header content with styled text
	title := thanksTitleStyle.Render("THANK YOU!")
	subtitle := thanksSubtitleStyle.Render("To all the humans who contributed to beads")
	header := title + "\n\n" + subtitle

	// render header in a bordered box matching content width
	fmt.Println(thanksBoxStyle(contentWidth).Render(header))
	fmt.Println()

	// print featured contributors section
	fmt.Println(thanksSectionStyle.Render("  Featured Contributors"))
	fmt.Println()
	printThanksColumns(topContributors, 4)

	// print additional contributors with line wrapping
	if len(additionalContributors) > 0 {
		fmt.Println()
		fmt.Println(thanksSectionStyle.Render("  Additional Contributors"))
		fmt.Println()
		printThanksWrappedList("", additionalContributors, contentWidth)
	}
	fmt.Println()
}

// calculateColumnsWidth returns the total width needed for displaying names in columns
func calculateColumnsWidth(names []string, cols int) int {
	if len(names) == 0 {
		return 0
	}

	maxWidth := 0
	for _, name := range names {
		if len(name) > maxWidth {
			maxWidth = len(name)
		}
	}
	if maxWidth > 20 {
		maxWidth = 20
	}
	colWidth := maxWidth + 2

	return colWidth * cols
}

// printThanksColumns prints names in n columns, sorted horizontally by input order
func printThanksColumns(names []string, cols int) {
	if len(names) == 0 {
		return
	}

	// find max width for alignment
	maxWidth := 0
	for _, name := range names {
		if len(name) > maxWidth {
			maxWidth = len(name)
		}
	}
	if maxWidth > 20 {
		maxWidth = 20
	}
	colWidth := maxWidth + 2

	// print in rows, reading left to right
	for i := 0; i < len(names); i += cols {
		fmt.Print("  ")
		for j := 0; j < cols && i+j < len(names); j++ {
			name := names[i+j]
			if len(name) > 20 {
				name = name[:17] + "..."
			}
			padded := fmt.Sprintf("%-*s", colWidth, name)
			fmt.Print(thanksNameStyle.Render(padded))
		}
		fmt.Println()
	}
}

// printThanksWrappedList prints a list with word wrapping at name boundaries
func printThanksWrappedList(label string, names []string, maxWidth int) {
	indent := "  "

	fmt.Print(indent)
	lineLen := len(indent)

	if label != "" {
		fmt.Print(thanksLabelStyle.Render(label) + " ")
		lineLen += len(label) + 1
	}

	for i, name := range names {
		suffix := ", "
		if i == len(names)-1 {
			suffix = ""
		}
		entry := name + suffix

		if lineLen+len(entry) > maxWidth && lineLen > len(indent) {
			fmt.Println()
			fmt.Print(indent)
			lineLen = len(indent)
		}

		fmt.Print(thanksDimStyle.Render(entry))
		lineLen += len(entry)
	}
	fmt.Println()
}

func init() {
	rootCmd.AddCommand(thanksCmd)
}
