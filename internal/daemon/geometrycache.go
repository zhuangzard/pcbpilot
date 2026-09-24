package daemon

import (
	"sync"
	"time"

	"github.com/zhuangzard/pcbpilot/internal/protocol"
)

// Guarded schematic writes read the whole page before and after the write
// (schematic_geometry.go). A full read costs ~1.5 s, and a block apply issues
// tens of guarded writes back to back, so the guard doubled the page reads of
// a run. When nothing else has touched the window since the previous guarded
// write, that write's verified after-read IS the page the next write starts
// from: reuse it as the next before-read.
//
// Reuse requires, per window:
//   - no other write, document transition, debug script or page-cycling read
//     since the snapshot (every such request bumps the epoch);
//   - the snapshot is recent (geometryCacheTTL), bounding the exposure to a
//     human editing the page in between;
//   - the entry is consumed on use, so any failure path leaves no snapshot and
//     the next write reads fresh.
//
// The after-read of every write is always a fresh read, and its document/FIFO
// freshness checks still compare against the reused snapshot: a page that
// changed underneath fails closed, as before.
const geometryCacheTTL = 10 * time.Second

type geometryCacheEntry struct {
	epoch uint64
	at    time.Time
	res   *protocol.Response
}

type geometryCache struct {
	mu      sync.Mutex
	epoch   map[string]uint64
	entries map[string]geometryCacheEntry
	now     func() time.Time
}

func newGeometryCache() *geometryCache {
	return &geometryCache{epoch: map[string]uint64{}, entries: map[string]geometryCacheEntry{}, now: time.Now}
}

// invalidates reports whether a request may change what the page holds (or
// which page is active) without being a guarded write that refreshes the cache.
func geometryCacheInvalidates(req *protocol.Request) bool {
	if schematicGeometryGuarded(req.Action) || req.Action == "document.current" {
		return false
	}
	return schematicGeometrySerializes(req)
}

func (c *geometryCache) bump(window string) {
	c.mu.Lock()
	c.epoch[window]++
	delete(c.entries, window)
	c.mu.Unlock()
}

// take consumes a reusable snapshot for window, or returns nil.
func (c *geometryCache) take(window string) *protocol.Response {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[window]
	delete(c.entries, window)
	if !ok || e.epoch != c.epoch[window] || c.now().Sub(e.at) > geometryCacheTTL {
		return nil
	}
	return e.res
}

func (c *geometryCache) put(window string, res *protocol.Response) {
	c.mu.Lock()
	c.entries[window] = geometryCacheEntry{epoch: c.epoch[window], at: c.now(), res: res}
	c.mu.Unlock()
}
