package lru

import "testing"

func TestCacheEvictsLeastRecentlyUsed(t *testing.T) {
	c := New[string, int](2)
	c.Put("a", 1)
	c.Put("b", 2)
	if v, ok := c.Get("a"); !ok || v != 1 {
		t.Fatal("a missing")
	}
	c.Put("c", 3) // b is now the oldest
	if _, ok := c.Get("b"); ok {
		t.Fatal("b should have been evicted")
	}
	c.Put("a", 10)
	if v, _ := c.Get("a"); v != 10 || c.Len() != 2 {
		t.Fatalf("a=%d len=%d", v, c.Len())
	}
	if _, ok := c.Get("c"); !ok {
		t.Fatal("c missing")
	}
	if New[int, int](0).max != 1 {
		t.Fatal("limit below 1 not raised")
	}
}
