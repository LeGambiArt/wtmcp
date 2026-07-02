package credentials

import (
	"fmt"
	"sync"
	"time"
)

const (
	// DefaultTTL is the default cache entry lifetime.
	DefaultTTL = 15 * time.Minute
)

// Cache provides thread-safe in-memory caching for resolved
// credentials. Each entry expires after DefaultTTL.
type Cache struct {
	mu      sync.RWMutex
	entries map[string]*CachedValue
	ttl     time.Duration
}

// NewCache creates a new credential cache with the default TTL.
func NewCache() *Cache {
	return &Cache{
		entries: make(map[string]*CachedValue),
		ttl:     DefaultTTL,
	}
}

// NewCacheWithTTL creates a new credential cache with a custom TTL.
// Useful for testing with shorter durations.
func NewCacheWithTTL(ttl time.Duration) *Cache {
	return &Cache{
		entries: make(map[string]*CachedValue),
		ttl:     ttl,
	}
}

// cacheKey builds a composite key from group and key.
func cacheKey(group, key string) string {
	return fmt.Sprintf("%s\x00%s", group, key)
}

// Get retrieves a cached credential. Returns the cached value and
// true if found and not expired, or nil and false otherwise.
// Expired entries are lazily evicted on access.
func (c *Cache) Get(group, key string) (*CachedValue, bool) {
	ck := cacheKey(group, key)

	c.mu.RLock()
	entry, ok := c.entries[ck]
	c.mu.RUnlock()

	if !ok {
		return nil, false
	}

	if time.Since(entry.LoadedAt) > c.ttl {
		c.mu.Lock()
		delete(c.entries, ck)
		c.mu.Unlock()
		return nil, false
	}

	return entry, true
}

// Set stores a credential in the cache with the current timestamp.
func (c *Cache) Set(group, key, value string, source CredentialSource) {
	ck := cacheKey(group, key)

	entry := &CachedValue{
		Value:    value,
		Source:   source,
		LoadedAt: time.Now(),
	}

	c.mu.Lock()
	c.entries[ck] = entry
	c.mu.Unlock()
}

// Invalidate removes a specific credential from the cache.
func (c *Cache) Invalidate(group, key string) {
	ck := cacheKey(group, key)

	c.mu.Lock()
	delete(c.entries, ck)
	c.mu.Unlock()
}

// InvalidateGroup removes all cached credentials for a group.
func (c *Cache) InvalidateGroup(group string) {
	prefix := group + "\x00"

	c.mu.Lock()
	for ck := range c.entries {
		if len(ck) >= len(prefix) && ck[:len(prefix)] == prefix {
			delete(c.entries, ck)
		}
	}
	c.mu.Unlock()
}

// Clear removes all entries from the cache.
func (c *Cache) Clear() {
	c.mu.Lock()
	c.entries = make(map[string]*CachedValue)
	c.mu.Unlock()
}
