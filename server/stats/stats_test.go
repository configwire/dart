package stats

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"configwire/purge"
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
		{EnvID: "e", FlagID: "f", Kind: "fetch", Ts: now.Add(-100 * 24 * time.Hour)},
		{EnvID: "e", FlagID: "f", Kind: "exposure", Variant: "treatment", Ts: now.Add(-100 * 24 * time.Hour)},
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
		{EnvID: "e", FlagID: "f", Kind: "weird", Variant: "control", Ts: now},
		{EnvID: "e", FlagID: "f", Kind: "exposure", Variant: "control"},
	}
	st := Aggregate(rows, "e", "f", true, now.Add(-7*24*time.Hour))
	if st.Fetches != 1 || st.Exposures != 0 || len(st.PerVariant) != 0 {
		t.Errorf("got %+v, want {1 0 {}}", st)
	}
}

func TestEchoCutoffMath(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	cutoff7 := now.Add(-7 * 24 * time.Hour).Truncate(time.Second)
	echo := EchoFor("dev", "launch_flag", 7, cutoff7, purge.DayBucket(now.AddDate(0, 0, -30)))
	if echo["env"] != "dev" || echo["flag"] != "launch_flag" {
		t.Errorf("verbatim echo = %v, want env:dev flag:launch_flag", echo)
	}
	if echo["since"] != "7d" || echo["sinceDays"] != 7 || echo["horizon"] != "7d" {
		t.Errorf("7d echo = %v, want since:7d sinceDays:7 horizon:7d", echo)
	}
	gotHorizon, err := time.Parse(time.RFC3339, echo["rollupHorizon"].(string))
	if err != nil {
		t.Fatalf("rollupHorizon %v does not parse as RFC3339: %v", echo["rollupHorizon"], err)
	}
	if gotHorizon.Hour() != 0 || gotHorizon.Minute() != 0 || gotHorizon.Second() != 0 {
		t.Errorf("rollupHorizon = %v, want UTC midnight", gotHorizon)
	}
	gotCutoff, err := time.Parse(time.RFC3339, echo["cutoff"].(string))
	if err != nil {
		t.Fatalf("cutoff %v does not parse as RFC3339: %v", echo["cutoff"], err)
	}
	if gotCutoff.Sub(cutoff7.UTC().Truncate(time.Second)).Abs() > 2*time.Second {
		t.Errorf("cutoff = %v, want ~%v", gotCutoff, cutoff7.UTC())
	}

	// 400d clamps to 90d: ParseSince echoes the EFFECTIVE window.
	days, err := ParseSince("400d")
	if err != nil || days != 90 {
		t.Fatalf("ParseSince(400d) = %d,%v, want 90,nil", days, err)
	}
	clamped := EchoFor("dev", "launch_flag", days, now.Add(-time.Duration(days)*24*time.Hour), purge.DayBucket(now.AddDate(0, 0, -30)))
	if clamped["since"] != "90d" || clamped["sinceDays"] != 90 || clamped["horizon"] != "90d" {
		t.Errorf("400d clamp echo = %v, want since:90d sinceDays:90 horizon:90d", clamped)
	}
}

func TestFlagFoundSemantics(t *testing.T) {
	if !FlagFound("", false) {
		t.Errorf("unfiltered FlagFound(\"\", false) = false, want true")
	}
	if !FlagFound("launch_flag", true) {
		t.Errorf("known flag FlagFound = false, want true")
	}
	if FlagFound("no-such", false) {
		t.Errorf("unknown flag FlagFound = true, want false")
	}
}

func TestUnknownFlagZeros(t *testing.T) {
	now := time.Now().UTC()
	rows := []EventRow{
		{EnvID: "e1", FlagID: "f1", Kind: "fetch", Ts: now},
		{EnvID: "e1", FlagID: "f1", Kind: "exposure", Variant: "control", Ts: now},
	}
	cutoff := now.Add(-7 * 24 * time.Hour)
	st := Aggregate(rows, "e1", "no-such-flag", true, cutoff)
	if st.Fetches != 0 || st.Exposures != 0 || len(st.PerVariant) != 0 {
		t.Errorf("unknown flag: got %+v, want zeros", st)
	}
	if TotalFor(st) != 0 {
		t.Errorf("unknown flag total = %d, want 0", TotalFor(st))
	}
	if rates := RatesFor(st); len(rates) != 0 {
		t.Errorf("unknown flag rates = %v, want {}", rates)
	}
	if FlagFound("no-such-flag", false) {
		t.Errorf("unknown flag flagFound = true, want false")
	}
}

