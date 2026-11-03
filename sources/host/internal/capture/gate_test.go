package capture

import (
	"github.com/jjundot/EspKVM/internal/input"
	"testing"
)

func TestHookMakesSynchronousHotkeyDecision(t *testing.T) {
	var g routing
	event := func(code uint16, down bool) bool {
		return g.observe(input.Event{Device: "hook", Kind: input.Key, Code: code, Down: down})
	}
	if event(0xe0, true) || event(0xe2, true) {
		t.Fatal("shortcut modifiers were delayed")
	}
	if !event(0x1a, true) {
		t.Fatal("completing Ctrl+Alt+W was not swallowed")
	}
	if !event(4, true) || !event(0xe0, false) || !event(0xe2, false) || !event(0x1a, false) {
		t.Fatal("combination suppression ended early")
	}
	if !event(4, false) {
		t.Fatal("key held during switch was released to desktop")
	}
	if event(5, true) || event(5, false) {
		t.Fatal("ordinary LOCAL input not restored")
	}
}
func TestLocalFaultDoesNotSwallowNativeKeyRelease(t *testing.T) {
	var g routing
	g.observe(input.Event{Device: "hook", Kind: input.Key, Code: 4, Down: true})
	g.local()
	if g.observe(input.Event{Device: "hook", Kind: input.Key, Code: 4, Down: false}) {
		t.Fatal("native key left held")
	}
}
