package cache

import (
	"testing"
	"time"
)

func TestGetSetDeleteClear(t *testing.T) {
	c := New[string](time.Minute, 10)
	if _, ok := c.Get("a"); ok {
		t.Fatal("empty cache returned a value")
	}
	c.Set("a", "1")
	if v, ok := c.Get("a"); !ok || v != "1" {
		t.Errorf("got %q %v", v, ok)
	}
	c.Delete("a")
	if _, ok := c.Get("a"); ok {
		t.Error("deleted key still present")
	}
	c.Set("a", "1")
	c.Set("b", "2")
	c.Clear()
	if _, ok := c.Get("b"); ok {
		t.Error("clear left an entry behind")
	}
}

func TestEntriesExpire(t *testing.T) {
	c := New[string](20*time.Millisecond, 10)
	c.Set("a", "1")
	time.Sleep(40 * time.Millisecond)
	if _, ok := c.Get("a"); ok {
		t.Error("expired entry was returned")
	}
}

func TestCountBoundEvictsOldest(t *testing.T) {
	c := New[int](time.Minute, 2)
	c.Set("a", 1)
	time.Sleep(2 * time.Millisecond)
	c.Set("b", 2)
	time.Sleep(2 * time.Millisecond)
	c.Set("c", 3)
	if _, ok := c.Get("a"); ok {
		t.Error("oldest entry should have been evicted")
	}
	if _, ok := c.Get("b"); !ok {
		t.Error("newer entry was evicted")
	}
	if _, ok := c.Get("c"); !ok {
		t.Error("newest entry missing")
	}
}

func TestByteBound(t *testing.T) {
	size := func(s string) int { return len(s) }
	c := NewSized[string](time.Minute, 100, 10, size)

	c.Set("a", "123456")
	time.Sleep(2 * time.Millisecond)
	c.Set("b", "123456") // 12 > 10: the older one has to go
	if _, ok := c.Get("a"); ok {
		t.Error("byte bound should evict the oldest")
	}
	if _, ok := c.Get("b"); !ok {
		t.Error("newest entry should fit")
	}

	// Bigger than the whole budget: not stored, and nothing else is lost for it.
	c.Set("huge", "0123456789ABC")
	if _, ok := c.Get("huge"); ok {
		t.Error("an entry over the whole budget must not be stored")
	}
	if _, ok := c.Get("b"); !ok {
		t.Error("an unstorable entry must not evict others")
	}
}
