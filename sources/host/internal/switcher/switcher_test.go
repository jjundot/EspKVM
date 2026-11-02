package switcher

import (
	"context"
	"github.com/jjundot/EspKVM/internal/input"
	"github.com/jjundot/EspKVM/internal/protocol/pb"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type capture struct {
	remote atomic.Bool
	wait   atomic.Bool
}

func (c *capture) Local() { c.remote.Store(false) }
func (c *capture) Remote() (bool, error) {
	if c.wait.Load() {
		return false, nil
	}
	c.remote.Store(true)
	return true, nil
}
func (c *capture) ReleaseLocal(*input.State) error { return nil }

type endpoint struct {
	barriers   chan chan error
	sent       chan *pb.Msg
	closed     atomic.Bool
	mu         sync.Mutex
	generation uint64
}

func newEndpoint() *endpoint {
	return &endpoint{barriers: make(chan chan error, 8), sent: make(chan *pb.Msg, 128)}
}
func (e *endpoint) Send(m *pb.Msg, g uint64, _ bool) error {
	e.mu.Lock()
	e.generation = g
	e.mu.Unlock()
	e.sent <- m
	return nil
}
func (e *endpoint) Release(g uint64) <-chan error {
	e.mu.Lock()
	e.generation = g
	e.mu.Unlock()
	ch := make(chan error, 1)
	e.barriers <- ch
	return ch
}
func (e *endpoint) Close() { e.closed.Store(true) }
func wait(t *testing.T, p func() bool) {
	t.Helper()
	end := time.Now().Add(time.Second)
	for !p() {
		if time.Now().After(end) {
			t.Fatal("state stalled")
		}
		time.Sleep(time.Millisecond)
	}
}
func barrier(t *testing.T, e *endpoint) chan error {
	t.Helper()
	select {
	case ch := <-e.barriers:
		return ch
	case <-time.After(time.Second):
		t.Fatal("missing release")
		return nil
	}
}
func TestSwitchBarrierKeepsDispatcherResponsive(t *testing.T) {
	c := &capture{}
	d := New(c)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx)
	a, b := newEndpoint(), newEndpoint()
	d.Attach("a", a, true)
	d.Attach("b", b, true)
	d.Order([]string{"a", "b"})
	d.Switch("a")
	barrier(t, a) <- nil
	wait(t, func() bool { return d.View().Target == "a" })
	d.Input(input.Event{Device: "kbd", Kind: input.Key, Code: 4, Down: true})
	select {
	case m := <-a.sent:
		if m.GetKb().KeysLo != 16 {
			t.Fatal(m)
		}
	case <-time.After(time.Second):
		t.Fatal("input missing")
	}
	d.Switch("b")
	old := barrier(t, a)
	// A pressed Shift during the asynchronous barrier remains physical and suppressed.
	if !d.Input(input.Event{Device: "kbd", Kind: input.Key, Code: 0xe1, Down: true}) {
		t.Fatal("blocked input")
	}
	wait(t, func() bool { return d.View().Phase == "old-release" })
	old <- nil
	barrier(t, b) <- nil
	wait(t, func() bool { return d.View().Target == "b" })
	d.Input(input.Event{Device: "kbd", Kind: input.Key, Code: 5, Down: true})
	select {
	case m := <-b.sent:
		if m.GetKb().Mods != 0 || m.GetKb().KeysLo != 32 {
			t.Fatal("held state leaked", m)
		}
	case <-time.After(time.Second):
		t.Fatal("new input missing")
	}
	d.Detach("b", b)
	wait(t, func() bool { return d.View().Target == Local })
	if c.remote.Load() {
		t.Fatal("local gate not restored")
	}
}
func TestLocalCancelsPendingActivation(t *testing.T) {
	c := &capture{}
	d := New(c)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx)
	a := newEndpoint()
	d.Attach("a", a, true)
	d.Switch("a")
	pending := barrier(t, a)
	d.Switch(Local)
	wait(t, func() bool { return d.View().Phase == "idle" })
	pending <- nil
	time.Sleep(10 * time.Millisecond)
	if d.View().Target != Local || c.remote.Load() {
		t.Fatal("late completion changed target")
	}
}
func TestLinuxWaitDoesNotBlockPhysicalRelease(t *testing.T) {
	c := &capture{}
	c.wait.Store(true)
	d := New(c)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx)
	a := newEndpoint()
	d.Attach("a", a, true)
	d.Switch("a")
	barrier(t, a) <- nil
	wait(t, func() bool { return d.View().Phase == "waiting-release" })
	c.wait.Store(false)
	d.Input(input.Event{Device: "kbd", Kind: input.Key, Code: 4, Down: false})
	wait(t, func() bool { return d.View().Target == "a" })
}
func TestHotkeyRecordingDoesNotSwitch(t *testing.T) {
	c := &capture{}
	d := New(c)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx)
	a := newEndpoint()
	d.Attach("a", a, true)
	d.Order([]string{"a"})
	d.Recording(true)
	wait(t, func() bool { return d.View().Recording })
	combo := func() {
		for _, down := range []bool{true, false} {
			for _, code := range []uint16{0xe0, 0xe4} {
				d.Input(input.Event{Device: "kbd", Kind: input.Key, Code: code, Down: down})
			}
		}
	}
	combo()
	wait(t, func() bool { return len(d.events) == 0 })
	d.Recording(false)
	wait(t, func() bool { return !d.View().Recording })
	select {
	case <-a.barriers:
		t.Fatal("recording triggered a switch")
	default:
	}
	combo()
	barrier(t, a) <- nil
	wait(t, func() bool { return d.View().Target == "a" })
}
