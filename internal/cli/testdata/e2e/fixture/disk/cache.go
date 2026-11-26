// Package disk is a file-backed Store.
package disk

// Cache maps keys to files under Dir.
type Cache struct{ Dir string }

// Get returns the path that would hold key.
func (c *Cache) Get(key string) string { return c.Dir + "/" + key }
