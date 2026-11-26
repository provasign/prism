// Package app wires the stores together.
package app

import (
	"example.com/fx/config"
	"example.com/fx/disk"
	"example.com/fx/mem"
	"example.com/fx/remote"
	"example.com/fx/store"
)

// Lookup reads key from s, falling back to config.Default.
func Lookup(s store.Store, key string) string {
	if v := s.Get(key); v != "" {
		return v
	}
	return config.Default
}

// All reads key from every backend.
func All(key string) []string {
	return []string{
		Lookup(mem.New(), key),
		Lookup(&disk.Cache{Dir: "/tmp"}, key),
		Lookup(remote.Client{URL: "https://example.com"}, key),
	}
}
