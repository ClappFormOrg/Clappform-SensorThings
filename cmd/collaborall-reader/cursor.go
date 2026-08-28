package main

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

// cursorStore persists a per-source-Datastream "since" cursor to a JSON
// file so the reader resumes where it left off across restarts. Push mode
// has no server-side cursor, so the reader owns this state; the write-log
// makes any re-send after a crash idempotent.
type cursorStore struct {
	path string
	mu   sync.Mutex
	m    map[string]time.Time // sourceDatastreamID → last covered phenomenonTime
}

// loadCursorStore reads path if it exists; a missing file starts empty.
func loadCursorStore(path string) (*cursorStore, error) {
	cs := &cursorStore{path: path, m: map[string]time.Time{}}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cs, nil
		}
		return nil, err
	}
	if len(b) == 0 {
		return cs, nil
	}
	if err := json.Unmarshal(b, &cs.m); err != nil {
		return nil, err
	}
	return cs, nil
}

// get returns the cursor for key, or the zero time if unset.
func (c *cursorStore) get(key string) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.m[key]
}

// advance moves key's cursor forward to ts (never backwards) and persists.
func (c *cursorStore) advance(key string, ts time.Time) error {
	c.mu.Lock()
	if cur, ok := c.m[key]; ok && !ts.After(cur) {
		c.mu.Unlock()
		return nil
	}
	c.m[key] = ts.UTC()
	snapshot := make(map[string]time.Time, len(c.m))
	for k, v := range c.m {
		snapshot[k] = v
	}
	c.mu.Unlock()

	b, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	// Write atomically: temp file + rename, so a crash mid-write can't
	// corrupt the cursor file.
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.path)
}
