package store

import (
	"context"
	"reflect"
	"testing"
)

func TestSplitTagsSanitizes(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "messy hand-typed list",
			in:   "  Marketing , marketing,, daily  ,DAILY",
			want: []string{"marketing", "daily"},
		},
		{
			name: "case-different duplicates collapse",
			in:   "Daily,DAILY,daily,dAiLy",
			want: []string{"daily"},
		},
		{
			name: "all empty",
			in:   ",,  ,",
			want: nil,
		},
		{
			name: "internal whitespace collapses",
			in:   "deal  pipeline,deal\tpipeline",
			want: []string{"deal pipeline"},
		},
		{
			name: "first-seen order preserved",
			in:   "zeta,alpha,ZETA,mid",
			want: []string{"zeta", "alpha", "mid"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := SplitTags(c.in)
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("SplitTags(%q) = %#v, want %#v", c.in, got, c.want)
			}
		})
	}
}

func TestSanitizeTagsIdempotent(t *testing.T) {
	once := SplitTags("  Marketing , marketing,, daily  ,DAILY, deal  pipeline")
	twice := SanitizeTags(once)
	if !reflect.DeepEqual(once, twice) {
		t.Fatalf("not idempotent: %#v then %#v", once, twice)
	}
}

func TestPutJobNormalizesTagsThroughStorage(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	j := Job{
		ID:    "j1",
		Name:  "tagged",
		Tags:  []string{"  Marketing ", "marketing", "", " daily ", "DAILY", "deal  pipeline"},
		Model: DefaultModel,
	}
	if err := s.PutJob(ctx, j); err != nil {
		t.Fatal(err)
	}
	got, err := s.Job(ctx, "j1")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"marketing", "daily", "deal pipeline"}
	if !reflect.DeepEqual(got.Tags, want) {
		t.Fatalf("stored tags = %#v, want %#v", got.Tags, want)
	}
}

func TestOpenBackfillsStoredTags(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.PutJob(ctx, Job{ID: "j1", Name: "a", Model: DefaultModel}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutJob(ctx, Job{ID: "j2", Name: "b", Tags: []string{"clean"}, Model: DefaultModel}); err != nil {
		t.Fatal(err)
	}
	// Write a pre-sanitisation row the way an older binary would have.
	if _, err := s.db.ExecContext(ctx, `UPDATE jobs SET tags=? WHERE id=?`, "Marketing,marketing,, daily ,DAILY", "j1"); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if s2.TagsNormalized != 1 {
		t.Fatalf("TagsNormalized = %d, want 1", s2.TagsNormalized)
	}
	got, err := s2.Job(ctx, "j1")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"marketing", "daily"}
	if !reflect.DeepEqual(got.Tags, want) {
		t.Fatalf("backfilled tags = %#v, want %#v", got.Tags, want)
	}

	// A second open must find nothing left to do.
	s2.Close()
	s3, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s3.Close()
	if s3.TagsNormalized != 0 {
		t.Fatalf("second open normalized %d rows, want 0", s3.TagsNormalized)
	}
}

// A tag matches in the shape it was stored in.
//
// SanitizeTags collapses runs of whitespace on the way in; HasTag only trimmed
// the ends, so a job tagged "release notes" could not be found by a filter
// typed as "release  notes" — the two spaces the store had already removed.
func TestHasTagMatchesTheShapeTagsAreStoredIn(t *testing.T) {
	j := Job{Tags: SanitizeTags([]string{"Release  Notes", "ci"})}
	for _, query := range []string{"release notes", "Release  Notes", " release   notes ", "CI"} {
		if !j.HasTag(query) {
			t.Errorf("HasTag(%q) = false, want true (stored as %q)", query, j.Tags)
		}
	}
	if j.HasTag("releasenotes") {
		t.Error("HasTag ignored the space entirely, which would match a different tag")
	}
}
