package migrations

// ConfigWire retention rollups (plan todo 15, ADDITIVE only).
//
// Adds the event_daily collection that the server/purge job upserts into
// BEFORE deleting raw events older than 30d. Daily aggregates live to 90d.
//
// Shape: event_daily{day Date, env relation (required), flag relation
// NULLABLE (raw rows with unknown flag keys roll up with flag unset),
// variant text, fetches int, exposures int}. Upsert key is
// (day, env, flag, variant) — enforced in Go code (purge.go), not by a
// unique DB index.
//
// Rules: nil-deny like T5 (nil = deny non-superusers; "" would be public).
// Down drops event_daily ONLY and never touches the T5 collections.

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

const colEventDaily = "cw_event_daily"

func init() {
	m.Register(func(app core.App) error {
		daily := core.NewBaseCollection("event_daily", colEventDaily)
		daily.ListRule = nil
		daily.ViewRule = nil
		daily.CreateRule = nil
		daily.UpdateRule = nil
		daily.DeleteRule = nil
		daily.Fields.Add(
			&core.DateField{Name: "day", Required: true},
			&core.RelationField{
				Name:         "env",
				CollectionId: colEnvironments,
				MaxSelect:    1,
				Required:     true,
			},
			&core.RelationField{
				Name:         "flag",
				CollectionId: colFlags,
				MaxSelect:    1,
				Required:     false,
			},
			&core.TextField{Name: "variant"},
			&core.NumberField{Name: "fetches", OnlyInt: true},
			&core.NumberField{Name: "exposures", OnlyInt: true},
		)
		return app.Save(daily)
	}, func(app core.App) error {
		collection, err := app.FindCollectionByNameOrId("event_daily")
		if err != nil {
			return nil
		}
		return app.Delete(collection)
	})
}
