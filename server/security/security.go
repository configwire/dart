// Package security sets static response headers with no behavior change.
// Ownership: this helper is called ONLY from the request paths owned by
// the releases, stats, and fetch packages (their files). server/main.go
// wiring is owned by server/main.go — never add calls there.
//
// Headers (static values, no behavior change):
//   - X-Content-Type-Options: nosniff (block MIME sniffing)
//   - X-Frame-Options: DENY (no clickjacking host)
//   - Referrer-Policy: no-referrer (never leak URLs/keys on navigation)
package security

import (
	"github.com/pocketbase/pocketbase/core"
)

// SetHeaders stamps static security headers. Call at the top of a handler, before any body write.
func SetHeaders(re *core.RequestEvent) {
	h := re.Response.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
}
