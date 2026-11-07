#!/bin/sh
# Git merge driver for CHANGELOG.md, named by .gitattributes. Git runs it only
# where it is configured:
#
#   git config merge.changelog.driver 'sh scripts/changelog-merge-driver.sh %O %A %B %L'
#
# Git passes the ancestor, current and other versions of the file (%O %A %B)
# and the conflict marker size (%L), and takes the merge from the current
# version's file: exit 0 when it merged cleanly, 1 when it left conflicts.
#
# Nearly every PR adds its entry at the top of a CHANGELOG.md section, so two
# PRs that each add one conflict there even though nothing needs deciding. This
# driver resolves the file only when every conflict is such an add/add: both
# sides inserted lines at the same place, neither changed or removed a line
# there, and the two insertions share no non-blank line. It keeps both
# insertions whole (each entry keeps its trailing blank line), the current
# side's first, unless only the current side's insertion holds a heading: then
# the other side's goes first, so its lines stay in the section they were added
# to. It keeps them only if every kept line sits under the same headings as in
# its own version, so an entry never moves into a section the other side
# opened, such as a release it cut above the conflict.
#
# Any other conflict leaves the whole file to git's text merge, conflict
# markers and all: a line one side reworded or removed next to the other side's
# insertion, a revert or cherry-pick that removes an entry another was added
# above, both sides adding the same heading or entry, both insertions holding a
# heading, a kept line that would change sections, or one below a # comment in
# a fenced code block, which reads like a heading and could hide such a change.
#
# The command above runs the copy checked out in the work tree, which a rebase
# or cherry-pick updates as it replays each commit. On a host that rebases
# branches it does not trust, point merge.changelog.driver at a copy outside
# the work tree.
#
# It needs only POSIX sh, awk and git.
set -u

if [ "$#" -lt 3 ]; then
	echo "usage: sh scripts/changelog-merge-driver.sh ANCESTOR CURRENT OTHER [MARKER_SIZE]" >&2
	exit 2
fi
ancestor=$1
current=$2
other=$3
marker_size=${4:-7}

# git's text merge of the three versions, written to the current version's file.
text_merge() {
	git merge-file --marker-size="$marker_size" -L ours -L base -L theirs \
		"$current" "$ancestor" "$other" && exit 0
	exit 1
}

# A file both sides created has no ancestor line to anchor an insertion to, and
# awk would add the newline a version's last line lacks.
[ -s "$ancestor" ] || text_merge
for version in "$ancestor" "$current" "$other"; do
	if [ -s "$version" ] && [ -n "$(tail -c 1 "$version")" ]; then
		text_merge
	fi
done

# The diff3 style keeps each side of a conflict whole. git's default style moves
# lines both sides start or end with out of the conflict: keeping both sides
# would then join two entries at the blank line they end with, and the check
# for a line both sides added would miss it. Markers 32 characters longer than
# git's cannot be mistaken for a line of the file.
wide=$((marker_size + 32))
diff3="$current.diff3.$$"
kept="$current.kept.$$"
trap 'rm -f "$diff3" "$kept"' EXIT
git merge-file -p --diff3 --marker-size="$wide" "$current" "$ancestor" "$other" >"$diff3"
conflicts=$?

if [ "$conflicts" -eq 0 ]; then
	result=$diff3
