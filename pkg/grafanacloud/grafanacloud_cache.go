package grafanacloud

import (
	"fmt"
	"sync"
	"time"

	"github.com/go-logr/logr"
)

type CacheEntry struct {
	stack     *Stack
	timestamp int64
}

func (c CacheEntry) Value() *Stack {
	return c.stack
}

// IsStale checks if the entry is older than the amount passed for
// staleness in seconds.
func (c CacheEntry) IsStale(staleness int) bool {
	return time.Now().Unix()-c.timestamp > int64(staleness)
}

type StackCache struct {
	sync.RWMutex
	data map[string]CacheEntry

	// Last time we updated all entries
	timestamp int64
}

func NewStackCache() StackCache {
	data := make(map[string]CacheEntry)
	return StackCache{data: data}
}

type CachedClient struct {
	Log logr.Logger

	client *Client
	cache  StackCache
}

func NewCachedClient(logr logr.Logger, client *Client) *CachedClient {
	return &CachedClient{
		Log:    logr,
		client: client,
		cache:  NewStackCache(),
	}
}

// GetStack returns a stack definition for the corresponding GrafanaCloud stack
func (c *CachedClient) GetStack(slug string) (*Stack, error) {
	c.cache.RLock()
	timestamp := c.cache.timestamp
	c.cache.RUnlock()

	if timestamp == 0 {
		// cache is empty, fill it with the data we have
		if err := c.loadCache(600); err != nil {
			return nil, fmt.Errorf("failed to update cache: %w", err)
		}
	}

	c.cache.RLock()
	entry, ok := c.cache.data[slug]
	c.cache.RUnlock()

	if ok && !entry.IsStale(300) {
		// Recent cache entry
		return entry.Value(), nil
	}

	s, err := c.client.GetStack(slug)
	if err != nil {
		return nil, err
	}
	c.cache.Lock()
	c.cache.data[slug] = CacheEntry{stack: s, timestamp: time.Now().Unix()}
	c.cache.Unlock()

	return s, nil
}

func (c *CachedClient) GetTracesConnection(stackSlug string) (int, string, error) {
	return c.client.GetTracesConnection(stackSlug)
}

func (c *CachedClient) ListStacks() (Stacks, error) {
	if c.cache.timestamp == 0 || time.Now().Unix()-c.cache.timestamp > 600 {
		if err := c.loadCache(600); err != nil {
			return nil, fmt.Errorf("failed to update cache: %w", err)
		}
	}

	stacks := []Stack{}
	for _, entry := range c.cache.data {
		stacks = append(stacks, *entry.Value())
	}

	return stacks, nil
}

func (c *CachedClient) loadCache(staleness int) error {
	c.cache.Lock()
	defer c.cache.Unlock()

	if time.Now().Unix()-c.cache.timestamp < int64(staleness) {
		// cache was already updated by some other thread
		// while we were waiting for the lock
		return nil
	}

	stacks, err := c.client.ListStacks()
	if err != nil {
		return err
	}

	clear(c.cache.data)
	for _, stack := range stacks {
		c.cache.data[stack.Slug] = CacheEntry{stack: &stack, timestamp: time.Now().Unix()}
	}

	c.cache.timestamp = time.Now().Unix()
	return nil
}
