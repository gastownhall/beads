//go:build embeddeddolt

package embeddeddolt

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/cenkalti/backoff/v4"
	doltembed "github.com/dolthub/driver"
)

const (
	commitName  = "beads"
	commitEmail = "beads@local"
)

// OpenSQL opens an embedded Dolt database at dir. The returned cleanup
// function closes both the *sql.DB and the underlying connector.
func OpenSQL(ctx context.Context, dir, database, branch string) (*sql.DB, func() error, error) {
	dsn := buildDSN(dir, database)

	cfg, err := doltembed.ParseDSN(dsn)
	if err != nil {
		return nil, nil, err
	}

	bo := backoff.NewExponentialBackOff()
	bo.MaxElapsedTime = 0 // wait until ctx cancellation
	bo.MaxInterval = 5 * time.Second
	cfg.BackOff = bo

	connector, err := doltembed.NewConnector(cfg)
	if err != nil {
		return nil, nil, err
	}

	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxIdleTime(0)
	db.SetConnMaxLifetime(0)

	cleanup := func() error {
		return errors.Join(db.Close(), connector.Close())
	}

	if err := db.PingContext(ctx); err != nil {
		return nil, nil, errors.Join(err, cleanup())
	}

	if strings.TrimSpace(branch) != "" && strings.TrimSpace(database) != "" {
		if _, err := db.ExecContext(ctx, "USE `"+database+"`"); err != nil {
			return nil, nil, errors.Join(err, cleanup())
		}
		if _, err := db.ExecContext(ctx, fmt.Sprintf("SET @@%s_head_ref = %s", database, sqlStringLiteral(branch))); err != nil {
			return nil, nil, errors.Join(err, cleanup())
		}
	}

	return db, cleanup, nil
}

func buildDSN(dir, database string) string {
	v := url.Values{}
	v.Set(doltembed.CommitNameParam, commitName)
	v.Set(doltembed.CommitEmailParam, commitEmail)
	v.Set(doltembed.MultiStatementsParam, "true")
	if strings.TrimSpace(database) != "" {
		v.Set(doltembed.DatabaseParam, database)
	}
	u := url.URL{Scheme: "file", Path: encodeDir(dir), RawQuery: v.Encode()}
	return u.String()
}

func encodeDir(dir string) string {
	if os.PathSeparator == '\\' {
		return strings.ReplaceAll(dir, `\`, `/`)
	}
	return dir
}

func sqlStringLiteral(s string) string {
	return "'" + strings.ReplaceAll(strings.TrimSpace(s), "'", "''") + "'"
}