elif [ "$conflicts" -lt 128 ] && awk -v size="$wide" '
# Reads the current and other versions, noting the headings each of their lines
# sits under, then prints the diff3 merge with each conflict replaced by the
# lines of both sides. Exits 1 when a conflict has ancestor lines, a non-blank
# line on both sides or a heading on both sides, or when a kept line would sit
# under other headings than in its own version.
function is_marker(line, mark) {
	return substr(line, 1, size) == mark && (length(line) == size || substr(line, size + 1, 1) == " ")
}
# The level of a Markdown heading (up to three spaces, one or more #, then a
# blank or the end of the line), at most 6; 0 for any other line. A # comment
# line in a fenced code block counts as a heading too, and masks the heading
# path (see advance).
function heading_level(line) {
	if (line !~ /^ ? ? ?#+[ \t]/ && line !~ /^ ? ? ?#+$/) return 0
	match(line, /#+/)
	return RLENGTH > 6 ? 6 : RLENGTH
}
# Moves the heading path of file (current, other, or merge for the result) past
# line: a heading takes the place of the one at its level and of every deeper
# one. A heading-like line inside a fenced code block, such as a # comment in a
# shell snippet, does the same, so a fence the driver misreads hides no real
# heading; but it may replace a heading the other side changed, so from that
# line on the path is masked.
function advance(file, line,    level, i) {
	if (line ~ /^[ \t]*(```|~~~)/) fenced[file] = !fenced[file]
	level = heading_level(line)
	if (!level) return
	if (fenced[file]) masked[file] = 1
	heading[file, level] = line
	for (i = level + 1; i <= 6; i++) heading[file, i] = ""
	path[file] = ""
	for (i = 1; i <= 6; i++) path[file] = path[file] SUBSEP heading[file, i]
}
# Holds a line of one side of the conflict until both sides are read.
function keep(side, line) {
	if (heading_level(line)) headed[side] = 1
	held[side, ++count[side]] = line
}
# Prints the held lines of one side and checks where they land. Its first line
# that is not blank, not a heading and found under one heading path in its own
# version must sit under that path here too: from that line on, both copies of
# the side run through the same lines and so stay under the same headings. That
# check sees only the levels of the headings above the conflict that the
# headings of the side so far leave in place (below shown), so every line and
# heading of the side before it must depend on no deeper level (below needed).
# Neither path may be masked. A side with no such line is not kept.
function emit(side,    i, line, level, shown, needed, checked) {
	shown = 7
	needed = 0
	for (i = 1; i <= count[side]; i++) {
		line = held[side, i]
		level = heading_level(line)
		advance("merge", line)
		print line
		if (checked || line !~ /[^ \t]/) continue
		if (level) {
			if (level < shown) shown = level
			if (shown > needed) needed = shown
		} else if ((side, line) in ambiguous) {
			if (shown > needed) needed = shown
		} else {
			if (shown < needed || at[side, line] != path["merge"] || masked["merge"] || maskedat[side, line]) exit 1
			checked = 1
		}
	}
	if (!checked) exit 1
}
BEGIN {
	for (i = 0; i < size; i++) {
		ours_mark = ours_mark "<"
		base_mark = base_mark "|"
		split_mark = split_mark "="
		theirs_mark = theirs_mark ">"
	}
	state = "text"
}
# The heading path of each non-blank line of the current and other versions; a
# line found under two paths in one version is ambiguous there, and one found
# below a masked path is masked there.
file != "merge" {
	advance(file, $0)
	if (heading_level($0) || $0 !~ /[^ \t]/) next
	if (masked[file]) maskedat[file, $0] = 1
	if (!((file, $0) in at)) at[file, $0] = path[file]
	else if (at[file, $0] != path[file]) ambiguous[file, $0] = 1
	next
}
state == "text" {
	if (is_marker($0, ours_mark)) {
		state = "ours"
		split("", ours)
		count["current"] = count["other"] = 0
		headed["current"] = headed["other"] = 0
		next
	}
	if (is_marker($0, base_mark) || is_marker($0, split_mark) || is_marker($0, theirs_mark)) exit 1
	advance("merge", $0)
	print
	next
}
state == "ours" {
	if (is_marker($0, base_mark)) {
		state = "base"
		next
	}
	if (is_marker($0, ours_mark) || is_marker($0, split_mark) || is_marker($0, theirs_mark)) exit 1
	if ($0 ~ /[^ \t]/) ours[$0] = 1
	keep("current", $0)
	next
}
state == "base" {
	if (is_marker($0, split_mark)) {
		state = "theirs"
		next
	}
	exit 1
}
state == "theirs" {
	if (is_marker($0, theirs_mark)) {
		# Lines below a heading belong to its section, so the side without
		# one goes first. With a heading on both sides, the lines below the
		# conflict would stay in the section of only one side.
		if (headed["current"] && headed["other"]) exit 1
		if (headed["current"]) {
			emit("other")
			emit("current")
		} else {
			emit("current")
			emit("other")
		}
		state = "text"
		next
	}
	if (is_marker($0, ours_mark) || is_marker($0, base_mark) || is_marker($0, split_mark)) exit 1
	if ($0 ~ /[^ \t]/ && ($0 in ours)) exit 1
	keep("other", $0)
	next
}
END {
	if (state != "text") exit 1
}
' file=current "$current" file=other "$other" file=merge "$diff3" >"$kept"; then
	result=$kept
else
	text_merge
fi
cat "$result" >"$current" || exit 1
exit 0
