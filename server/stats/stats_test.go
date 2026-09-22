package stats

import (
	"testing"
	"time"
)

// fakeRows builds n fetch rows + per-variant exposure rows for one env,
// all stamped at now (inside any sane cutoff).
func fakeRows(t *testing.T, env, flag string, fetches int, variants map[string]int) []EventRow {
	t.Helper()
	now := time.Now().UTC()
	var rows []EventRow
	for i := 0; i < fetches; i++ {
		rows = append(rows, EventRow{EnvID: env, FlagID: flag, Kind: "fetch", Ts: now})
	}
	for variant, n := range variants {
		for i := 0; i < n; i++ {
			rows = append(rows, EventRow{EnvID: env, FlagID: flag, Kind: "exposure", Variant: variant, Ts: now})
		}
	}
	return rows
}

func TestParseSince(t *testing.T) {
	cases := []struct {
		in   string
		want int
		ok   bool
	}{
		{"", DefaultSinceDays, true}, // absent -> 7d default
		{"7d", 7, true},
		{"1d", 1, true},
		{"90d", 90, true},
		{"400d", 90, true}, // clamp, not error
		{"abc", 0, false},
		{"-5d", 0, false},
		{"0d", 0, false},
		{"7", 0, false},  // missing d suffix
		{"7h", 0, false}, // wrong unit
		{"d", 0, false},
		{"  ", DefaultSinceDays, true}, // blank -> default
	}
	for _, c := range cases {
		got, err := ParseSince(c.in)
		if c.ok && err != nil {
			t.Errorf("ParseSince(%q): unexpected error %v", c.in, err)
		}
		if !c.ok && err == nil {
			t.Errorf("ParseSince(%q): expected error, got %d", c.in, got)
		}
		if c.ok && got != c.want {
			t.Errorf("ParseSince(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestAggregateCountsAndSplit(t *testing.T) {
	rows := fakeRows(t, "env1", "flag1", 100, map[string]int{"control": 60, "treatment": 40})
	st := Aggregate(rows, "env1", "flag1", true, time.Now().UTC().Add(-7*24*time.Hour))
	if st.Fetches != 100 {
		t.Errorf("fetches = %d, want 100", st.Fetches)
	}
	if st.Exposures != 100 {
		t.Errorf("exposures = %d, want 100", st.Exposures)
	}
	if st.PerVariant["control"] != 60 || st.PerVariant["treatment"] != 40 {
		t.Errorf("perVariant = %v, want control:60 treatment:40", st.PerVariant)
	}
	if len(st.PerVariant) != 2 {
		t.Errorf("perVariant has %d keys, want exactly 2: %v", len(st.PerVariant), st.PerVariant)
	}
}

func TestAggregateSinceFilter(t *testing.T) {
	now := time.Now().UTC()
	rows := []EventRow{
		{EnvID: "e", FlagID: "f", Kind: "fetch", Ts: now},
		{EnvID: "e", FlagID: "f", Kind: "exposure", Variant: "control", Ts: now},
		{EnvID: "e", FlagID: "f", Kind: "fetch", Ts: now.Add(-100 * 24 * time.Hour)},                          // too old
		{EnvID: "e", FlagID: "f", Kind: "exposure", Variant: "treatment", Ts: now.Add(-100 * 24 * time.Hour)}, // too old
	}
	st := Aggregate(rows, "e", "f", true, now.Add(-90*24*time.Hour)) // clamped 90d window
	if st.Fetches != 1 || st.Exposures != 1 {
		t.Errorf("got fetches=%d exposures=%d, want 1/1 (100d-old rows excluded)", st.Fetches, st.Exposures)
	}
	if st.PerVariant["control"] != 1 {
		t.Errorf("perVariant = %v, want control:1 only", st.PerVariant)
	}
	if _, ok := st.PerVariant["treatment"]; ok {
		t.Errorf("stale treatment leaked into perVariant: %v", st.PerVariant)
	}
}

func TestAggregateEnvAndFlagIsolation(t *testing.T) {
	now := time.Now().UTC()
	rows := []EventRow{
		{EnvID: "e1", FlagID: "f1", Kind: "fetch", Ts: now},
		{EnvID: "e1", FlagID: "f1", Kind: "exposure", Variant: "control", Ts: now},
		{EnvID: "e2", FlagID: "f1", Kind: "fetch", Ts: now},                        // other env
		{EnvID: "e1", FlagID: "f2", Kind: "exposure", Variant: "control", Ts: now}, // other flag
		{EnvID: "e1", FlagID: "", Kind: "exposure", Variant: "control", Ts: now},   // unset relation
	}
	cutoff := now.Add(-7 * 24 * time.Hour)

	st := Aggregate(rows, "e1", "f1", true, cutoff)
	if st.Fetches != 1 || st.Exposures != 1 {
		t.Errorf("flag-filtered: got %d/%d, want 1/1", st.Fetches, st.Exposures)
	}

	// Unknown flag id matches nothing -> zeros (handler returns these 200).
	st = Aggregate(rows, "e1", "no-such-flag", true, cutoff)
	if st.Fetches != 0 || st.Exposures != 0 || len(st.PerVariant) != 0 {
		t.Errorf("unknown flag: got %+v, want zeros", st)
	}

	// No flag filter -> all flags in the env (incl. unset-relation rows).
	st = Aggregate(rows, "e1", "", false, cutoff)
	if st.Fetches != 1 || st.Exposures != 3 {
		t.Errorf("unfiltered: got %d/%d, want 1/3", st.Fetches, st.Exposures)
	}
}

func TestAggregateIgnoresUnknownKindsAndZeroTs(t *testing.T) {
	now := time.Now().UTC()
	rows := []EventRow{
		{EnvID: "e", FlagID: "f", Kind: "fetch", Ts: now},
		{EnvID: "e", FlagID: "f", Kind: "weird", Variant: "control", Ts: now}, // unknown kind
		{EnvID: "e", FlagID: "f", Kind: "exposure", Variant: "control"},       // zero ts
	}
	st := Aggregate(rows, "e", "f", true, now.Add(-7*24*time.Hour))
	if st.Fetches != 1 || st.Exposures != 0 || len(st.PerVariant) != 0 {
		t.Errorf("got %+v, want {1 0 {}}", st)
	}
}
