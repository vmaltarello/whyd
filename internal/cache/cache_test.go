package cache

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vmaltarello/whyd/internal/analyze"
)

// A file with the right key and no sessions must give an empty cache, not a nil map that
// panics on the first Put.
func TestOpenNullBoots(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", dir)
	path := filepath.Join(dir, "whyd", "sessions.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"key":"k","boots":null}`), 0o600); err != nil {
		t.Fatal(err)
	}

	c := Open("k")
	last := time.Date(2026, 10, 9, 18, 49, 28, 0, time.UTC)
	c.Put("a", analyze.Summary{Last: last})
	if err := c.Save([]string{"a"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := Open("k").Get("a", last); !ok {
		t.Error("the session is not in the cache saved again")
	}
}
