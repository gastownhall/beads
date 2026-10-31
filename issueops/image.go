package issueops

import "context"

// Image is the canonical, hashable picture of one bead: every stored row that
// belongs to it, read in one transaction, with a revision over the whole.
//
// IT IS RAW. Every row is a map from column name to the stored value — no
// rename, no omitempty, SQL NULL as JSON null, a JSON-typed column (metadata)
// as the text stored, a timestamp as UTC RFC 3339 at the column's stored
// fractional precision. It is the shape an archive keeps and a restore reads
// back, not the shape a reader renders; types.Issue is that one.
//
// GENERATED COLUMNS ARE LEFT OUT. They are a function of the other columns,
// so they carry nothing an archive needs, and leaving them out keeps a
// deployment's own extra generated column from changing every revision.
//
// The plane a dependency row came from is not recorded beside it because it
// does not need to be: dependencies holds edges whose issue_id is durable and
// wisp_dependencies those whose issue_id is a wisp, so a row's own issue_id
// names its table.
type Image struct {
	// ID is the bead's id.
	ID string `json:"id"`
	// Table is the bead's own table: "issues" or "wisps". It also names the
	// plane of Labels, Comments and Events, which always come from the same
	// plane as the bead.
	Table string `json:"table"`
	// Revision is the sha256 hex of the canonical image without this field.
	// It changes on any write to the bead — its row, a label, a comment, an
	// event, or a dependency in EITHER direction — so a caller that archived
	// an image can later ask whether the bead is still the one it archived.
	Revision string `json:"revision"`
	// Row is the bead's own row.
	Row map[string]any `json:"row"`
	// Labels, Dependencies, Comments and Events are the bead's rows in each
	// associated table, ordered by primary key. Dependencies holds edges in
	// both directions — rows whose issue_id is the bead and rows whose target
	// is — from both edge tables. An empty set is an empty list, never null.
	Labels       []map[string]any `json:"labels"`
	Dependencies []map[string]any `json:"dependencies"`
	Comments     []map[string]any `json:"comments"`
	Events       []map[string]any `json:"events"`
}

// Imager is the role the stores implement: read beads as Images.
// A new capability gets a new role interface and its own accessor; never
// append a method here.
//
// READ-ONLY, AND ONE SNAPSHOT. Every image a call returns is read in one
// transaction, so two images of beads joined by an edge agree about it.
type Imager interface {
	// Image returns one Image per distinct id, in the order the ids were
	// first named; duplicates collapse.
	//
	// IDS ARE EXACT. An empty request, or a blank id, is ErrValidation. An id
	// naming no row in either plane is a *NotFoundError naming EVERY missing
	// id, matching ErrNotFound, and no image is returned — not even for the
	// ids that resolved.
	Image(ctx context.Context, ids []string) ([]Image, error)
}
