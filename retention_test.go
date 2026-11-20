package main

// Retention is a policy, so it gets tests like one: the cutoff must be a typed
// interval (never a string-built date), each table keeps its own window, and a
// bad env var must not be able to empty a table.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type fakePurger struct {
	sqls []string
	args [][]any
	err  error
}

func (f *fakePurger) Exec(ctx context.Context, sql string, args ...any) (int64, error) {
	f.sqls = append(f.sqls, sql)
	f.args = append(f.args, args)
	if f.err != nil {
		return 0, f.err
	}
	return 3, nil
}

func TestKeepDurationClampsAndFallsBack(t *testing.T) {
	def := 24 * time.Hour
	for _, tc := range []struct {
		in   string
		want time.Duration
	}{
		{"", def},         // unset
		{"nonsense", def}, // unparseable
		{"5m", def},       // below the one-hour floor
		{"48h", 48 * time.Hour},
	} {
		if got := keepDuration(tc.in, def); got != tc.want {
			t.Errorf("keepDuration(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestPurgeTelemetryUsesTypedInterval(t *testing.T) {
	db := &fakePurger{}
	got, err := purgeTelemetry(context.Background(), db,
		tableRetention{"spans", "started_at", 30 * 24 * time.Hour},
		tableRetention{"audit_log", "ts", time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if len(db.sqls) != 2 {
		t.Fatalf("statements = %d, want 2", len(db.sqls))
	}
	if !strings.Contains(db.sqls[0], "DELETE FROM spans") || !strings.Contains(db.sqls[1], "DELETE FROM audit_log") {
		t.Fatalf("unexpected SQL: %q", db.sqls)
	}
	for _, sql := range db.sqls {
		if !strings.Contains(sql, "make_interval(secs => $1)") {
			t.Fatalf("cutoff must be a typed interval, got %q", sql)
		}
	}
	if db.args[0][0].(int64) != int64((30*24*time.Hour)/time.Second) {
		t.Fatalf("spans cutoff = %v", db.args[0][0])
	}
	if got["spans"] != 3 || got["audit_log"] != 3 {
		t.Fatalf("rows deleted = %v", got)
	}
}

func TestPurgeTelemetryNamesTheFailingTable(t *testing.T) {
	db := &fakePurger{err: errors.New("connection refused")}
	if _, err := purgeTelemetry(context.Background(), db, tableRetention{"spans", "started_at", time.Hour}); err == nil {
		t.Fatal("expected an error")
	} else if !strings.Contains(err.Error(), "spans") {
		t.Fatalf("error must name the table: %v", err)
	}
}
