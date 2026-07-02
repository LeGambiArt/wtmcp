package credentials

import (
	"sync"
	"testing"
	"time"
)

func TestCacheSetGet(t *testing.T) {
	c := NewCache()

	c.Set("jira", "TOKEN", "abc123", SourceEnvD)

	val, ok := c.Get("jira", "TOKEN")
	if !ok {
		t.Fatal("Get returned not-found for cached entry")
	}
	if val.Value != "abc123" {
		t.Errorf("Value = %q, want %q", val.Value, "abc123")
	}
	if val.Source != SourceEnvD {
		t.Errorf("Source = %v, want %v", val.Source, SourceEnvD)
	}
	if val.LoadedAt.IsZero() {
		t.Error("LoadedAt is zero")
	}
}

func TestCacheGetMiss(t *testing.T) {
	c := NewCache()

	val, ok := c.Get("nonexistent", "KEY")
	if ok {
		t.Errorf("Get returned ok=true for missing entry, val=%v", val)
	}
}

func TestCacheTTLExpiry(t *testing.T) {
	c := NewCacheWithTTL(50 * time.Millisecond)

	c.Set("group", "KEY", "value", SourceKeyring)

	// Should be found immediately
	if _, ok := c.Get("group", "KEY"); !ok {
		t.Fatal("Get returned not-found for fresh entry")
	}

	// Wait for expiry
	time.Sleep(100 * time.Millisecond)

	val, ok := c.Get("group", "KEY")
	if ok {
		t.Errorf("Get returned ok=true for expired entry, val=%v", val)
	}
}

func TestCacheInvalidate(t *testing.T) {
	c := NewCache()

	c.Set("group", "KEY1", "v1", SourceEnvVar)
	c.Set("group", "KEY2", "v2", SourceEnvVar)

	c.Invalidate("group", "KEY1")

	if _, ok := c.Get("group", "KEY1"); ok {
		t.Error("KEY1 still cached after Invalidate")
	}
	if _, ok := c.Get("group", "KEY2"); !ok {
		t.Error("KEY2 should still be cached")
	}
}

func TestCacheInvalidateGroup(t *testing.T) {
	c := NewCache()

	c.Set("group-a", "KEY1", "v1", SourceEnvD)
	c.Set("group-a", "KEY2", "v2", SourceEnvD)
	c.Set("group-b", "KEY1", "v3", SourceEnvD)

	c.InvalidateGroup("group-a")

	if _, ok := c.Get("group-a", "KEY1"); ok {
		t.Error("group-a KEY1 still cached after InvalidateGroup")
	}
	if _, ok := c.Get("group-a", "KEY2"); ok {
		t.Error("group-a KEY2 still cached after InvalidateGroup")
	}
	if _, ok := c.Get("group-b", "KEY1"); !ok {
		t.Error("group-b KEY1 should still be cached")
	}
}

func TestCacheClear(t *testing.T) {
	c := NewCache()

	c.Set("g1", "K1", "v1", SourceKeyring)
	c.Set("g2", "K2", "v2", SourceEnvD)

	c.Clear()

	if _, ok := c.Get("g1", "K1"); ok {
		t.Error("g1/K1 still cached after Clear")
	}
	if _, ok := c.Get("g2", "K2"); ok {
		t.Error("g2/K2 still cached after Clear")
	}
}

func TestCacheOverwrite(t *testing.T) {
	c := NewCache()

	c.Set("group", "KEY", "old", SourceEnvD)
	c.Set("group", "KEY", "new", SourceKeyring)

	val, ok := c.Get("group", "KEY")
	if !ok {
		t.Fatal("Get returned not-found after overwrite")
	}
	if val.Value != "new" {
		t.Errorf("Value = %q, want %q", val.Value, "new")
	}
	if val.Source != SourceKeyring {
		t.Errorf("Source = %v, want %v", val.Source, SourceKeyring)
	}
}

func TestCacheConcurrency(t *testing.T) {
	t.Parallel()

	c := NewCache()
	const goroutines = 50
	const iterations = 100

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func(_ int) {
			defer wg.Done()
			group := "concurrent"
			key := "KEY"
			for j := 0; j < iterations; j++ {
				c.Set(group, key, "value", SourceEnvD)
				c.Get(group, key)
				if j%10 == 0 {
					c.Invalidate(group, key)
				}
				if j%50 == 0 {
					c.InvalidateGroup(group)
				}
			}
		}(i)
	}

	wg.Wait()
	// If we get here without a race condition panic, the test passes.
}

func TestCacheDefaultTTL(t *testing.T) {
	c := NewCache()
	if c.ttl != DefaultTTL {
		t.Errorf("default TTL = %v, want %v", c.ttl, DefaultTTL)
	}
}

func TestCacheCustomTTL(t *testing.T) {
	ttl := 5 * time.Minute
	c := NewCacheWithTTL(ttl)
	if c.ttl != ttl {
		t.Errorf("custom TTL = %v, want %v", c.ttl, ttl)
	}
}
