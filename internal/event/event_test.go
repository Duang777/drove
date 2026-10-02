package event

import (
	"sync"
	"testing"
	"time"
)

func TestNewSessionLifecycleCarriesEncodedPayload(t *testing.T) {
	ev := NewSessionLifecycle(7, "session-1", "agent-1", "created", `{"version":1}`)

	if ev.Seq != 7 ||
		ev.Type != TypeSessionLifecycle ||
		ev.SessionID != "session-1" ||
		ev.AgentID != "agent-1" ||
		ev.Reason != "created" ||
		ev.Payload != `{"version":1}` {
		t.Fatalf("session lifecycle event = %+v", ev)
	}
	if ev.Timestamp.IsZero() {
		t.Fatal("session lifecycle timestamp is zero")
	}
}

func TestHubFanOut(t *testing.T) {
	h := NewHub(0)
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
	h := NewHub(0)
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
	h := NewHub(0)
	ev := NewStateChanged(42, "s", "a", "working", "blocked", "waiting")
	if got := h.Publish(ev); got != 42 {
		t.Fatalf("preset seq publish = %d, want 42", got)
	}
}

func TestUnsubscribeClosesChannel(t *testing.T) {
	h := NewHub(0)
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

func TestHubContinuesAfterInitialSequence(t *testing.T) {
	h := NewHub(41)

	if got := h.NextSeq(); got != 42 {
		t.Fatalf("first seq = %d, want 42", got)
	}
	if got := h.NextSeq(); got != 43 {
		t.Fatalf("second seq = %d, want 43", got)
	}
}

func TestHubCloseClosesSubscriptionsAndIsIdempotent(t *testing.T) {
	h := NewHub(0)
	first := h.Subscribe(1)
	second := h.Subscribe(1)

	h.Close()
	h.Close()
	h.Unsubscribe(first)
	h.Unsubscribe(second)

	for _, sub := range []*Subscription{first, second} {
		select {
		case _, ok := <-sub.C():
			if ok {
				t.Fatal("subscription channel remained open after Hub.Close")
			}
		default:
			t.Fatal("closed subscription channel was not immediately readable")
		}
	}

	afterClose := h.Subscribe(1)
	select {
	case _, ok := <-afterClose.C():
		if ok {
			t.Fatal("subscription created after Hub.Close remained open")
		}
	default:
		t.Fatal("subscription created after Hub.Close was not closed")
	}
	if got := h.Publish(NewOutput(0, "session", "agent", "ignored")); got != 1 {
		t.Fatalf("publish sequence after close = %d, want 1", got)
	}
}

func TestHubCloseIsSafeWithConcurrentPublishAndUnsubscribe(t *testing.T) {
	h := NewHub(0)
	start := make(chan struct{})
	var wg sync.WaitGroup

	for i := 0; i < 16; i++ {
		sub := h.Subscribe(32)
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < 100; j++ {
				h.Publish(NewOutput(0, "session", "agent", "line"))
			}
		}()
		go func(sub *Subscription) {
			defer wg.Done()
			<-start
			h.Unsubscribe(sub)
		}(sub)
	}

	close(start)
	h.Close()
	wg.Wait()
}
