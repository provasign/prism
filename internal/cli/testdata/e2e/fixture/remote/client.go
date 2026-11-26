// Package remote is a Store over the network.
package remote

// Client fetches values from URL.
type Client struct{ URL string }

// Get returns the URL for key.
func (c Client) Get(key string) string { return c.URL + "/" + key }
