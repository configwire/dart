package ingest

import (
	"log"
	"sync"
	"time"

	"github.com/pocketbase/pocketbase/core"
)

// StoredEvent is one validated event ready for persistence. It carries IDs,
// never raw PII (see hash.go).
type StoredEvent struct {
	EnvID    string
	FlagID   string // "" when the flag key matched nothing; relation left unset
	Kind     string
	Variant  string
	UserHash string
	Ts       time.Time
}

// BatcherChannelCap bounds queued events before Enqueue returns false (→ 503), so bursts apply backpressure instead of growing memory.
// FlushEvery bounds ingest lag to ~1s plus write time.
// FlushSize bounds one flush batch so writes stay small.
const (
	BatcherChannelCap = 2048
	FlushEvery        = time.Second
	FlushSize         = 500
)

// Batcher buffers validated events on a channel and persists them from a
// single background goroutine via app.Save (never a per-request synchronous
// transaction — handlers return 202 right after enqueue).
//
// DROP-vs-BLOCK CHOICE (documented per contract): Enqueue is NON-BLOCKING.
// When the buffer is full it returns false and the handler answers 503 so
// the client can retry. Rationale: blocking the handler would tie up HTTP
// workers under burst load (backpressure via 503 is explicit and observable),
// while silently dropping would lose analytics data without a trace. At
// 2048 slots with a 1s/500-row flush, saturation requires a sustained
// >2000 events/s against a stalled SQLite — effectively a down-DB signal.
//
// APP DISCIPLINE: handlers use re.App for request-scoped lookups; the
// background flusher has no request scope, so it uses the process app
// captured at Register (se.App). Concurrent Save from one goroutine is the
// same load pattern as concurrent HTTP handlers.
type Batcher struct {
	app   core.App
	ch    chan StoredEvent
	done  chan struct{}
	once  sync.Once
	wg    sync.WaitGroup
	total int64 // accepted for flush accounting (log lines only)
}

// NewBatcher builds (but does not start) a Batcher bound to app.
func NewBatcher(app core.App) *Batcher {
	return &Batcher{app: app, ch: make(chan StoredEvent, BatcherChannelCap), done: make(chan struct{})}
}

// Start launches the background flush goroutine.
func (b *Batcher) Start() {
	b.wg.Add(1)
	go b.run()
}

// Enqueue buffers one event without blocking; false means full (→ 503).
func (b *Batcher) Enqueue(ev StoredEvent) bool {
	select {
	case b.ch <- ev:
		return true
	default:
		return false
	}
}

// Stop signals shutdown, drains the buffer best-effort, and logs a receipt.
func (b *Batcher) Stop() {
	b.once.Do(func() { close(b.done) })
	b.wg.Wait()
}

func (b *Batcher) run() {
	defer b.wg.Done()
	ticker := time.NewTicker(FlushEvery)
	defer ticker.Stop()
	var buf []StoredEvent
	flush := func() {
		if len(buf) == 0 {
			return
		}
		b.flush(buf)
		buf = nil
	}
	for {
		select {
		case ev := <-b.ch:
			buf = append(buf, ev)
			if len(buf) >= FlushSize {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-b.done:
			for {
				select {
				case ev := <-b.ch:
					buf = append(buf, ev)
				default:
					n := len(buf)
					flush()
					log.Printf("ingest: batcher stopped, drained %d buffered events", n)
					return
				}
			}
		}
	}
}

// flush persists one batch with individual app.Save calls (per contract:
// "via e.App saves"). A failed row is logged and skipped — one poison row
// must not wedge the whole batch.
func (b *Batcher) flush(items []StoredEvent) {
	col, err := b.app.FindCollectionByNameOrId("events")
	if err != nil {
		log.Printf("ingest: flush of %d events skipped: find events collection: %v", len(items), err)
		return
	}
	failed := 0
	for _, it := range items {
		rec := core.NewRecord(col)
		if it.EnvID != "" {
			rec.Set("env", it.EnvID)
		}
		if it.FlagID != "" {
			rec.Set("flag", it.FlagID)
		}
		rec.Set("kind", it.Kind)
		rec.Set("variant", it.Variant)
		rec.Set("userHash", it.UserHash)
		rec.Set("ts", it.Ts)
		if err := b.app.Save(rec); err != nil {
			failed++
			log.Printf("ingest: save event failed (kind=%s variant=%s): %v", it.Kind, it.Variant, err)
		}
	}
	b.total += int64(len(items) - failed)
	if failed > 0 {
		log.Printf("ingest: flushed %d events, %d failed", len(items), failed)
	}
}

// SplitBatch bounds flush work; size <= 0 returns items as a single chunk.
func SplitBatch[T any](items []T, size int) [][]T {
	if size <= 0 || len(items) <= size {
		if items == nil {
			return nil
		}
		return [][]T{items}
	}
	var out [][]T
	for i := 0; i < len(items); i += size {
		end := i + size
		if end > len(items) {
			end = len(items)
		}
		out = append(out, items[i:end])
	}
	return out
}
