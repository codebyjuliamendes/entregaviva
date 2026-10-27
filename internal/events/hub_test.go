package events

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func recv(t *testing.T, s *Subscription) (Event, bool) {
	t.Helper()
	select {
	case ev, ok := <-s.C:
		return ev, ok
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for event")
		return Event{}, false
	}
}

func TestPublishFanOut(t *testing.T) {
	h := NewHub(Options{})
	a, _, _ := h.Subscribe("")
	b, _, _ := h.Subscribe("")
	ev := h.Publish("order", 1)
	for _, s := range []*Subscription{a, b} {
		got, ok := recv(t, s)
		if !ok || got.ID != ev.ID || got.Payload != 1 {
			t.Fatalf("got %+v %v", got, ok)
		}
	}
	a.Unsubscribe()
	a.Unsubscribe() // idempotent
	if h.Len() != 1 {
		t.Fatalf("len %d", h.Len())
	}
	if _, ok := <-a.C; ok {
		t.Fatal("unsubscribed channel must be closed")
	}
}

func TestSlowSubscriberIsDropped(t *testing.T) {
	h := NewHub(Options{BufferSize: 2})
	slow, _, _ := h.Subscribe("")
	fast, _, _ := h.Subscribe("")
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range fast.C {
		}
	}()
	start := time.Now()
	for i := range 10 {
		h.Publish("order", i)
	}
	if time.Since(start) > time.Second {
		t.Fatal("publish blocked on a slow subscriber")
	}
	n := 0
	for range slow.C {
		n++
	}
	if n != 2 || !slow.Lagged() {
		t.Fatalf("slow got %d events, lagged=%v", n, slow.Lagged())
	}
	h.Close()
	<-done
}

func TestReplaySinceLastEventID(t *testing.T) {
	h := NewHub(Options{ReplaySize: 4})
	var ids []string
	for i := range 6 {
		ids = append(ids, h.Publish("order", i).ID)
	}
	// ids[2] is seq 3; ring holds seq 3..6, so 4..6 are replayable.
	_, res, err := h.Subscribe(ids[2])
	if err != nil || res.Resync || len(res.Replay) != 3 || res.Replay[0].Payload != 3 || res.CurrentID != ids[5] {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	// Up to date: nothing to replay.
	if _, res, _ := h.Subscribe(ids[5]); res.Resync || len(res.Replay) != 0 {
		t.Fatal("up-to-date client should not replay")
	}
	// Seq 1 is gone (ring only holds 3..6): replaying from it would miss seq 2.
	if _, res, _ := h.Subscribe(ids[0]); !res.Resync {
		t.Fatal("too-old id must resync")
	}
	// Oldest kept boundary: from seq 2 we still have 3..6.
	if _, res, _ := h.Subscribe(ids[1]); res.Resync || len(res.Replay) != 4 {
		t.Fatalf("boundary res=%+v", res)
	}
	for _, bad := range []string{"other-1", "garbage", h.epoch + "-999"} {
		if _, res, _ := h.Subscribe(bad); !res.Resync {
			t.Fatalf("%q must resync", bad)
		}
	}
	// Fresh connection: no replay, no resync, but an ID to resume from.
	if _, res, _ := h.Subscribe(""); res.Resync || len(res.Replay) != 0 || res.CurrentID != ids[5] {
		t.Fatalf("fresh res=%+v", res)
	}
}

func TestCloseDisconnectsAndRejects(t *testing.T) {
	h := NewHub(Options{})
	s, _, _ := h.Subscribe("")
	h.Close()
	h.Close()
	if _, ok := <-s.C; ok {
		t.Fatal("channel must be closed")
	}
	if _, _, err := h.Subscribe(""); !errors.Is(err, ErrClosed) {
		t.Fatalf("got %v", err)
	}
	h.Publish("order", 1) // must not panic after close
}

func TestMaxSubscribers(t *testing.T) {
	h := NewHub(Options{MaxSubscribers: 1})
	s, _, _ := h.Subscribe("")
	if _, _, err := h.Subscribe(""); !errors.Is(err, ErrTooManySubscribers) {
		t.Fatalf("got %v", err)
	}
	s.Unsubscribe()
	if _, _, err := h.Subscribe(""); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentPublishSubscribe(t *testing.T) {
	h := NewHub(Options{BufferSize: 4})
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				s, _, err := h.Subscribe("")
				if err != nil {
					return
				}
				h.Publish("order", nil)
				s.Unsubscribe()
			}
		}()
	}
	wg.Wait()
	if h.Len() != 0 {
		t.Fatalf("leaked %d subscribers", h.Len())
	}
}
