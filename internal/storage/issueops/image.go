package issueops

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"sort"
	"strconv"
	"strings"
	"time"

	publicops "github.com/steveyegge/beads/issueops"
)

// imageTables is one plane's six table names, in the roles an Image reads.
type imageTables struct {
	issue, label, comment, event, dep string
}

var (
	durableImageTables = imageTables{"issues", "labels", "comments", "events", "dependencies"}
	wispImageTables    = imageTables{"wisps", "wisp_labels", "wisp_comments", "wisp_events", "wisp_dependencies"}
)

// imageRowKeys are the primary-key columns each row list sorts by. They are
// fixed here rather than discovered so CanonicalImageJSON, which sees only an
// Image, sorts exactly as ImageInTx does; a row that lacks a key, or ties on
// one, falls back to its canonical bytes, so the order is total either way.
var (
	imageLabelKeys = []string{"issue_id", "label"}
	imageIDKeys    = []string{"id"}
)

// imageColumn is one non-generated column of a table, as SHOW COLUMNS reports
// it.
type imageColumn struct {
	name string
	typ  string // lowercased, e.g. "datetime(6)", "bigint unsigned"
}

// imageColumnCache holds each table's non-generated columns for one call.
type imageColumnCache map[string][]imageColumn

// columns discovers table's non-generated columns once per call, from SHOW
// COLUMNS, which reads one table's definition rather than information_schema.
// A column is generated when Extra says so ("STORED GENERATED", "VIRTUAL
// GENERATED"; not "DEFAULT_GENERATED", which marks an expression default).
//
//nolint:gosec // table is one of the fixed imageTables names.
func (c imageColumnCache) columns(ctx context.Context, tx DBTX, table string) ([]imageColumn, error) {
	if cols, ok := c[table]; ok {
		return cols, nil
	}
	rows, err := tx.QueryContext(ctx, "SHOW COLUMNS FROM "+table)
	if err != nil {
		return nil, fmt.Errorf("image: columns of %s: %w", table, err)
	}
	defer rows.Close()
	names, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("image: columns of %s: %w", table, err)
	}
	field, typ, extra := -1, -1, -1
	for i, name := range names {
		switch strings.ToLower(name) {
		case "field":
			field = i
		case "type":
			typ = i
		case "extra":
			extra = i
		}
	}
	if field < 0 || typ < 0 || extra < 0 {
		return nil, fmt.Errorf("image: columns of %s: unexpected SHOW COLUMNS shape %v", table, names)
	}
	var cols []imageColumn
	for rows.Next() {
		vals := make([]any, len(names))
		ptrs := make([]any, len(names))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, fmt.Errorf("image: columns of %s: scan: %w", table, err)
		}
		name := imageText(vals[field])
		if imageExtraGenerated(imageText(vals[extra])) {
			continue
		}
		cols = append(cols, imageColumn{name: name, typ: strings.ToLower(imageText(vals[typ]))})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("image: columns of %s: rows: %w", table, err)
	}
	if len(cols) == 0 {
		return nil, fmt.Errorf("image: columns of %s: no stored columns", table)
	}
	c[table] = cols
	return cols, nil
}

// imageExtraGenerated reports whether a SHOW COLUMNS Extra value marks a
// generated column. The word must stand alone: DEFAULT_GENERATED is an
// expression default on a stored column.
func imageExtraGenerated(extra string) bool {
	for _, word := range strings.Fields(strings.ToUpper(extra)) {
		if word == "GENERATED" {
			return true
		}
	}
	return false
}

