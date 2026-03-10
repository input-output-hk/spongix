package store

import "github.com/folbricht/desync"

// RefTrackingIndexStore extends desync.IndexWriteStore with reference tracking.
// Implementations that support reference tracking (like the Pebble IndexStore)
// should implement this interface. The router uses it to store NAR references
// alongside indices for GC's transitive closure.
type RefTrackingIndexStore interface {
	desync.IndexWriteStore
	StoreIndexWithRefs(name string, idx desync.Index, narRefs []StorePathHash32) error
}
