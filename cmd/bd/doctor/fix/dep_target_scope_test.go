package fix

import (
	"context"
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// TestIsUnverifiableDepTarget pins which missing dependency targets doctor
// leaves alone: an external: ref, or a bare ID under another repository's
// prefix (bd-zcpo44). A target under a prefix this database holds is an
// orphan when missing.
func TestIsUnverifiableDepTarget(t *testing.T) {
	cases := []struct {
		name                string
		source, target      string
		issuePrefix, allowd string
		want                bool
	}{
		{"external_ref", "bd-1", "external:gt:capability", "bd", "", true},
		{"cross_prefix_not_held", "bd-1", "gt-9", "bd", "", true},
		{"same_prefix", "bd-1", "bd-gone", "bd", "", false},
		{"same_prefix,_not_held", "bd-1", "bd-gone", "zz", "", false},
		{"allowed_prefix", "bd-1", "gt-9", "bd", "gt", false},
		{"allowed_prefix_spaced_with_hyphen", "bd-1", "gt-9", "bd", " hq , gt- ", false},
		{"hyphenated_allowed_prefix", "bd-1", "hq-cv-9", "bd", "hq-cv", false},
		{"target_is_issue_prefix_from_allowed_source", "gt-1", "bd-9", "bd-", "gt", false},
		{"hyphenless_target", "bd-1", "abc", "bd", "", false},
		{"hyphenless_source", "abc", "gt-9", "bd", "", false},
		{"no_prefix_config", "bd-1", "gt-9", "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsUnverifiableDepTarget(tc.source, tc.target, tc.issuePrefix, tc.allowd); got != tc.want {
				t.Fatalf("IsUnverifiableDepTarget(%q, %q, %q, %q) = %v, want %v",
					tc.source, tc.target, tc.issuePrefix, tc.allowd, got, tc.want)
			}
		})
	}
}

func TestLoadDepTargetPrefixes(t *testing.T) {
	query := regexp.QuoteMeta("SELECT `key`, COALESCE(`value`, '') FROM config WHERE `key` IN ('issue_prefix', 'allowed_prefixes')")

	t.Run("both_keys", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		mock.ExpectQuery(query).WillReturnRows(sqlmock.NewRows([]string{"key", "value"}).
			AddRow("issue_prefix", "bd").AddRow("allowed_prefixes", "gt, hq-cv"))
		ip, ap, err := LoadDepTargetPrefixes(context.Background(), db)
		if err != nil || ip != "bd" || ap != "gt, hq-cv" {
			t.Fatalf("got (%q, %q, %v), want (\"bd\", \"gt, hq-cv\", nil)", ip, ap, err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("no_keys", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		mock.ExpectQuery(query).WillReturnRows(sqlmock.NewRows([]string{"key", "value"}))
		ip, ap, err := LoadDepTargetPrefixes(context.Background(), db)
		if err != nil || ip != "" || ap != "" {
			t.Fatalf("got (%q, %q, %v), want (\"\", \"\", nil)", ip, ap, err)
		}
	})

	t.Run("query_error", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		boom := errors.New("boom")
		mock.ExpectQuery(query).WillReturnError(boom)
		if _, _, err := LoadDepTargetPrefixes(context.Background(), db); !errors.Is(err, boom) {
			t.Fatalf("err = %v, want %v", err, boom)
		}
	})
}
