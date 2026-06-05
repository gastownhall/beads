package db

import (
	"sort"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/storage"
)

type clauseBuf struct {
	where []string
	args  []any
}

func (c *clauseBuf) and(clause string, args ...any) {
	c.where = append(c.where, clause)
	c.args = append(c.args, args...)
}

func eqStrPtr[T ~string](c *clauseBuf, col string, p *T) {
	if p == nil {
		return
	}
	c.and(col+" = ?", string(*p))
}

func eqIntPtr(c *clauseBuf, col string, p *int) {
	if p == nil {
		return
	}
	c.and(col+" = ?", *p)
}

func inList[T ~string](c *clauseBuf, col string, vals []T) {
	if len(vals) == 0 {
		return
	}
	ph, args := buildInPlaceholders(vals)
	c.and(col+" IN ("+ph+")", args...)
}

func notInList[T ~string](c *clauseBuf, col string, vals []T) {
	if len(vals) == 0 {
		return
	}
	ph, args := buildInPlaceholders(vals)
	c.and(col+" NOT IN ("+ph+")", args...)
}

func likeLowerContains(c *clauseBuf, col, term string) {
	if term == "" {
		return
	}
	c.and("LOWER("+col+") LIKE ?", "%"+strings.ToLower(term)+"%")
}

func timeOp(c *clauseBuf, col, op string, t *time.Time) {
	if t == nil {
		return
	}
	c.and(col+" "+op+" ?", t.Format(time.RFC3339))
}

func boolFlag(c *clauseBuf, col string, p *bool) {
	if p == nil {
		return
	}
	if *p {
		c.and(col + " = 1")
	} else {
		c.and("(" + col + " = 0 OR " + col + " IS NULL)")
	}
}

func nullOrEmpty(c *clauseBuf, col string) {
	c.and("(" + col + " IS NULL OR " + col + " = '')")
}

func (c *clauseBuf) metadata(hasKey string, fields map[string]string) error {
	var err error
	c.where, c.args, err = appendMetadataClauses(c.where, c.args, hasKey, fields)
	return err
}

func appendMetadataClauses(where []string, args []any, hasKey string, fields map[string]string) ([]string, []any, error) {
	if hasKey != "" {
		if err := storage.ValidateMetadataKey(hasKey); err != nil {
			return nil, nil, err
		}
		where = append(where, "JSON_EXTRACT(metadata, ?) IS NOT NULL")
		args = append(args, storage.JSONMetadataPath(hasKey))
	}
	if len(fields) > 0 {
		keys := make([]string, 0, len(fields))
		for k := range fields {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if err := storage.ValidateMetadataKey(k); err != nil {
				return nil, nil, err
			}
			where = append(where, "JSON_UNQUOTE(JSON_EXTRACT(metadata, ?)) = ?")
			args = append(args, storage.JSONMetadataPath(k), fields[k])
		}
	}
	return where, args, nil
}
