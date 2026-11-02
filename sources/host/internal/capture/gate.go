// Package capture adapts physical input; routing decisions stay synchronous on hooks.
package capture

import (
	"github.com/jjundot/EspKVM/internal/input"
	"sync"
	"sync/atomic"
)

type routing struct {
	epoch              atomic.Uint64
	mu                 sync.Mutex
	physical           input.State
	hotkey             *input.Hotkey
	remote, transition bool
	recording          bool
	suppressed         map[uint32]bool
}

func (g *routing) init() {
	if g.hotkey == nil {
		g.hotkey, _ = input.NewHotkey([]uint16{0xe0, 0xe4})
	}
	if g.suppressed == nil {
		g.suppressed = map[uint32]bool{}
	}
}
func (g *routing) observe(e input.Event) bool {
	return g.routeEvent(&e)
}
func (g *routing) routeEvent(e *input.Event) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.init()
	wasWaiting := g.hotkey.Waiting()
	g.physical.Apply(*e)
	trigger := g.hotkey.Observe(&g.physical, e.Resync || g.recording)
	token := input.Token(e.Kind, e.Code)
	swallow := g.remote || g.transition || wasWaiting || trigger || g.suppressed[token]
	e.Suppressed = g.transition || wasWaiting || trigger || g.suppressed[token]
	if trigger || g.transition || g.hotkey.Waiting() {
		for _, k := range g.physical.HeldTokens() {
			g.suppressed[k] = true
		}
	}
	if !e.Down && !g.physical.Held(e.Kind, e.Code) {
		delete(g.suppressed, token)
	}
	return swallow
}
func (g *routing) local() {
	g.epoch.Add(1)
	g.mu.Lock()
	defer g.mu.Unlock()
	g.init()
	if g.remote || g.transition {
		for _, k := range g.physical.HeldTokens() {
			g.suppressed[k] = true
		}
	}
	g.remote = false
	g.transition = false
}
func (g *routing) begin() {
	g.epoch.Add(1)
	g.mu.Lock()
	defer g.mu.Unlock()
	g.init()
	g.transition = true
	for _, k := range g.physical.HeldTokens() {
		g.suppressed[k] = true
	}
}
func (g *routing) activate() {
	g.mu.Lock()
	g.epoch.Add(1)
	g.remote = true
	g.transition = false
	g.mu.Unlock()
}
func (g *routing) abortLocal() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.init()
	g.remote = false
	g.transition = false
	g.suppressed = map[uint32]bool{}
	g.hotkey.Disarm()
}
func (g *routing) setHotkey(keys []uint16) error {
	h, ok := input.NewHotkey(keys)
	if !ok {
		return ErrHotkey
	}
	g.mu.Lock()
	h.Disarm()
	h.Observe(&g.physical, false)
	g.hotkey = h
	g.mu.Unlock()
	return nil
}
func (g *routing) resetPhysical() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.init()
	g.physical = input.State{}
	g.suppressed = map[uint32]bool{}
	g.hotkey.Disarm()
	g.hotkey.Observe(&g.physical, false)
}
func (g *routing) setRecording(active bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.init()
	g.recording = active
	g.hotkey.Disarm()
	g.hotkey.Observe(&g.physical, active)
}
