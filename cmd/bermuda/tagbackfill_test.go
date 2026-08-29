package main

import (
	"bytes"
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/bon5co/bermuda/v3/internal/store"
	_ "modernc.org/sqlite"
)

// Opening the store normalises tags left over from before they were sanitised
// on write, and says how many rows it touched.
//
// The row is dirtied with raw SQL on purpose: PutJob sanitises, so there is no
// way through the store's own API to produce the state an older binary left
// behind, and a test that cannot produce that state proves nothing about the
// backfill.
func TestOpenStoreReportsTagBackfill(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BERMUDA_STATE_DIR", dir)
	ctx := context.Background()

	s, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutJob(ctx, store.Job{ID: "t1", Name: "tagged", Model: store.DefaultModel}); err != nil {
		t.Fatal(err)
	}
	s.Close()

	db, err := sql.Open("sqlite", filepath.Join(dir, "bermuda.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE jobs SET tags=? WHERE id=?`,
		"  Marketing , marketing,, daily  ,DAILY", "t1"); err != nil {
		t.Fatal(err)
	}
	db.Close()

	s2, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if s2.TagsNormalized != 1 {
		t.Fatalf("TagsNormalized = %d, want 1", s2.TagsNormalized)
	}
	got, err := s2.Job(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"marketing", "daily"}; !reflect.DeepEqual(got.Tags, want) {
		t.Fatalf("tags after backfill = %#v, want %#v", got.Tags, want)
	}
}

func TestReportTagBackfill(t *testing.T) {
	cases := []struct {
		n    int
		want string
	}{
		{0, ""},
		{1, "bermuda: normalised tags on 1 job\n"},
		{4, "bermuda: normalised tags on 4 jobs\n"},
	}
	for _, c := range cases {
		var buf bytes.Buffer
		reportTagBackfill(&buf, c.n)
		if buf.String() != c.want {
			t.Errorf("reportTagBackfill(%d) = %q, want %q", c.n, buf.String(), c.want)
		}
	}
}
