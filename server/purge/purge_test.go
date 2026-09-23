package purge

import (
	"testing"
	"time"
)

func TestShouldDeleteCutoffEdges(t *testing.T) {
	cutoff := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		ts   time.Time
		want bool
	}{
		{"40d old deleted", cutoff.Add(-10 * 24 * time.Hour), true},
		{"1ns before cutoff deleted", cutoff.Add(-time.Nanosecond), true},
		{"exactly at cutoff KEPT", cutoff, false},
		{"1ns after cutoff kept", cutoff.Add(time.Nanosecond), false},
		{"fresh kept", time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC), false},
		{"zero ts KEPT (no day bucket)", time.Time{}, false},
	}
	for _, c := range cases {
		if got := ShouldDelete(c.ts, cutoff); got != c.want {
			t.Errorf("ShouldDelete(%v) = %v, want %v (%s)", c.ts, got, c.want, c.name)
		}
	}
}

func TestDayBucketTruncatesToUTCMidnight(t *testing.T) {
	in := time.Date(2026, 8, 13, 23, 59, 59, 999999999, time.UTC)
	want := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	if got := DayBucket(in); !got.Equal(want) {
		t.Errorf("DayBucket(%v) = %v, want %v", in, got, want)
	}
	// Non-UTC input lands on its UTC calendar day.
	plus9 := time.Date(2026, 8, 14, 1, 30, 0, 0, time.FixedZone("JST", 9*3600))
	if got := DayBucket(plus9); !got.Equal(want) {
		t.Errorf("DayBucket(%v) = %v, want %v (UTC day)", plus9, got, want)
	}
}

func TestBuildRollupsMath(t *testing.T) {
	dayA := time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC)
	dayB := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	rows := []EventRow{
		{EnvID: "e1", FlagID: "f1", Kind: "fetch", Ts: dayA},
		{EnvID: "e1", FlagID: "f1", Kind: "fetch", Ts: dayA.Add(time.Hour)},
		{EnvID: "e1", FlagID: "f1", Kind: "exposure", Variant: "control", Ts: dayA},
		{EnvID: "e1", FlagID: "f1", Kind: "exposure", Variant: "control", Ts: dayA.Add(2 * time.Hour)},
		{EnvID: "e1", FlagID: "f1", Kind: "exposure", Variant: "treatment", Ts: dayA},
		{EnvID: "e1", FlagID: "f1", Kind: "fetch", Ts: dayB},
		{EnvID: "e2", FlagID: "f1", Kind: "fetch", Ts: dayA},
		{EnvID: "e1", FlagID: "", Kind: "exposure", Variant: "control", Ts: dayA},
		{EnvID: "e1", FlagID: "f1", Kind: "weird", Variant: "control", Ts: dayA}, // unknown kind ignored
		{EnvID: "e1", FlagID: "f1", Kind: "exposure", Variant: "control"},        // zero ts skipped
	}
	got := BuildRollups(rows)
	byKey := map[string]Rollup{}
	for _, b := range got {
		byKey[b.Day.Format("2006-01-02")+"|"+b.EnvID+"|"+b.FlagID+"|"+b.Variant] = b
	}
	if len(got) != 6 {
		t.Fatalf("BuildRollups produced %d buckets, want 6: %+v", len(got), got)
	}
	want := map[string]Rollup{
		"2026-08-13|e1|f1|":          {Fetches: 2},
		"2026-08-13|e1|f1|control":   {Exposures: 2},
		"2026-08-13|e1|f1|treatment": {Exposures: 1},
		"2026-08-14|e1|f1|":          {Fetches: 1},
		"2026-08-13|e2|f1|":          {Fetches: 1},
		"2026-08-13|e1||control":     {Exposures: 1},
	}
	for k, w := range want {
		b, ok := byKey[k]
		if !ok {
			t.Errorf("missing bucket %q", k)
			continue
		}
		if b.Fetches != w.Fetches || b.Exposures != w.Exposures {
			t.Errorf("bucket %q = fetches=%d exposures=%d, want %d/%d",
				k, b.Fetches, b.Exposures, w.Fetches, w.Exposures)
		}
		if !b.Day.Equal(DayBucket(b.Day)) {
			t.Errorf("bucket %q day %v is not UTC midnight", k, b.Day)
		}
	}
}

func TestBuildRollupsEmpty(t *testing.T) {
	if got := BuildRollups(nil); len(got) != 0 {
		t.Errorf("BuildRollups(nil) = %+v, want empty", got)
	}
}
