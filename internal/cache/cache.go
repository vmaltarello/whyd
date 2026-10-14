// Package cache keeps the summary of each ended session on disk, so lists don't read every
// session's journal again at each start: an ended session never changes.
package cache

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/vmaltarello/whyd/internal/analyze"
)

// Cache maps boot IDs to summaries. It is safe for concurrent use.
type Cache struct {
	mu    sync.Mutex
	path  string
	key   string
	boots map[string]analyze.Summary
	dirty bool
}

type file struct {
	Key   string                     `json:"key"` // whyd version and rules: a change empties the cache
	Boots map[string]analyze.Summary `json:"boots"`
}

// Open reads the cache in the user's cache directory; key is what the summaries depend on.
// A missing, unreadable or outdated file gives an empty cache.
func Open(key string) *Cache {
	c := &Cache{key: key, boots: map[string]analyze.Summary{}}
	dir, err := os.UserCacheDir()
	if err != nil {
		return c // no place for it: everything works, only slower
	}
	c.path = filepath.Join(dir, "whyd", "sessions.json")
	var f file
	if data, err := os.ReadFile(c.path); err == nil && json.Unmarshal(data, &f) == nil && f.Key == key && f.Boots != nil {
		c.boots = f.Boots
	}
	return c
}

// Get returns the summary of a boot, if it is cached for a boot with the same last entry.
func (c *Cache) Get(id string, last time.Time) (analyze.Summary, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s, ok := c.boots[id]
	return s, ok && s.Last.Equal(last)
}

// Put stores the summary of a boot.
func (c *Cache) Put(id string, s analyze.Summary) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.boots[id] = s
	c.dirty = true
}

// Save writes the cache if it changed, keeping only the boots still in the journal.
func (c *Cache) Save(keep []string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.path == "" {
		return nil
	}
	f := file{Key: c.key, Boots: map[string]analyze.Summary{}}
	for _, id := range keep {
		if s, ok := c.boots[id]; ok {
			f.Boots[id] = s
		}
	}
	if !c.dirty && len(f.Boots) == len(c.boots) {
		return nil
	}
	data, err := json.Marshal(f)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	c.dirty = false
	return os.Rename(tmp, c.path)
}