// ImageInTx reads one publicops.Image per distinct id, in first-mention order,
// inside tx. See publicops.Imager for the contract: an empty request or a
// blank id is ErrValidation, and any id naming no row in either plane fails
// the whole call with a *publicops.NotFoundError naming every missing id.
func ImageInTx(ctx context.Context, tx DBTX, ids []string) ([]publicops.Image, error) {
	if len(ids) == 0 {
		return nil, fmt.Errorf("%w: image needs at least one id", publicops.ErrValidation)
	}
	seen := make(map[string]struct{}, len(ids))
	unique := make([]string, 0, len(ids))
	for _, id := range ids {
		if strings.TrimSpace(id) == "" {
			return nil, fmt.Errorf("%w: image id must not be blank", publicops.ErrValidation)
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}

	wispIDs, permIDs, err := PartitionWispIDsInTx(ctx, tx, unique)
	if err != nil {
		return nil, fmt.Errorf("image: %w", err)
	}
	isWisp := make(map[string]bool, len(wispIDs))
	for _, id := range wispIDs {
		isWisp[id] = true
	}
	present, err := imageDurablePresent(ctx, tx, permIDs)
	if err != nil {
		return nil, err
	}
	var missing []string
	for _, id := range unique {
		if !isWisp[id] && !present[id] {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		return nil, &publicops.NotFoundError{IDs: missing}
	}

	cache := imageColumnCache{}
	byID := make(map[string]*publicops.Image, len(unique))
	for _, plane := range []struct {
		own  imageTables
		wisp bool
	}{{durableImageTables, false}, {wispImageTables, true}} {
		var planeIDs []string
		for _, id := range unique {
			if isWisp[id] == plane.wisp {
				planeIDs = append(planeIDs, id)
			}
		}
		for start := 0; start < len(planeIDs); start += queryBatchSize {
			end := min(start+queryBatchSize, len(planeIDs))
			if err := imageBatchInTx(ctx, tx, cache, plane.own, planeIDs[start:end], byID); err != nil {
				return nil, err
			}
		}
	}
	images := make([]publicops.Image, 0, len(unique))
	for _, id := range unique {
		img := byID[id]
		if img.Row == nil {
			// The probe saw it, so only a row deleted under a non-snapshot
			// transaction gets here.
			return nil, &publicops.NotFoundError{IDs: []string{id}}
		}
		sortImageRows(img.Labels, imageLabelKeys)
		sortImageRows(img.Comments, imageIDKeys)
		sortImageRows(img.Events, imageIDKeys)
		sortImageRows(img.Dependencies, imageIDKeys)
		img.Revision = ComputeImageRevision(*img)
		images = append(images, *img)
	}
	return images, nil
}

// imageDurablePresent reports which of ids name a row in issues.
func imageDurablePresent(ctx context.Context, tx DBTX, ids []string) (map[string]bool, error) {
	present := make(map[string]bool, len(ids))
	for start := 0; start < len(ids); start += queryBatchSize {
		end := min(start+queryBatchSize, len(ids))
		batch := ids[start:end]
		args := make([]any, len(batch))
		for i, id := range batch {
			args[i] = id
		}
		//nolint:gosec // placeholders only.
		rows, err := tx.QueryContext(ctx, "SELECT id FROM issues WHERE id IN ("+imagePlaceholders(len(batch))+")", args...)
		if err != nil {
			return nil, fmt.Errorf("image: probe issues: %w", err)
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("image: probe issues: scan: %w", err)
			}
			present[id] = true
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("image: probe issues: rows: %w", err)
		}
	}
	return present, nil
}

// imageBatchInTx reads the images of ids, all of plane own, into out: one
// read of each of own's issue, label, comment and event tables and one of each
// edge table, each over every id at once. An image gets every row matching it
// as its own single-id read would, so an edge between two requested ids
// belongs to both. An id whose own row is not read exactly once is left with
// a nil Row.
func imageBatchInTx(ctx context.Context, tx DBTX, cache imageColumnCache, own imageTables, ids []string, out map[string]*publicops.Image) error {
	batch := make(map[string]*publicops.Image, len(ids))
	for _, id := range ids {
		img := &publicops.Image{
			ID:           id,
			Table:        own.issue,
			Labels:       []map[string]any{},
			Comments:     []map[string]any{},
			Events:       []map[string]any{},
			Dependencies: []map[string]any{},
		}
		batch[id] = img
		out[id] = img
	}

	matches, err := imageSelect(ctx, tx, cache, own.issue, []string{"id"}, ids)
	if err != nil {
		return err
	}
	seen := make(map[string]int, len(ids))
	for _, m := range matches {
		for _, id := range m.ids {
			if img, ok := batch[id]; ok {
				img.Row = imageRowFor(m, id)
				seen[id]++
			}
		}
	}
	for id, img := range batch {
		if seen[id] != 1 {
			img.Row = nil
		}
	}
	for _, part := range []struct {
		table string
		add   func(*publicops.Image, map[string]any)
	}{
		{own.label, func(img *publicops.Image, row map[string]any) { img.Labels = append(img.Labels, row) }},
		{own.comment, func(img *publicops.Image, row map[string]any) { img.Comments = append(img.Comments, row) }},
		{own.event, func(img *publicops.Image, row map[string]any) { img.Events = append(img.Events, row) }},
	} {
		matches, err := imageSelect(ctx, tx, cache, part.table, []string{"issue_id"}, ids)
		if err != nil {
			return err
		}
		for _, m := range matches {
			for _, id := range m.ids {
				if img, ok := batch[id]; ok {
					part.add(img, imageRowFor(m, id))
				}
			}
		}
	}

	// Edges in both directions, from both edge tables: a wisp can depend on a
	// durable bead and a durable bead on a wisp.
	for _, table := range []string{durableImageTables.dep, wispImageTables.dep} {
		cols, err := cache.columns(ctx, tx, table)
		if err != nil {
			// The other plane's edge table is clone-local and may not exist
			// yet; its absence means no edges there. The bead's own plane
			// must have one.
			if table != own.dep && isTableNotExistError(err) {
				continue
			}
			return err
		}
		where := []string{"issue_id"}
		for _, col := range cols {
			if strings.HasPrefix(col.name, "depends_on_") && strings.HasSuffix(col.name, "_id") {
				where = append(where, col.name)
			}
		}
		matches, err := imageSelect(ctx, tx, cache, table, where, ids)
		if err != nil {
			return err
		}
		// A row naming one id in two columns (a self-edge) comes once per
		// column; its single-id read listed it once.
		listed := map[string]map[string]bool{}
		for _, m := range matches {
			key, err := imageMarshal(m.row)
			if err != nil {
				return fmt.Errorf("image: read %s: %w", table, err)
			}
			for _, id := range m.ids {
				img, ok := batch[id]
				if !ok || listed[id][string(key)] {
					continue
				}
				if listed[id] == nil {
					listed[id] = map[string]bool{}
				}
				listed[id][string(key)] = true
				img.Dependencies = append(img.Dependencies, imageRowFor(m, id))
			}
		}
	}
	return nil
}

// imageMatch is one row of an image table with the requested ids it matched.
type imageMatch struct {
	ids []string
	row map[string]any
}

// imageRowFor is m's row for the image of id. A row matching several
// requested ids gives each image its own map, as their single-id reads did.
func imageRowFor(m imageMatch, id string) map[string]any {
	if len(m.ids) < 2 || id == m.ids[0] {
		return m.row
	}
	return maps.Clone(m.row)
}

// imageSelect reads every stored column of table's rows where any of match
// equals one of ids, normalized as Image documents, each paired with the ids
// it matched. One statement, one UNION ALL arm per match column; a row
// matching in two columns comes once per arm.
//
// The database names the matched ids. Every comparison is the match column
// against a bound id, `col = ?` as the single-id read's was, so it runs under
// the column's own collation whatever the collation of the issue table's id
// or of the other columns: the arm keeps rows equal to any id, and CONCAT_WS
// lists, by its index in ids, every requested id the row's column equals. Go
// byte equality would miss a row naming a bead in another case under a
// case-insensitive column; a join to the issue table would compare under a
// collation the two columns negotiate; and Dolt matches `col IN (...)` on a
// column without an index byte for byte, whatever its collation.
func imageSelect(ctx context.Context, tx DBTX, cache imageColumnCache, table string, match []string, ids []string) ([]imageMatch, error) {
	cols, err := cache.columns(ctx, tx, table)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(cols))
	for i, col := range cols {
		names[i] = "t." + imageQuoteIdent(col.name)
	}
	arms := make([]string, len(match))
	args := make([]any, 0, 2*len(match)*len(ids))
	for i, col := range match {
		equals := "t." + imageQuoteIdent(col) + " = ?"
		which := make([]string, len(ids))
		eq := make([]string, len(ids))
		for j, id := range ids {
			which[j] = "CASE WHEN " + equals + " THEN '" + strconv.Itoa(j) + "' END"
			eq[j] = equals
			args = append(args, id)
		}
		for _, id := range ids {
			args = append(args, id)
		}
		//nolint:gosec // table is a fixed name; columns come from SHOW COLUMNS and are quoted; indexes are integers.
		arms[i] = "SELECT CONCAT_WS(',', " + strings.Join(which, ", ") + "), " + strings.Join(names, ", ") +
			" FROM " + table + " t WHERE " + strings.Join(eq, " OR ")
	}
	rows, err := tx.QueryContext(ctx, strings.Join(arms, " UNION ALL "), args...)
	if err != nil {
		return nil, fmt.Errorf("image: read %s: %w", table, err)
	}
	defer rows.Close()
	var out []imageMatch
	for rows.Next() {
		vals := make([]any, len(cols)+1)
		ptrs := make([]any, len(vals))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, fmt.Errorf("image: read %s: scan: %w", table, err)
		}
		var matched []string
		if which := imageText(vals[0]); which != "" {
			for _, n := range strings.Split(which, ",") {
				j, err := strconv.Atoi(n)
				if err != nil || j < 0 || j >= len(ids) {
					return nil, fmt.Errorf("image: read %s: matched id index %q", table, n)
				}
				matched = append(matched, ids[j])
			}
		}
		row := make(map[string]any, len(cols))
		for i, col := range cols {
			v, err := normalizeImageValue(col.typ, vals[i+1])
			if err != nil {
				return nil, fmt.Errorf("image: read %s.%s: %w", table, col.name, err)
			}
			row[col.name] = v
		}
		out = append(out, imageMatch{ids: matched, row: row})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("image: read %s: rows: %w", table, err)
	}
	return out, nil
}

