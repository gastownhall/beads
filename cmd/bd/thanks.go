package main

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/steveyegge/beads/internal/ansi"
	"github.com/steveyegge/beads/internal/ui"
)

// styles for the thanks page using Ayu theme
var (
	thanksTitleStyle    = ansi.NewStyle().Bold(true).Foreground(ansi.Color(ui.ColorWarn))
	thanksSubtitleStyle = ansi.NewStyle().Foreground(ansi.Color(ui.ColorMuted))
	thanksSectionStyle  = ansi.NewStyle().Foreground(ansi.Color(ui.ColorAccent)).Bold(true)
	thanksNameStyle     = ansi.NewStyle().Foreground(ansi.Color(ui.ColorPass))
	thanksDimStyle      = ansi.NewStyle().Foreground(ansi.Color(ui.ColorMuted))
)

// Static list of human contributors to the beads project.
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
		return cmp.Compare(b.commits, a.commits)
	})
	names := make([]string, len(sorted))
	for i, kv := range sorted {
		names[i] = kv.name
	}
	return names
}

func printThanksPage() {
	fmt.Println()

	allContributors := getContributorsSorted()
	topN := 20
	if topN > len(allContributors) {
		topN = len(allContributors)
	}

	topContributors := allContributors[:topN]
	additionalContributors := allContributors[topN:]

	contentWidth := calculateColumnsWidth(topContributors, 4) + 4

	// Simple bordered header
	border := strings.Repeat("═", contentWidth-2)
	fmt.Printf("╔%s╗\n", border)
	title := thanksTitleStyle.Render("THANK YOU!")
	subtitle := thanksSubtitleStyle.Render("To all the humans who contributed to beads")
	fmt.Printf("║ %-*s║\n", contentWidth+20, title) // extra width for ANSI codes
	fmt.Printf("║ %-*s║\n", contentWidth+20, subtitle)
	fmt.Printf("╚%s╝\n", border)
	fmt.Println()

	fmt.Println(thanksSectionStyle.Render("  Featured Contributors"))
	fmt.Println()
	printThanksColumns(topContributors, 4)

	if len(additionalContributors) > 0 {
		fmt.Println()
		fmt.Println(thanksSectionStyle.Render("  Additional Contributors"))
		fmt.Println()
		printThanksWrappedList("", additionalContributors, contentWidth)
	}
	fmt.Println()
}

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

func printThanksColumns(names []string, cols int) {
	if len(names) == 0 {
		return
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

func printThanksWrappedList(label string, names []string, maxWidth int) {
	indent := "  "
	fmt.Print(indent)
	lineLen := len(indent)

	if label != "" {
		styled := ansi.NewStyle().Foreground(ansi.Color(ui.ColorWarn)).Render(label)
		fmt.Print(styled + " ")
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
