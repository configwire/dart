package main

// SPIKE — route skeleton candidate, production stream shape.
// GET /stream uses real SDK-key auth (ingest.RequireSDKKey reuse) and holds
// a long-lived SSE connection with `: ping` keepalives; freshness beyond
// push is covered by the documented 15min poll fallback. POST /spike/publish
// is a QA-only hook (see below), never production.

import (
	"net/http"
	"time"

	"configwire/envresolve"
	"configwire/ingest"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/subscriptions"
)

// spikeSDKKey is the hardcoded QA key for the spike publish hook ONLY.
// Never production: GET /stream authenticates via ingest.RequireSDKKey.
const spikeSDKKey = "spike"

// NOTE: the spike GET /api/v1/env/{env}/config was REMOVED here —
// the real SDK delivery endpoint (server/fetch) owns that path now. Spike
// /stream + /spike/publish stay for T14.
func registerSpikeRoutes(se *core.ServeEvent) {
	se.Router.GET("/api/v1/env/{env}/stream", spikeStream)
	se.Router.POST("/api/v1/spike/publish/{clientId}", spikePublish)
}

func spikeCheckKey(re *core.RequestEvent) error {
	if re.Request.Header.Get(ingest.HeaderKey) != spikeSDKKey {
		return re.UnauthorizedError("Missing or invalid SDK key.", nil)
	}
	return nil
}

// Order (same as fetch/ingest): 401 (key) -> 404 (env) / 400 (ambiguous
// slug) -> 401 (env scope).
// Emits NO canned config_update event (avoids stale-event churn; freshness
// is covered by the documented 15min poll fallback).
// Lifecycle: the loop runs inline on the request goroutine — no per-
// connection goroutine is spawned, the ticker is stopped via defer, and
// every return path ends the handler, so nothing leaks after close.
func spikeStream(re *core.RequestEvent) error {
	key, err := ingest.RequireSDKKey(re)
	if err != nil {
		return err
	}
	slug := re.Request.PathValue("env")
	// Deterministic: the key's env IS the env, so a slug shared by
	// several projects can never misroute here.
	if _, err := envresolve.ResolveForKey(re.App, slug, key.GetString("env")); err != nil {
		return envresolve.ToRequestError(re, err)
	}
	re.Response.Header().Set("Content-Type", "text/event-stream")
	re.Response.Header().Set("Cache-Control", "no-store")
	re.Response.Header().Set("X-Accel-Buffering", "no")
	if err := re.Flush(); err != nil {
		return err
	}
	const keepaliveInterval = 20 * time.Second
	const maxStreamDuration = 10 * time.Minute
	ticker := time.NewTicker(keepaliveInterval)
	defer ticker.Stop()
	deadline := time.NewTimer(maxStreamDuration)
	defer deadline.Stop()
	ctx := re.Request.Context()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-deadline.C:
			return nil
		case <-ticker.C:
			if _, err := re.Response.Write([]byte(": ping\n\n")); err != nil {
				return nil
			}
			if err := re.Flush(); err != nil {
				return nil
			}
		}
	}
}

// QA-ONLY hook, never production. Still gated by the hardcoded spike key
// (not SDK keys). Publish-triggered fan-out to /stream is out of scope.
func spikePublish(re *core.RequestEvent) error {
	if err := spikeCheckKey(re); err != nil {
		return err
	}
	clientID := re.Request.PathValue("clientId")
	client, err := re.App.SubscriptionsBroker().ClientById(clientID)
	if err != nil {
		return re.NotFoundError("Missing or invalid client id.", err)
	}
	env := re.Request.URL.Query().Get("env")
	if env == "" {
		env = "dev"
	}
	topic := "config_" + env
	if !client.HasSubscription(topic) {
		return re.BadRequestError("Client is not subscribed to "+topic+".", nil)
	}
	client.Send(subscriptions.Message{
		Name: "config_update",
		Data: []byte(`{"version":1,"etag":"spike-etag-1"}`),
	})
	return re.JSON(http.StatusOK, map[string]any{"delivered": true, "topic": topic})
}
