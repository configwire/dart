package migrations

// ConfigWire stored-id rename (zero-residue rebrand follow-up).
//
// Renames every cn_* collection id to cw_* (projects, environments,
// groups, flags, rules, releases, sdk_keys, events, experiments,
// event_daily) AND remaps all relation CollectionId references so no
// relation dangles. Collection NAMES are frozen; only ids move.
//
// Mechanism: TRUE RENAME via a single UPDATE per collection, not
// copy-to-new + delete-old. The app-layer Save explicitly forbids
// primary-key change (PocketBase v0.40.4 core/db.go: "primary key
// change is not allowed"), but the storage layer is safe for a direct
// UPDATE: _collections.id has no foreign keys (PRAGMA
// foreign_key_list is empty) and record tables are keyed by collection
// NAME (CREATE TABLE `projects`, not `cn_projects`), so no record row
// moves. A copy-based migration would risk record loss for zero
// benefit and was rejected.
//
// Both directions are idempotent: collections already on the target
// side (fresh DBs created by the renamed init consts) or absent are
// skipped. Filenames of older migrations are untouched (PocketBase
// tracks applied migrations by filename).

import (
	"encoding/json"

	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

// Old -> new stored collection ids (self-contained literals so the
// down path stays correct even if the col* consts move again).
var configwireIdRenames = [][2]string{
	{"cn_projects", "cw_projects"},
	{"cn_environments", "cw_environments"},
	{"cn_groups", "cw_groups"},
	{"cn_flags", "cw_flags"},
	{"cn_rules", "cw_rules"},
	{"cn_releases", "cw_releases"},
	{"cn_sdk_keys", "cw_sdk_keys"},
	{"cn_events", "cw_events"},
	{"cn_experiments", "cw_experiments"},
	{"cn_event_daily", "cw_event_daily"},
}

// Collection names covered by the rename (display names are frozen).
var configwireRenamedNames = []string{
	"projects",
	"environments",
	"groups",
	"flags",
	"rules",
	"releases",
	"sdk_keys",
	"events",
	"experiments",
	"event_daily",
}

// renameConfigwireIds applies pairs (each [2]string{from, to}) to the
// stored ids and to every relation CollectionId reference. Src-side
// ids that are already on the target side, or collections that are
// absent, are skipped.
func renameConfigwireIds(app core.App, pairs [][2]string) error {
	forward := make(map[string]string, len(pairs))
	for _, p := range pairs {
		forward[p[0]] = p[1]
	}
	for _, name := range configwireRenamedNames {
		collection, err := app.FindCollectionByNameOrId(name)
		if err != nil {
			continue // absent; keep the migration idempotent
		}
		target := collection.Id
		if next, ok := forward[collection.Id]; ok {
			target = next
		}
		fieldsChanged := false
		for _, f := range collection.Fields {
			rel, ok := f.(*core.RelationField)
			if !ok {
				continue
			}
			if next, ok := forward[rel.CollectionId]; ok && next != rel.CollectionId {
				rel.CollectionId = next
				fieldsChanged = true
			}
		}
		if target == collection.Id && !fieldsChanged {
			continue // already renamed; no-op
		}
		rawFields, err := json.Marshal(collection.Fields)
		if err != nil {
			return err
		}
		current := collection.Id
		// Raw UPDATE (not app.Save): the model layer rejects primary
		// key change, while _collections.id carries no foreign keys.
		// map[string]any satisfies dbx.Params without a dbx import.
		_, err = app.NonconcurrentDB().NewQuery(
			"UPDATE _collections SET id = {:newId}, fields = {:fields} WHERE id = {:oldId}",
		).Bind(map[string]any{
			"newId":  target,
			"fields": string(rawFields),
			"oldId":  current,
		}).Execute()
		if err != nil {
			return err
		}
	}
	// Raw SQL bypasses the model cache; reload so the rest of the
	// process (later migrations, serve boot) sees the new ids.
	return app.ReloadCachedCollections()
}

func reverseConfigwireIdRenames(pairs [][2]string) [][2]string {
	out := make([][2]string, len(pairs))
	for i, p := range pairs {
		out[i] = [2]string{p[1], p[0]}
	}
	return out
}

func init() {
	m.Register(func(app core.App) error {
		return renameConfigwireIds(app, configwireIdRenames)
	}, func(app core.App) error {
		return renameConfigwireIds(app, reverseConfigwireIdRenames(configwireIdRenames))
	})
}
