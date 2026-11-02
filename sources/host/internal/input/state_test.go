package input

import "testing"

func TestPhysicalReferencesAndHotkey(t *testing.T) {
	var s State
	h, _ := NewHotkey([]uint16{0xe0, 0xe4})
	apply := func(device string, key uint16, down bool) bool {
		s.Apply(Event{Device: device, Kind: Key, Code: key, Down: down})
		return h.Observe(&s, false)
	}
	if apply("a", 0xe0, true) || apply("b", 0xe0, true) {
		t.Fatal("early trigger")
	}
	if !apply("a", 0xe4, true) {
		t.Fatal("no hotkey")
	}
	if apply("a", 0xe4, true) {
		t.Fatal("repeat retriggered")
	}
	apply("a", 0xe0, false)
	if !s.Held(Key, 0xe0) {
		t.Fatal("second keyboard contribution lost")
	}
	s.Apply(Event{Device: "b", Kind: Removed})
	h.Observe(&s, false)
	apply("a", 0xe4, false)
	if h.Waiting() || !s.Released() {
		t.Fatal("did not rearm")
	}
	apply("a", 4, true)
	apply("a", 0xe0, true)
	if apply("a", 0xe4, true) {
		t.Fatal("outside key allowed hotkey")
	}
	if apply("a", 4, false) {
		t.Fatal("outside key release triggered the already-held combination")
	}
}
func TestModifierBitmapAndSuppression(t *testing.T) {
	var s State
	for _, k := range []uint16{4, 0xe0, 0xe4} {
		s.Apply(Event{Device: "a", Kind: Key, Code: k, Down: true})
	}
	k := s.Keyboard(map[uint32]bool{Token(Key, 0xe4): true})
	if k.Mods != 1 || k.KeysLo != 16 || k.Keys_192_255 != 0 {
		t.Fatal(k)
	}
}