func TestUnfilteredFlagFoundParity(t *testing.T) {
	now := time.Now().UTC()
	rows := []EventRow{
		{EnvID: "e1", FlagID: "f1", Kind: "fetch", Ts: now},
		{EnvID: "e1", FlagID: "f1", Kind: "exposure", Variant: "control", Ts: now},
		{EnvID: "e2", FlagID: "f1", Kind: "fetch", Ts: now},                        // other env
		{EnvID: "e1", FlagID: "f2", Kind: "exposure", Variant: "control", Ts: now}, // other flag
		{EnvID: "e1", FlagID: "", Kind: "exposure", Variant: "control", Ts: now},   // unset relation
	}
	cutoff := now.Add(-7 * 24 * time.Hour)
	st := Aggregate(rows, "e1", "", false, cutoff)
	if st.Fetches != 1 || st.Exposures != 3 {
		t.Fatalf("unfiltered: got %d/%d, want 1/3", st.Fetches, st.Exposures)
	}
	if !FlagFound("", false) {
		t.Errorf("unfiltered flagFound = false, want true")
	}
	if TotalFor(st) != 4 {
		t.Errorf("unfiltered total = %d, want 4", TotalFor(st))
	}
	rates := RatesFor(st)
	if len(rates) != 1 {
		t.Fatalf("unfiltered rates = %v, want single control entry", rates)
	}
	if got := rates["control"]; got != 1.0 {
		t.Errorf("unfiltered control rate = %v, want 1.0", got)
	}
}

func TestRatesAndTotal(t *testing.T) {
	rows := fakeRows(t, "env1", "flag1", 100, map[string]int{"control": 60, "treatment": 40})
	st := Aggregate(rows, "env1", "flag1", true, time.Now().UTC().Add(-7*24*time.Hour))
	if TotalFor(st) != 200 {
		t.Errorf("total = %d, want 200", TotalFor(st))
	}
	rates := RatesFor(st)
	if rates["control"] != 0.6 || rates["treatment"] != 0.4 {
		t.Errorf("rates = %v, want control:0.6 treatment:0.4", rates)
	}
	// Counts stay exact integers; rates are display-math only.
	if st.Fetches != 100 || st.Exposures != 100 {
		t.Errorf("counts rounded? got %+v, want {100 100}", st)
	}
}

func TestZeroExposureRatesEmpty(t *testing.T) {
	now := time.Now().UTC()
	rows := []EventRow{
		{EnvID: "e", FlagID: "f", Kind: "fetch", Ts: now},
		{EnvID: "e", FlagID: "f", Kind: "fetch", Ts: now},
	}
	st := Aggregate(rows, "e", "f", true, now.Add(-7*24*time.Hour))
	if st.Exposures != 0 {
		t.Fatalf("exposures = %d, want 0", st.Exposures)
	}
	rates := RatesFor(st)
	if rates == nil {
		t.Fatalf("rates is nil, want non-nil {}")
	}
	if len(rates) != 0 {
		t.Errorf("zero-exposure rates = %v, want {}", rates)
	}
	if TotalFor(st) != 2 {
		t.Errorf("total = %d, want 2", TotalFor(st))
	}
}

func TestStatsPathNeverTouchesUserHash(t *testing.T) {
	typ := reflect.TypeOf(EventRow{})
	for i := 0; i < typ.NumField(); i++ {
		if strings.EqualFold(typ.Field(i).Name, "userhash") {
			t.Errorf("EventRow carries %s field; stats path must never read userHash", typ.Field(i).Name)
		}
	}
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	src, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "stats.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{`GetString("userHash")`, `Get("userHash")`, `"userHash":`} {
		if strings.Contains(string(src), banned) {
			t.Errorf("stats.go contains %q; userHash must never be read/echoed", banned)
		}
	}
}

// mergeWindow wires the handler's disjoint window math purely for tests:
// 90d-style cutoff + UTC-midnight horizon, events clamped raw-side,
// rollups day-grained history-side.
func mergeWindow(t *testing.T, days int) (cutoff, cutoffDay, horizon, eventCutoff time.Time) {
	t.Helper()
	now := time.Now().UTC()
	cutoff = now.Add(-time.Duration(days) * 24 * time.Hour)
	horizon = HorizonFor(now)
	cutoffDay = purge.DayBucket(cutoff)
	eventCutoff = EventCutoffFor(cutoff, horizon)
	return cutoff, cutoffDay, horizon, eventCutoff
}

