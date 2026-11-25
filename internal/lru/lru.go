// Package lru is a small least-recently-used map with a fixed entry limit,
// for per-session caches in long-running servers.
package lru

import "container/list"

// Cache is a map that drops its least recently used entry when a Put would
// exceed the limit. It is not safe for concurrent use; callers hold their own
// lock, as the maps it replaces did.
type Cache[K comparable, V any] struct {
	max   int
	order *list.List // front = most recent
	items map[K]*list.Element
}

type entry[K comparable, V any] struct {
	key K
	val V
}

// New returns a cache holding at most max entries (at least 1).
func New[K comparable, V any](max int) *Cache[K, V] {
	if max < 1 {
		max = 1
	}
	return &Cache[K, V]{max: max, order: list.New(), items: map[K]*list.Element{}}
}

// Get returns the value for key and marks it most recently used.
func (c *Cache[K, V]) Get(key K) (V, bool) {
	if el, ok := c.items[key]; ok {
		c.order.MoveToFront(el)
		return el.Value.(*entry[K, V]).val, true
	}
	var zero V
	return zero, false
}

// Put sets key to val, evicting the least recently used entry when full.
func (c *Cache[K, V]) Put(key K, val V) {
	if el, ok := c.items[key]; ok {
		el.Value.(*entry[K, V]).val = val
		c.order.MoveToFront(el)
		return
	}
	c.items[key] = c.order.PushFront(&entry[K, V]{key, val})
	if c.order.Len() > c.max {
		oldest := c.order.Back()
		c.order.Remove(oldest)
		delete(c.items, oldest.Value.(*entry[K, V]).key)
	}
}

// Len is the number of entries.
func (c *Cache[K, V]) Len() int { return c.order.Len() }
