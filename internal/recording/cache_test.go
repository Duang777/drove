package recording

import "testing"

func TestFrameCacheIsBoundedLRUAndCopiesRows(t *testing.T) {
	cache := newFrameCache(2)
	firstKey := frameCacheKey{sessionID: "s1", sequence: 1}
	secondKey := frameCacheKey{sessionID: "s1", sequence: 2}
	thirdKey := frameCacheKey{sessionID: "s1", sequence: 3}
	cache.Add(firstKey, Frame{Lines: []string{"first"}})
	cache.Add(secondKey, Frame{Lines: []string{"second"}})

	first, ok := cache.Get(firstKey)
	if !ok {
		t.Fatal("first cache entry missing")
	}
	first.Lines[0] = "mutated"
	cache.Add(thirdKey, Frame{Lines: []string{"third"}})

	if _, ok := cache.Get(secondKey); ok {
		t.Fatal("least recently used entry was not evicted")
	}
	first, ok = cache.Get(firstKey)
	if !ok || len(first.Lines) != 1 || first.Lines[0] != "first" {
		t.Fatalf("cached frame was aliased or evicted: %+v, %t", first, ok)
	}
	if third, ok := cache.Get(thirdKey); !ok ||
		len(third.Lines) != 1 ||
		third.Lines[0] != "third" {
		t.Fatalf("third cache entry = %+v, %t", third, ok)
	}
}

func TestFrameCacheCanBeDisabled(t *testing.T) {
	cache := newFrameCache(0)
	key := frameCacheKey{sessionID: "s1", sequence: 1}
	cache.Add(key, Frame{Lines: []string{"ignored"}})
	if _, ok := cache.Get(key); ok {
		t.Fatal("disabled cache retained a frame")
	}
}