// normalizeImageValue maps a driver value to its one Image form by the
// column's declared type, so the text protocol, the binary protocol and the
// embedded driver — which disagree about []byte versus int64 versus
// time.Time — produce the same image and the same revision.
func normalizeImageValue(typ string, v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	base := typ
	if i := strings.IndexAny(base, "( "); i >= 0 {
		base = base[:i]
	}
	switch base {
	case "tinyint", "smallint", "mediumint", "int", "integer", "bigint", "year":
		if strings.Contains(typ, "unsigned") {
			return imageUint(v)
		}
		return imageInt(v)
	case "float", "double", "real":
		return imageFloat(v)
	case "datetime", "timestamp":
		return imageTime(v, imagePrecision(typ), true), nil
	case "date":
		return imageTime(v, 0, false), nil
	case "binary", "varbinary", "tinyblob", "blob", "mediumblob", "longblob", "bit":
		switch x := v.(type) {
		case []byte:
			return x, nil
		case string:
			return []byte(x), nil
		}
		return v, nil
	case "json":
		switch x := v.(type) {
		case []byte, string:
			return imageText(x), nil
		}
		b, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		return string(b), nil
	}
	switch x := v.(type) {
	case []byte:
		return string(x), nil
	case time.Time:
		return imageTime(x, 0, true), nil
	}
	return v, nil
}

