package console

// The Requests page reads the newest traces, so its scans must be bounded to a
// day: a name predicate alone matches every request ever written, which is how
// the page ended up scanning and detoasting JSONB well past the point any row
// could appear (audit 2026-11-18 A2).

import (
	"context"
	"strings"
	"testing"
)

func TestRecentRequestsIsBoundedTo24Hours(t *testing.T) {
	db := &seriesDB{rows: &seriesRows{}}
	store := SQLStore{Query: db}
	if _, err := store.RecentRequests(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(db.sql, "started_at > now() - interval '24 hours'"); n != 2 {
		t.Fatalf("both scans must be bounded, found %d bound(s) in: %s", n, db.sql)
	}
	if !strings.Contains(db.sql, "router.answer") {
		t.Fatalf("a gateway answer is still a request: %s", db.sql)
	}
}
