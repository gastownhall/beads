package issueops

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
)

// maxIssueIDLen is the width of issues.id (VARCHAR(255)).
const maxIssueIDLen = 255

// AssignChildRenumbers maps local colliding hierarchical children to fresh
// suffixes (bd-28sb25, GH#4796). collisions are child ids both sides added
// with different content; only the minimal roots move, each carrying its
// local subtree (every localID under root+"."; never a sibling like P.710
// for P.71). floors gives each root parent's high-water mark — the caller
// folds both sides' counters and used suffixes (live and retired) into it —
// and roots are numbered above it in (parent, suffix) order. taken holds
// every id a destination must not land on (live or retired, either side).
// Renames come back roots first, then descendants, each group sorted.
func AssignChildRenumbers(collisions, localIDs []string, floors map[string]int, taken map[string]bool) ([]storage.ChildRenumber, error) {
	type root struct {
		id, parent string
		num        int
	}
	collide := make(map[string]bool, len(collisions))
	for _, id := range collisions {
		collide[id] = true
	}
	var roots []root
	for id := range collide {
		parent, num, ok := ParseHierarchicalID(id)
		if !ok {
			return nil, fmt.Errorf("renumber children: %q is not a hierarchical child id", id)
		}
		if hasCollidingAncestor(id, collide) {
			continue
		}
		roots = append(roots, root{id: id, parent: parent, num: num})
	}
	sort.Slice(roots, func(i, j int) bool {
		if roots[i].parent != roots[j].parent {
			return roots[i].parent < roots[j].parent
		}
		return roots[i].num < roots[j].num
	})

	next := make(map[string]int)
	var rootRenames, descRenames []storage.ChildRenumber
	assigned := make(map[string]bool)
	claim := func(from, to string) error {
		if len(to) > maxIssueIDLen {
			return fmt.Errorf("renumber children: %s -> %s exceeds the %d-character id limit", from, to, maxIssueIDLen)
		}
		if taken[to] || assigned[to] {
			return fmt.Errorf("renumber children: destination %s for %s is already used", to, from)
		}
		assigned[to] = true
		return nil
	}
	for _, r := range roots {
		n, seen := next[r.parent]
		if !seen {
			floor, ok := floors[r.parent]
			if !ok {
				return nil, fmt.Errorf("renumber children: no high-water mark for parent %s", r.parent)
			}
			n = floor
		}
		if n >= math.MaxInt32 {
			return nil, fmt.Errorf("renumber children: parent %s has no child suffix left above %d", r.parent, n)
		}
		n++
		next[r.parent] = n
		to := fmt.Sprintf("%s.%d", r.parent, n)
		if err := claim(r.id, to); err != nil {
			return nil, err
		}
		rootRenames = append(rootRenames, storage.ChildRenumber{From: r.id, To: to})
	}

	sortedLocal := append([]string(nil), localIDs...)
	sort.Strings(sortedLocal)
	for _, rr := range rootRenames {
		prefix := rr.From + "."
		for _, id := range sortedLocal {
			if !strings.HasPrefix(id, prefix) {
				continue
			}
			to := rr.To + id[len(rr.From):]
			if err := claim(id, to); err != nil {
				return nil, err
			}
			descRenames = append(descRenames, storage.ChildRenumber{From: id, To: to})
		}
	}
	return append(rootRenames, descRenames...), nil
}

// hasCollidingAncestor reports whether a proper hierarchical ancestor of id
// is itself a collision, which makes id part of that root's subtree.
func hasCollidingAncestor(id string, collide map[string]bool) bool {
	for {
		parent, _, ok := ParseHierarchicalID(id)
		if !ok {
			return false
		}
		if collide[parent] {
			return true
		}
		id = parent
	}
}