func imageInt(v any) (any, error) {
	switch x := v.(type) {
	case int64:
		return x, nil
	case int:
		return int64(x), nil
	case int32:
		return int64(x), nil
	case int16:
		return int64(x), nil
	case int8:
		return int64(x), nil
	case uint64, uint32, uint16, uint8, uint:
		return imageUint(v)
	case bool:
		if x {
			return int64(1), nil
		}
		return int64(0), nil
	case []byte, string:
		return strconv.ParseInt(imageText(x), 10, 64)
	}
	return nil, fmt.Errorf("integer column holds %T", v)
}

func imageUint(v any) (any, error) {
	switch x := v.(type) {
	case uint64:
		return x, nil
	case uint:
		return uint64(x), nil
	case uint32:
		return uint64(x), nil
	case uint16:
		return uint64(x), nil
	case uint8:
		return uint64(x), nil
	case int64:
		return uint64(x), nil //nolint:gosec // an unsigned column holds no negative value.
	case int:
		return uint64(x), nil //nolint:gosec // an unsigned column holds no negative value.
	case []byte, string:
		return strconv.ParseUint(imageText(x), 10, 64)
	}
	return nil, fmt.Errorf("unsigned column holds %T", v)
}

func imageFloat(v any) (any, error) {
	switch x := v.(type) {
	case float64:
		return x, nil
	case float32:
		return float64(x), nil
	case []byte, string:
		return strconv.ParseFloat(imageText(x), 64)
	}
	return nil, fmt.Errorf("float column holds %T", v)
}