func TestMergeOverlapNoDoubleCount(t *testing.T) {
	// Crash window: raw rows older than the horizon are still present
	// (purge upserted but crashed before delete) AND the matching
	// rollup bucket exists. Disjoint rule counts them once.
	_, cutoffDay, horizon, eventCutoff := mergeWindow(t, 90)
	oldTs := horizon.AddDate(0, 0, -5).Add(12 * time.Hour) // < horizon, same day as bucket
	rows := []EventRow{
		{EnvID: "e1", FlagID: "f1", Kind: "fetch", Ts: oldTs},
		{EnvID: "e1", FlagID: "f1", Kind: "fetch", Ts: oldTs},
		{EnvID: "e1", FlagID: "f1", Kind: "exposure", Variant: "control", Ts: oldTs},
	}
	buckets := []purge.Rollup{
		{Day: purge.DayBucket(oldTs), EnvID: "e1", FlagID: "f1", Variant: "control", Fetches: 2, Exposures: 1},
	}
	stEvents := Aggregate(rows, "e1", "f1", true, eventCutoff)
	if stEvents.Fetches != 0 || stEvents.Exposures != 0 {
		t.Errorf("overlap events raw-side = %+v, want zeros (ts < horizon excluded)", stEvents)
	}
	stRollups := AggregateRollups(buckets, "e1", "f1", true, cutoffDay, horizon)
	merged := MergeStats(stEvents, stRollups)
	if merged.Fetches != 2 || merged.Exposures != 1 {
		t.Errorf("overlap merged = %+v, want {2 1} counted once, not twice", merged)
	}
	if merged.PerVariant["control"] != 1 {
		t.Errorf("overlap perVariant = %v, want control:1", merged.PerVariant)
	}
	if !ApproximateFor(TotalFor(stRollups)) {
		t.Errorf("overlap window with rollup days must be approximate")
	}
}

func TestMergeDayBoundaryCutoff(t *testing.T) {
	// Day grain: bucket exactly on cutoffDay is KEPT, the day before is
	// dropped (strictly-older-than, matching purge ShouldDelete).
	_, cutoffDay, horizon, _ := mergeWindow(t, 90)
	kept := purge.Rollup{Day: cutoffDay, EnvID: "e1", FlagID: "f1", Variant: "control", Fetches: 1, Exposures: 2}
	dropped := purge.Rollup{Day: cutoffDay.AddDate(0, 0, -1), EnvID: "e1", FlagID: "f1", Variant: "control", Fetches: 100, Exposures: 100}
	st := AggregateRollups([]purge.Rollup{kept, dropped}, "e1", "f1", true, cutoffDay, horizon)
	if st.Fetches != 1 || st.Exposures != 2 {
		t.Errorf("day-boundary = %+v, want {1 2} (exactly-at-cutoff kept, older dropped)", st)
	}
	if st.PerVariant["control"] != 2 {
		t.Errorf("day-boundary perVariant = %v, want control:2", st.PerVariant)
	}
}

func TestMergeUnknownFlagZerosBothSources(t *testing.T) {
	now := time.Now().UTC()
	_, cutoffDay, horizon, eventCutoff := mergeWindow(t, 90)
	rows := []EventRow{
		{EnvID: "e1", FlagID: "f1", Kind: "fetch", Ts: now},
		{EnvID: "e1", FlagID: "f1", Kind: "exposure", Variant: "control", Ts: now},
	}
	buckets := []purge.Rollup{
		{Day: horizon.AddDate(0, 0, -5), EnvID: "e1", FlagID: "f1", Variant: "control", Fetches: 7, Exposures: 9},
	}
	stEvents := Aggregate(rows, "e1", "no-such-flag", true, eventCutoff)
	stRollups := AggregateRollups(buckets, "e1", "no-such-flag", true, cutoffDay, horizon)
	merged := MergeStats(stEvents, stRollups)
	if merged.Fetches != 0 || merged.Exposures != 0 || len(merged.PerVariant) != 0 {
		t.Errorf("unknown flag merged = %+v, want zeros across both sources", merged)
	}
	if TotalFor(stRollups) != 0 || ApproximateFor(TotalFor(stRollups)) {
		t.Errorf("unknown flag rollups total = %d, want 0 and exact", TotalFor(stRollups))
	}
}

