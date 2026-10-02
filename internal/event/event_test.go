package event

import (
	"testing"
	"time"
)

func TestHubFanOut(t *testing.T) {
	h := NewHub()
	s1 := h.Subscribe(16)
	s2 := h.Subscribe(16)
	defer h.Unsubscribe(s1)
	defer h.Unsubscribe(s2)

	ev := NewOutput(0, "sess", "agent", "hello\n")
	seq := h.Publish(ev)

	if seq != 1 {
		t.Fatalf("first seq = %d, want 1", seq)
	}

	for _, s := range []*Subscription{s1, s2} {
		select {
		case got := <-s.C():
			if got.Seq != 1 || got.Payload != "hello\n" {
				t.Fatalf("subscriber got %+v", got)
			}
		case <-time.After(time.Second):
			t.Fatal("subscriber did not receive event")
		}
	}
}

func TestHubSlowSubscriberDropped(t *testing.T) {
	h := NewHub()
	s := h.Subscribe(1)
	defer h.Unsubscribe(s)

	h.Publish(NewOutput(0, "s", "a", "one"))
	// 缓冲已满，第二条被丢弃（发布者不阻塞）。
	h.Publish(NewOutput(0, "s", "a", "two"))
	h.Publish(NewOutput(0, "s", "a", "three"))

	<-s.C() // 消费第一条
	time.Sleep(10 * time.Millisecond)

	if d := s.Dropped(); d < 2 {
		t.Fatalf("expected >=2 dropped events, got %d", d)
	}
}

func TestPublishWithPresetSeq(t *testing.T) {
	h := NewHub()
	ev := NewStateChanged(42, "s", "a", "working", "blocked", "waiting")
	if got := h.Publish(ev); got != 42 {
		t.Fatalf("preset seq publish = %d, want 42", got)
	}
}

func TestUnsubscribeClosesChannel(t *testing.T) {
	h := NewHub()
	s := h.Subscribe(4)
	h.Unsubscribe(s)
	select {
	case _, ok := <-s.C():
		if ok {
			t.Fatal("channel should be closed after unsubscribe")
		}
	default:
		t.Fatal("expected closed channel to be immediately readable")
	}
}
