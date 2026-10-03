package main

import (
	"strings"
	"testing"
)

func TestBatchJSONParsesPinnedCreateAndVersionGuard(t *testing.T) {
	request, err := parseBatchJSON(strings.NewReader(`{"schema_version":"1","request":{"Items":[{"Kind":"create","Create":{"Issue":{"id":"bd-record","title":"cited record","issue_type":"task","status":"closed","description":"quotes \" and newlines\n","metadata":{"flag":true,"count":9007199254740993},"labels":["evidence"]}}},{"Kind":"update","Update":{"Target":{"ID":"bd-state"},"ExpectedVersion":42,"Patch":{"Metadata":{"Set":{"epoch":"new"}}}}}]}}`), "fixture-actor")
	if err != nil || len(request.Items) != 2 {
		t.Fatalf("JSON plan: %+v, %v", request, err)
	}
	if request.Items[0].Create.Issue.ID != "bd-record" || *request.Items[1].Update.ExpectedVersion != 42 {
		t.Fatal("pinned identity or guard was discarded")
	}
}

func TestBatchJSONRejectsUnknownAndAmbiguousFieldsBeforeWrites(t *testing.T) {
	for _, input := range []string{
		`{"schema_version":"1","request":{"Items":[{"Kind":"create","Create":{"Issue":null}}]}}`,
		`{"schema_version":"1","request":{"Items":[{"Kind":"unknown"}]}}`,
		`{"schema_version":"1","request":{"Items":[{"Kind":"create","Create":{"Issue":{"id":"bd-x","title":"x","row_version":42}}}]}}`,
		`{"schema_version":"1","request":{"Actor":"human","Items":[]}}`,
		`{"schema_version":"1","request":{}} {}`,
	} {
		if _, err := parseBatchJSON(strings.NewReader(input), "fixture-actor"); err == nil {
			t.Fatalf("invalid plan accepted: %s", input)
		}
	}
}
