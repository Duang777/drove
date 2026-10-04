package recording

import (
	"container/list"
	"sync"
)

const defaultFrameCacheEntries = 64

type frameCacheKey struct {
	sessionID           string
	sequence            Seq
	outputOffset        OutputOffset
	bottomRows          int
	maxCellsPerRow      int
	maxBytes            int
	retentionGeneration uint64
}

type frameCacheEntry struct {
	key   frameCacheKey
	frame Frame
}

type frameCache struct {
	mu       sync.Mutex
	capacity int
	entries  map[frameCacheKey]*list.Element
	order    *list.List
}

func newFrameCache(capacity int) *frameCache {
	if capacity < 0 {
		capacity = 0
	}
	return &frameCache{
		capacity: capacity,
		entries:  make(map[frameCacheKey]*list.Element),
		order:    list.New(),
	}
}

func (c *frameCache) Get(key frameCacheKey) (Frame, bool) {
	if c == nil || c.capacity == 0 {
		return Frame{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	element, ok := c.entries[key]
	if !ok {
		return Frame{}, false
	}
	c.order.MoveToFront(element)
	entry := element.Value.(frameCacheEntry)
	return cloneFrame(entry.frame), true
}

func (c *frameCache) Add(key frameCacheKey, frame Frame) {
	if c == nil || c.capacity == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if element, ok := c.entries[key]; ok {
		element.Value = frameCacheEntry{key: key, frame: cloneFrame(frame)}
		c.order.MoveToFront(element)
		return
	}
	element := c.order.PushFront(frameCacheEntry{
		key:   key,
		frame: cloneFrame(frame),
	})
	c.entries[key] = element
	for c.order.Len() > c.capacity {
		oldest := c.order.Back()
		entry := oldest.Value.(frameCacheEntry)
		delete(c.entries, entry.key)
		c.order.Remove(oldest)
	}
}

func cloneFrame(frame Frame) Frame {
	if frame.Lines != nil {
		frame.Lines = append([]string{}, frame.Lines...)
	}
	return frame
}
