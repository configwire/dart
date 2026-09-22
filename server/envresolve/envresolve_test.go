package envresolve

import (
	"errors"
	"testing"
)

func TestSlugTakenDeniesDuplicateInSameProject(t *testing.T) {
	rows := []EnvRow{
		{ID: "envA", Slug: "dev", Project: "projA"},
		{ID: "envB", Slug: "dev", Project: "projB"},
	}
	if !SlugTaken(rows, "projA", "dev", "") {
		t.Fatal("expected duplicate slug in the same project to be taken")
	}
}

func TestSlugTakenAllowsSameSlugAcrossProjects(t *testing.T) {
	rows := []EnvRow{
		{ID: "envA", Slug: "dev", Project: "projA"},
	}
	if SlugTaken(rows, "projB", "dev", "") {
		t.Fatal("expected same slug under a different project to be allowed")
	}
}

func TestSlugTakenAllowsFirstUse(t *testing.T) {
	if SlugTaken(nil, "projA", "dev", "") {
		t.Fatal("expected empty table to allow any slug")
	}
}

func TestSlugTakenExcludesSelfOnUpdate(t *testing.T) {
	rows := []EnvRow{
		{ID: "envA", Slug: "dev", Project: "projA"},
	}
	// Updating envA without changing slug/project must not self-collide.
	if SlugTaken(rows, "projA", "dev", "envA") {
		t.Fatal("expected self row to be excluded from the collision check")
	}
	// ...but another row with the same (project, slug) still collides.
	rows = append(rows, EnvRow{ID: "envC", Slug: "dev", Project: "projA"})
	if !SlugTaken(rows, "projA", "dev", "envA") {
		t.Fatal("expected a second row with the same (project, slug) to collide")
	}
}

func TestSlugTakenIgnoresEmptyProjectOrSlug(t *testing.T) {
	rows := []EnvRow{{ID: "envA", Slug: "dev", Project: "projA"}}
	if SlugTaken(rows, "", "dev", "") {
		t.Fatal("expected empty project to never count as taken")
	}
	if SlugTaken(rows, "projA", "", "") {
		t.Fatal("expected empty slug to never count as taken")
	}
}

func TestPickIndexUnambiguous(t *testing.T) {
	idx, err := PickIndex("dev", []string{"projA"}, "")
	if err != nil || idx != 0 {
		t.Fatalf("expected index 0, got %d, err %v", idx, err)
	}
}

func TestPickIndexEmptyIsNotFound(t *testing.T) {
	if _, err := PickIndex("dev", nil, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestPickIndexAmbiguousWithoutQualifier(t *testing.T) {
	_, err := PickIndex("dev", []string{"projA", "projB"}, "")
	var amb AmbiguousError
	if !errors.As(err, &amb) {
		t.Fatalf("expected AmbiguousError, got %v", err)
	}
	if amb.Slug != "dev" || amb.Count != 2 {
		t.Fatalf("expected slug dev count 2, got %+v", amb)
	}
	if got := amb.Error(); got == "" {
		t.Fatal("expected a non-empty collision message")
	}
}

func TestPickIndexQualifierSelectsProject(t *testing.T) {
	idx, err := PickIndex("dev", []string{"projA", "projB"}, "projB")
	if err != nil || idx != 1 {
		t.Fatalf("expected index 1, got %d, err %v", idx, err)
	}
}

func TestPickIndexUnknownQualifierIsNotFound(t *testing.T) {
	if _, err := PickIndex("dev", []string{"projA", "projB"}, "projZ"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestMatchFlagScopesToProject(t *testing.T) {
	rows := []FlagRow{
		{ID: "flagA", Key: "launch", Project: "projA"},
		{ID: "flagB", Key: "launch", Project: "projB"},
	}
	id, ok := MatchFlag(rows, "launch", "projB")
	if !ok || id != "flagB" {
		t.Fatalf("expected flagB, got %q, ok=%v", id, ok)
	}
	id, ok = MatchFlag(rows, "launch", "projA")
	if !ok || id != "flagA" {
		t.Fatalf("expected flagA, got %q, ok=%v", id, ok)
	}
}

func TestMatchFlagUnknownKeyOrProject(t *testing.T) {
	rows := []FlagRow{{ID: "flagA", Key: "launch", Project: "projA"}}
	if _, ok := MatchFlag(rows, "missing", "projA"); ok {
		t.Fatal("expected unknown key to miss")
	}
	if _, ok := MatchFlag(rows, "launch", "projZ"); ok {
		t.Fatal("expected foreign project to miss")
	}
	if _, ok := MatchFlag(rows, "", "projA"); ok {
		t.Fatal("expected empty key to miss")
	}
}