func TestMergeUnfilteredIncludesUnsetFlagBothSources(t *testing.T) {
	// Parity with Aggregate unfiltered semantics: FlagID=="" buckets
	// count when no ?flag= filter, drop out under any filter.
	now := time.Now().UTC()
	_, cutoffDay, horizon, eventCutoff := mergeWindow(t, 90)
	rows := []EventRow{
		{EnvID: "e1", FlagID: "f1", Kind: "fetch", Ts: now},
		{EnvID: "e1", FlagID: "", Kind: "fetch", Ts: now},
		{EnvID: "e1", FlagID: "", Kind: "exposure", Variant: "", Ts: now},
	}
	buckets := []purge.Rollup{
		{Day: horizon.AddDate(0, 0, -5), EnvID: "e1", FlagID: "f1", Variant: "control", Fetches: 1, Exposures: 1},
		{Day: horizon.AddDate(0, 0, -5), EnvID: "e1", FlagID: "", Variant: "", Fetches: 2, Exposures: 3},
	}
	stEvents := Aggregate(rows, "e1", "", false, eventCutoff)
	stRollups := AggregateRollups(buckets, "e1", "", false, cutoffDay, horizon)
	merged := MergeStats(stEvents, stRollups)
	if merged.Fetches != 5 || merged.Exposures != 5 {
		t.Errorf("unfiltered merged = %+v, want {5 5} incl. FlagID==\"\" both sources", merged)
	}
	if merged.PerVariant[""] != 4 {
		t.Errorf("unfiltered perVariant[\"\"] = %d, want 4 (1 events + 3 rollups)", merged.PerVariant[""])
	}
	if merged.PerVariant["control"] != 1 {
		t.Errorf("unfiltered perVariant[control] = %d, want 1", merged.PerVariant["control"])
	}

	filteredEvents := Aggregate(rows, "e1", "f1", true, eventCutoff)
	filteredRollups := AggregateRollups(buckets, "e1", "f1", true, cutoffDay, horizon)
	filtered := MergeStats(filteredEvents, filteredRollups)
	if filtered.Fetches != 2 || filtered.Exposures != 1 {
		t.Errorf("filtered merged = %+v, want {2 1} (unset-relation drops out)", filtered)
	}
	if _, ok := filtered.PerVariant[""]; ok {
		t.Errorf("filtered perVariant leaks \"\": %v", filtered.PerVariant)
	}
}

func TestMergeStaleRollupExcluded(t *testing.T) {
	// Rollups older than the 90d window die with it: day < cutoffDay
	// excluded even though the collection may still hold the row
	// (purge deletes stale dailies on its own schedule).
	_, cutoffDay, horizon, _ := mergeWindow(t, 90)
	stale := purge.Rollup{Day: cutoffDay.AddDate(0, 0, -1), EnvID: "e1", FlagID: "f1", Variant: "control", Fetches: 50, Exposures: 60}
	fresh := purge.Rollup{Day: horizon.AddDate(0, 0, -1), EnvID: "e1", FlagID: "f1", Variant: "control", Fetches: 3, Exposures: 4}
	st := AggregateRollups([]purge.Rollup{stale, fresh}, "e1", "f1", true, cutoffDay, horizon)
	if st.Fetches != 3 || st.Exposures != 4 {
		t.Errorf("stale exclusion = %+v, want {3 4}", st)
	}
}

func TestMergeShortWindowExcludesRollups(t *testing.T) {
	// ?since=7d: cutoff is newer than the horizon, so no rollup day can
	// satisfy day >= cutoffDay AND day < horizon; events stay exact.
	now := time.Now().UTC()
	cutoff := now.Add(-7 * 24 * time.Hour)
	horizon := HorizonFor(now)
	cutoffDay := purge.DayBucket(cutoff)
	eventCutoff := EventCutoffFor(cutoff, horizon)
	if !eventCutoff.Equal(cutoff) {
		t.Errorf("7d eventCutoff = %v, want untouched cutoff %v", eventCutoff, cutoff)
	}
	rows := []EventRow{{EnvID: "e1", FlagID: "f1", Kind: "fetch", Ts: now}}
	buckets := []purge.Rollup{
		{Day: horizon.AddDate(0, 0, -1), EnvID: "e1", FlagID: "f1", Variant: "control", Fetches: 7, Exposures: 9},
	}
	stEvents := Aggregate(rows, "e1", "f1", true, eventCutoff)
	stRollups := AggregateRollups(buckets, "e1", "f1", true, cutoffDay, horizon)
	if TotalFor(stRollups) != 0 {
		t.Errorf("7d rollups = %+v, want zeros", stRollups)
	}
	if ApproximateFor(TotalFor(stRollups)) {
		t.Errorf("7d events-only window must stay exact (approximate:false)")
	}
	merged := MergeStats(stEvents, stRollups)
	if merged.Fetches != 1 || merged.Exposures != 0 {
		t.Errorf("7d merged = %+v, want {1 0}", merged)
	}
}

func TestHorizonForIsUTCMidnight(t *testing.T) {
	now := time.Now().UTC()
	h := HorizonFor(now)
	if h.Location() != time.UTC {
		t.Errorf("horizon loc = %v, want UTC", h.Location())
	}
	if h.Hour() != 0 || h.Minute() != 0 || h.Second() != 0 || h.Nanosecond() != 0 {
		t.Errorf("horizon = %v, want UTC midnight", h)
	}
	want := purge.DayBucket(now.AddDate(0, 0, -purge.RawRetentionDays))
	if !h.Equal(want) {
		t.Errorf("horizon = %v, want DayBucket(now-30d) = %v", h, want)
	}
}
