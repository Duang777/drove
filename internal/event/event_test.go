package event

import (
	"errors"
	"sync"
	"sync/atomic"
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

func TestNewAgentInputCarriesRedactedPayload(t *testing.T) {
	ev := NewAgentInput(8, "session-1", "agent-1", `{"version":1,"bytes":9}`)

	if ev.Seq != 8 ||
		ev.Type != TypeAgentInput ||
		ev.SessionID != "session-1" ||
		ev.AgentID != "agent-1" ||
		ev.Reason != "accepted" ||
		ev.Payload != `{"version":1,"bytes":9}` {
		t.Fatalf("agent input event = %+v", ev)
	}
	if ev.Timestamp.IsZero() {
		t.Fatal("agent input timestamp is zero")
	}
}

func TestNewAgentSignalCarriesNormalizedPayload(t *testing.T) {
	ev := NewAgentSignal(9, "session-1", "agent-1", "hook", `{"version":1}`)

	if ev.Seq != 9 ||
		ev.Type != TypeAgentSignal ||
		ev.SessionID != "session-1" ||
		ev.AgentID != "agent-1" ||
		ev.Reason != "hook" ||
		ev.Payload != `{"version":1}` {
		t.Fatalf("agent signal event = %+v", ev)
	}
}

func TestHubFanOut(t *testing.T) {
	h := NewHub(0)
	s1 := h.Subscribe(16)
	s2 := h.Subscribe(16)
	defer h.Unsubscribe(s1)
	defer h.Unsubscribe(s2)

	ev := NewOutput(1, "sess", "agent", "hello\n")
	if err := h.Publish(ev); err != nil {
		t.Fatalf("publish: %v", err)
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

	if err := h.Publish(NewOutput(1, "s", "a", "one")); err != nil {
		t.Fatalf("publish first event: %v", err)
	}
	// 缓冲已满，第二条被丢弃（发布者不阻塞）。
	if err := h.Publish(NewOutput(2, "s", "a", "two")); err != nil {
		t.Fatalf("publish second event: %v", err)
	}
	if err := h.Publish(NewOutput(3, "s", "a", "three")); err != nil {
		t.Fatalf("publish third event: %v", err)
	}

	<-s.C() // 消费第一条
	time.Sleep(10 * time.Millisecond)

	if d := s.Dropped(); d < 2 {
		t.Fatalf("expected >=2 dropped events, got %d", d)
	}
}

func TestPublishWithPresetSeq(t *testing.T) {
	h := NewHub(41)
	ev := NewStateChanged(42, "s", "a", "working", "blocked", "waiting")
	if err := h.Publish(ev); err != nil {
		t.Fatalf("publish preset sequence: %v", err)
	}
	if got := h.LastSeq(); got != 42 {
		t.Fatalf("last seq = %d, want 42", got)
	}
}

func TestHubRejectsUncommittedAndOutOfOrderEvents(t *testing.T) {
	h := NewHub(4)

	if err := h.Publish(NewOutput(0, "s", "a", "draft")); !errors.Is(err, ErrUncommittedEvent) {
		t.Fatalf("uncommitted publish error = %v, want ErrUncommittedEvent", err)
	}
	if err := h.Publish(NewOutput(4, "s", "a", "old")); !errors.Is(err, ErrSequenceOrder) {
		t.Fatalf("out-of-order publish error = %v, want ErrSequenceOrder", err)
	}
	if got := h.LastSeq(); got != 4 {
		t.Fatalf("last seq = %d, want unchanged 4", got)
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

func TestHubTracksInitialSequence(t *testing.T) {
	h := NewHub(41)

	if got := h.LastSeq(); got != 41 {
		t.Fatalf("last seq = %d, want 41", got)
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
	if err := h.Publish(NewOutput(1, "session", "agent", "ignored")); !errors.Is(err, ErrHubClosed) {
		t.Fatalf("publish after close error = %v, want ErrHubClosed", err)
	}
}

func TestHubCloseIsSafeWithConcurrentPublishAndUnsubscribe(t *testing.T) {
	h := NewHub(0)
	start := make(chan struct{})
	var wg sync.WaitGroup
	var seq atomic.Uint64

	for i := 0; i < 16; i++ {
		sub := h.Subscribe(32)
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < 100; j++ {
				h.Publish(NewOutput(seq.Add(1), "session", "agent", "line"))
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
