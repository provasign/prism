// Package store defines the read interface every backend implements.
package store

// Store reads values by key.
type Store interface {
	Get(key string) string
}
