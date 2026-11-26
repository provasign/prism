// Package mem is an in-memory Store.
package mem

import "example.com/fx/config"

// Cache keeps values in a map. disk.Cache has the same name on purpose.
type Cache struct{ m map[string]string }

// New returns an empty Cache.
func New() *Cache { return &Cache{m: map[string]string{}} }

// Get returns the value for key, or config.Default.
func (c *Cache) Get(key string) string {
	if v, ok := c.m[key]; ok {
		return v
	}
	return config.Default
}

// Put stores a value.
func (c *Cache) Put(key, value string) { c.m[key] = value }
