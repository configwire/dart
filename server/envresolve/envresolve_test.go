package envresolve

import "testing"

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