// imageTime renders a stored timestamp as UTC RFC 3339 with exactly digits
// fractional digits — the column's stored precision. A value that does not
// parse (a zero date) is kept as the text stored.
func imageTime(v any, digits int, clock bool) any {
	var t time.Time
	switch x := v.(type) {
	case time.Time:
		t = x
	case []byte, string:
		s := imageText(x)
		parsed := false
		for _, layout := range []string{"2006-01-02 15:04:05.999999999", time.RFC3339Nano, "2006-01-02"} {
			if p, err := time.Parse(layout, s); err == nil {
				t, parsed = p, true
				break
			}
		}
		if !parsed {
			return s
		}
	default:
		return v
	}
	if !clock {
		return t.UTC().Format("2006-01-02")
	}
	layout := "2006-01-02T15:04:05"
	if digits > 0 {
		layout += "." + strings.Repeat("0", digits)
	}
	return t.UTC().Format(layout + "Z07:00")
}

// imagePrecision returns n from "datetime(n)", or 0.
func imagePrecision(typ string) int {
	open := strings.IndexByte(typ, '(')
	end := strings.IndexByte(typ, ')')
	if open < 0 || end < open {
		return 0
	}
	n, err := strconv.Atoi(typ[open+1 : end])
	if err != nil || n < 0 || n > 9 {
		return 0
	}
	return n
}

func imageText(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case []byte:
		return string(x)
	case string:
		return x
	}
	return fmt.Sprint(v)
}

func imageQuoteIdent(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}

func imagePlaceholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// sortImageRows orders rows by keys, then by each row's canonical bytes.
func sortImageRows(rows []map[string]any, keys []string) {
	type keyed struct {
		key  [][]byte
		full []byte
		row  map[string]any
	}
	ks := make([]keyed, len(rows))
	for i, row := range rows {
		k := keyed{row: row}
		for _, key := range keys {
			b, _ := imageMarshal(row[key])
			k.key = append(k.key, b)
		}
		k.full, _ = imageMarshal(row)
		ks[i] = k
	}
	sort.SliceStable(ks, func(i, j int) bool {
		for n := range ks[i].key {
			if c := bytes.Compare(ks[i].key[n], ks[j].key[n]); c != 0 {
				return c < 0
			}
		}
		return bytes.Compare(ks[i].full, ks[j].full) < 0
	})
	for i := range ks {
		rows[i] = ks[i].row
	}
}

// imageMarshal is compact JSON with map keys sorted (encoding/json sorts them)
// and no HTML escaping, so stored text appears as stored.
func imageMarshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// CanonicalImageJSON is the one byte form of img that revisions hash: compact
// JSON, every object's keys sorted, every row list sorted by primary key and a
// nil list written as []. The revision key is written only when Revision is
// set, so the form with Revision cleared is the image without that field. It
// never reorders the caller's slices.
func CanonicalImageJSON(img publicops.Image) ([]byte, error) {
	sorted := func(rows []map[string]any, keys []string) []map[string]any {
		out := append([]map[string]any{}, rows...)
		sortImageRows(out, keys)
		return out
	}
	doc := map[string]any{
		"id":           img.ID,
		"table":        img.Table,
		"row":          img.Row,
		"labels":       sorted(img.Labels, imageLabelKeys),
		"dependencies": sorted(img.Dependencies, imageIDKeys),
		"comments":     sorted(img.Comments, imageIDKeys),
		"events":       sorted(img.Events, imageIDKeys),
	}
	if img.Revision != "" {
		doc["revision"] = img.Revision
	}
	return imageMarshal(doc)
}

// ComputeImageRevision is the sha256 hex of CanonicalImageJSON with Revision
// cleared. An image json cannot encode (ImageInTx never builds one) hashes
// the empty input rather than failing.
func ComputeImageRevision(img publicops.Image) string {
	img.Revision = ""
	b, _ := CanonicalImageJSON(img)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
