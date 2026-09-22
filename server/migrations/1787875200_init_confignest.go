package migrations

// ConfigNest data foundation (todo 5).
//
// Defines the 9 base collections every later todo builds on:
// projects, environments, groups, flags, rules, releases,
// sdk_keys, events, experiments.
//
// Notes:
//   - All API rules default-deny (nil); server code uses e.App /
//     superuser context which bypasses rules.
//   - releases rows are immutable enforced by hooks in main.go
//     (OnRecordValidate + OnRecordUpdate deny any update;
//     rollback creates new rows — todo 8).
//   - flags.key shape + 1000/project cap enforced by hooks in main.go.
//   - experiments.variants JSON shape follows the T3 contract
//     (server/eval/eval.go): [{name, weightBps (sum 10000), values}].
//   - events carries userHash ONLY (no raw user id, no network address).

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

func init() {
	m.Register(func(app core.App) error {
		return createConfignestCollections(app)
	}, func(app core.App) error {
		// Reverse dependency order so relation targets drop last.
		for _, name := range []string{
			"experiments",
			"events",
			"sdk_keys",
			"releases",
			"rules",
			"flags",
			"groups",
			"environments",
			"projects",
		} {
			collection, err := app.FindCollectionByNameOrId(name)
			if err != nil {
				continue // already gone; keep down idempotent
			}
			if err := app.Delete(collection); err != nil {
				return err
			}
		}
		return nil
	})
}

// Stable collection ids so relation fields can reference targets.
const (
	colProjects     = "cn_projects"
	colEnvironments = "cn_environments"
	colGroups       = "cn_groups"
	colFlags        = "cn_flags"
	colRules        = "cn_rules"
	colReleases     = "cn_releases"
	colSDKKeys      = "cn_sdk_keys"
	colEvents       = "cn_events"
	colExperiments  = "cn_experiments"
)

func denyAllRules(c *core.Collection) {
	// PocketBase v0.40.4 semantics (apis/record_crud.go): a nil rule
	// denies non-superusers with 403 ("Only superusers can perform
	// this action"), while "" (non-nil empty) applies NO filter and
	// leaves the endpoint PUBLIC. Default-deny therefore means nil.
	c.ListRule = nil
	c.ViewRule = nil
	c.CreateRule = nil
	c.UpdateRule = nil
	c.DeleteRule = nil
}

func newConfignestCollection(name, id string) *core.Collection {
	c := core.NewBaseCollection(name, id)
	denyAllRules(c)
	return c
}

func relation(name, target string, required bool) *core.RelationField {
	return &core.RelationField{
		Name:         name,
		CollectionId: target,
		MaxSelect:    1,
		Required:     required,
	}
}

func createConfignestCollections(app core.App) error {
	projects := newConfignestCollection("projects", colProjects)
	projects.Fields.Add(
		&core.TextField{Name: "owner"},
		&core.TextField{Name: "name", Required: true, Presentable: true},
	)

	environments := newConfignestCollection("environments", colEnvironments)
	environments.Fields.Add(
		relation("project", colProjects, true),
		&core.TextField{Name: "slug", Required: true, Presentable: true},
		&core.TextField{Name: "sdkKeyPrefix"},
	)

	groups := newConfignestCollection("groups", colGroups)
	groups.Fields.Add(
		&core.TextField{Name: "name", Required: true, Presentable: true},
		relation("project", colProjects, true),
	)

	flags := newConfignestCollection("flags", colFlags)
	flags.Fields.Add(
		&core.TextField{
			Name:     "key",
			Required: true,
			Min:      1,
			Max:      128,
			Pattern:  `^[A-Za-z_][A-Za-z0-9_]*$`,
		},
		&core.SelectField{
			Name:      "type",
			Values:    []string{"number", "string", "bool", "json"},
			MaxSelect: 1,
			Required:  true,
		},
		relation("group", colGroups, false),
		&core.JSONField{Name: "defaultValue"},
		relation("project", colProjects, true),
	)

	rules := newConfignestCollection("rules", colRules)
	rules.Fields.Add(
		relation("flag", colFlags, true),
		&core.NumberField{Name: "priority", OnlyInt: true},
		&core.JSONField{Name: "condition", Required: true},
		&core.JSONField{Name: "value"},
	)

	releases := newConfignestCollection("releases", colReleases)
	releases.Fields.Add(
		&core.NumberField{Name: "version", OnlyInt: true, Required: true},
		&core.TextField{Name: "etag", Required: true},
		&core.JSONField{Name: "snapshot", Required: true},
		&core.TextField{Name: "author"},
		&core.TextField{Name: "note"},
		relation("env", colEnvironments, true),
	)

	sdkKeys := newConfignestCollection("sdk_keys", colSDKKeys)
	sdkKeys.Fields.Add(
		&core.TextField{Name: "prefix", Required: true},
		&core.TextField{Name: "hash", Required: true},
		relation("env", colEnvironments, true),
		&core.BoolField{Name: "revoked"},
		&core.NumberField{Name: "rateLimit", OnlyInt: true},
	)

	events := newConfignestCollection("events", colEvents)
	events.Fields.Add(
		relation("env", colEnvironments, false),
		relation("flag", colFlags, false),
		&core.TextField{Name: "variant"},
		&core.SelectField{
			Name:      "kind",
			Values:    []string{"fetch", "exposure"},
			MaxSelect: 1,
		},
		&core.TextField{Name: "userHash"},
		&core.DateField{Name: "ts"},
	)

	experiments := newConfignestCollection("experiments", colExperiments)
	experiments.Fields.Add(
		&core.TextField{Name: "name", Required: true, Presentable: true},
		relation("flag", colFlags, false),
		&core.TextField{Name: "seed", Required: true},
		&core.JSONField{
			Name:     "variants",
			Required: true,
			Help:     `T3 contract shape: [{name, weightBps (sum 10000), values}]`,
		},
		&core.SelectField{
			Name:      "status",
			Values:    []string{"draft", "running", "stopped"},
			MaxSelect: 1,
			Required:  true,
		},
	)

	for _, c := range []*core.Collection{
		projects,
		environments,
		groups,
		flags,
		rules,
		releases,
		sdkKeys,
		events,
		experiments,
	} {
		if err := app.Save(c); err != nil {
			return err
		}
	}
	return nil
}
