// Package input keeps physical state separate from forwarded state.
package input

import (
	"github.com/jjundot/EspKVM/internal/protocol/pb"
	"time"
)

// DefaultHotkey returns the default local input target switch shortcut:
// left Ctrl + left Alt + W.
func DefaultHotkey() []uint16 { return []uint16{0xe0, 0xe2, 0x1a} }

type Kind uint8

const (
	Key Kind = iota
	Button
	Motion
	Consumer
	Removed
	Fault
	Boundary
)

type Event struct {
	Captured           time.Time
	Suppressed         bool
	Reset              bool
	Resync             bool
	Synthetic          bool
	Epoch              uint64
	Device             string
	Kind               Kind
	Code               uint16
	Down               bool
	DX, DY, Wheel, Pan int32
}
type State struct {
	devices map[string]map[uint32]bool
	refs    map[uint32]uint16
}

func tag(kind Kind, code uint16) uint32 { return uint32(kind)<<16 | uint32(code) }

// Apply ignores repeats and returns whether a state transition occurred.
func (s *State) Apply(e Event) bool {
	if e.Kind == Fault || e.Kind == Boundary {
		return false
	}
	if s.devices == nil {
		s.devices = map[string]map[uint32]bool{}
		s.refs = map[uint32]uint16{}
	}
	if e.Kind == Removed {
		changed := false
		for k := range s.devices[e.Device] {
			if s.refs[k] > 0 {
				s.refs[k]--
				changed = true
			}
			if s.refs[k] == 0 {
				delete(s.refs, k)
			}
		}
		delete(s.devices, e.Device)
		return changed
	}
	if e.Kind == Motion {
		return true
	}
	if (e.Kind == Key && (e.Code < 4 || e.Code > 255)) || (e.Kind == Button && e.Code >= 5) {
		return false
	}
	if s.devices[e.Device] == nil {
		s.devices[e.Device] = map[uint32]bool{}
	}
	k := tag(e.Kind, e.Code)
	held := s.devices[e.Device][k]
	if held == e.Down {
		return false
	}
	before := s.refs[k]
	if e.Down {
		s.devices[e.Device][k] = true
		s.refs[k]++
	} else {
		delete(s.devices[e.Device], k)
		s.refs[k]--
		if s.refs[k] == 0 {
			delete(s.refs, k)
		}
	}
	return (before == 0) != (s.refs[k] == 0)
}
func (s *State) Held(kind Kind, code uint16) bool { return s.refs[tag(kind, code)] > 0 }
func (s *State) Released() bool                   { return len(s.refs) == 0 }
func (s *State) HeldKeys() []uint16 {
	var keys []uint16
	for code := uint16(4); code < 256; code++ {
		if s.Held(Key, code) {
			keys = append(keys, code)
		}
	}
	return keys
}
func (s *State) Buttons() uint32 {
	var buttons uint32
	for i := uint16(0); i < 5; i++ {
		if s.Held(Button, i) {
			buttons |= 1 << i
		}
	}
	return buttons
}
func (s *State) Consumer(exclude map[uint32]bool) uint32 {
	var usage uint32
	for k := range s.refs {
		if Kind(k>>16) == Consumer && !exclude[k] && (usage == 0 || uint32(uint16(k)) < usage) {
			usage = uint32(uint16(k))
		}
	}
	return usage
}
func (s *State) Keyboard(exclude map[uint32]bool) *pb.KbState {
	k := &pb.KbState{}
	words := [4]uint64{}
	for _, code := range s.HeldKeys() {
		if exclude[tag(Key, code)] {
			continue
		}
		if code >= 0xe0 && code <= 0xe7 {
			k.Mods |= 1 << (code - 0xe0)
		} else {
			words[code/64] |= 1 << (code % 64)
		}
	}
	k.KeysLo = words[0]
	k.KeysHi = words[1]
	k.Keys_128_191 = words[2]
	k.Keys_192_255 = words[3]
	return k
}
func Token(kind Kind, code uint16) uint32 { return tag(kind, code) }
func (s *State) HeldTokens() []uint32 {
	out := make([]uint32, 0, len(s.refs))
	for k := range s.refs {
		out = append(out, k)
	}
	return out
}

type Hotkey struct {
	keys    []uint16
	armed   bool
	waiting bool
	all     bool
}

func NewHotkey(keys []uint16) (*Hotkey, bool) {
	if len(keys) < 2 || len(keys) > 8 {
		return nil, false
	}
	seen := map[uint16]bool{}
	for _, k := range keys {
		if k < 4 || k > 255 || seen[k] {
			return nil, false
		}
		seen[k] = true
	}
	return &Hotkey{keys: append([]uint16(nil), keys...), armed: true}, true
}

// Observe runs synchronously on the capture thread when swallowing is supported.
// Linux LOCAL still allows the desktop to see the entire combination.
func (h *Hotkey) Observe(s *State, recording bool) bool {
	all, any := true, false
	combo := map[uint16]bool{}
	for _, k := range h.keys {
		held := s.Held(Key, k)
		all = all && held
		any = any || held
		combo[k] = true
	}
	if !any {
		h.armed = true
		h.waiting = false
	}
	wasAll := h.all
	h.all = all
	if recording || !h.armed || !all || wasAll || s.Buttons() != 0 {
		return false
	}
	for _, k := range s.HeldKeys() {
		if !combo[k] {
			return false
		}
	}
	for _, k := range s.HeldTokens() {
		if Kind(k>>16) == Consumer {
			return false
		}
	}
	h.armed = false
	h.waiting = true
	return true
}
func (h *Hotkey) Waiting() bool { return h.waiting }
func (h *Hotkey) Disarm()       { h.armed = false; h.waiting = false }
