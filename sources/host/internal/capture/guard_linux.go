//go:build linux && (amd64 || arm64)

package capture

import (
	"encoding/binary"
	"errors"
	"fmt"
	"github.com/jjundot/EspKVM/internal/input"
	"golang.org/x/sys/unix"
	"unsafe"
)

// Mouse guard (optional): in LOCAL the guardable mice are grabbed as well and
// re-injected through one uinput device each, so Ctrl+side button can be held
// back from the desktop. Everything else is forwarded unchanged.

const (
	keyLeftCtrl, keyRightCtrl = 29, 97
	btnSide, btnExtra         = 0x113, 0x114
)

var openVirtual = createVirtual
var writeVirtual = func(fd int, b []byte) error {
	_, e := unix.Write(fd, b)
	return e
}
var closeVirtual = func(fd int) {
	ioctl(fd, 0x5502, 0) // UI_DEV_DESTROY; the kernel releases whatever it held.
	unix.Close(fd)
}

func keyBit(keys *[96]byte, code uint16) bool { return keys[code/8]&(1<<(code%8)) != 0 }

// createVirtual mirrors the physical mouse's buttons, axes and id. The name
// keeps the "EspKVM" prefix that scan excludes.
func createVirtual(d *linuxDevice) (int, error) {
	fd, e := unix.Open("/dev/uinput", unix.O_WRONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if e != nil {
		return 0, fmt.Errorf("open /dev/uinput: %w", e)
	}
	fail := func(e error) (int, error) {
		unix.Close(fd)
		return 0, fmt.Errorf("create uinput mouse: %w", e)
	}
	for _, kind := range []uintptr{1, 2} {
		if e = ioctl(fd, 0x40045564, kind); e != nil { // UI_SET_EVBIT
			return fail(e)
		}
	}
	for code := uint16(0); code < 768; code++ {
		if keyBit(&d.supported, code) {
			if e = ioctl(fd, 0x40045565, uintptr(code)); e != nil { // UI_SET_KEYBIT
				return fail(e)
			}
		}
	}
	for code := uint16(0); code < 16; code++ {
		if d.rel[code/8]&(1<<(code%8)) != 0 {
			if e = ioctl(fd, 0x40045566, uintptr(code)); e != nil { // UI_SET_RELBIT
				return fail(e)
			}
		}
	}
	var setup [92]byte // struct uinput_setup: input_id, name[80], ff_effects_max
	copy(setup[:8], d.id[:])
	name := "EspKVM " + d.name
	if len(name) > 79 {
		name = name[:79]
	}
	copy(setup[8:], name)
	if e = ioctlBuffer(fd, 0x405c5503, unsafe.Pointer(&setup[0])); e != nil { // UI_DEV_SETUP
		return fail(e)
	}
	if e = ioctl(fd, 0x5501, 0); e != nil { // UI_DEV_CREATE
		return fail(e)
	}
	return fd, nil
}

// forward queues one event for the virtual device and tracks what it holds.
func (d *linuxDevice) forward(kind, code uint16, value int32) {
	if kind == 1 && code < 768 {
		if value != 0 {
			d.vkeys[code/8] |= 1 << (code % 8)
		} else {
			d.vkeys[code/8] &^= 1 << (code % 8)
		}
	}
	var raw [24]byte // Zero time: the kernel stamps injected events.
	binary.LittleEndian.PutUint16(raw[16:], kind)
	binary.LittleEndian.PutUint16(raw[18:], code)
	binary.LittleEndian.PutUint32(raw[20:], uint32(value))
	d.out = append(d.out, raw[:]...)
}
func (s *linuxSource) flushVirtual(d *linuxDevice) {
	if len(d.out) == 0 {
		return
	}
	out := d.out
	d.out = d.out[:0]
	if d.virtual <= 0 {
		return
	}
	if e := writeVirtual(d.virtual, out); e != nil {
		s.dropGuard(d, e)
	}
}

// alignVirtual makes the virtual device hold exactly target, e.g. after
// SYN_DROPPED or before giving the physical device back to the desktop.
func (s *linuxSource) alignVirtual(d *linuxDevice, target [96]byte) {
	if d.virtual <= 0 {
		return
	}
	changed := false
	for code := uint16(0); code < 768; code++ {
		if want := keyBit(&target, code); keyBit(&d.vkeys, code) != want {
			value := int32(0)
			if want {
				value = 1
			}
			d.forward(1, code, value)
			changed = true
		}
	}
	if changed {
		d.forward(0, 0, 0)
	}
	s.flushVirtual(d)
}

// passThrough is what the virtual device should hold: the physical state
// without side buttons that are part of a Ctrl chord.
func (s *linuxSource) passThrough(d *linuxDevice) [96]byte {
	keys := d.keys
	for i, code := range []uint16{btnSide, btnExtra} {
		if s.chordButtons&(1<<i) != 0 {
			keys[code/8] &^= 1 << (code % 8)
		}
	}
	return keys
}

// unguard hands a LOCAL pass-through device back to the desktop.
func (s *linuxSource) unguard(d *linuxDevice) {
	if !d.guarded {
		return
	}
	s.alignVirtual(d, [96]byte{})
	grabDevice(d, false)
	d.grabbed, d.guarded = false, false
}
func (s *linuxSource) dropGuard(d *linuxDevice, e error) {
	if !s.remote {
		grabDevice(d, false)
		d.grabbed = false
	}
	d.guarded, d.guardable = false, false
	closeVirtual(d.virtual)
	d.virtual = 0
	d.out = nil
	d.vkeys = [96]byte{}
	s.guardWarning = "mouse guard disabled for " + d.label() + ": " + e.Error()
}
func (s *linuxSource) releaseVirtual(d *linuxDevice) {
	if d.virtual > 0 {
		closeVirtual(d.virtual)
		d.virtual = 0
	}
	d.out = nil
	d.vkeys = [96]byte{}
}

// chord decides at read time whether a side button belongs to Ctrl+side
// button. Ctrl comes from the devices' raw state, which is newer than routing.
func (s *linuxSource) chord(code uint16, down bool) bool {
	mask := uint8(1 << (code - btnSide))
	if down {
		for _, d := range s.devices {
			if keyBit(&d.keys, keyLeftCtrl) || keyBit(&d.keys, keyRightCtrl) {
				s.chordButtons |= mask
				return true
			}
		}
		return false
	}
	if s.chordButtons&mask != 0 {
		s.chordButtons &^= mask
		return true
	}
	return false
}

// guardPending grabs released guardable mice for LOCAL pass-through.
func (s *linuxSource) guardPending() []input.Event {
	if !s.guard || s.guardFailed || s.remote || s.requested {
		return nil
	}
	var events []input.Event
	for _, d := range s.devices {
		if !d.guardable || d.grabbed || d.foreign || !d.boundary || d.dropped || anyKey(d.keys) {
			continue
		}
		if d.virtual <= 0 {
			fd, e := openVirtual(d)
			if e != nil {
				// Usually a missing uinput permission; do not retry every poll.
				s.guardFailed = true
				s.guardWarning = "mouse guard unavailable: " + e.Error()
				return events
			}
			d.virtual = fd
		}
		if e := grabDevice(d, true); e != nil {
			if errors.Is(e, unix.EBUSY) {
				d.foreign = true
				s.updateWarning()
			} else {
				d.guardable = false
			}
			s.releaseVirtual(d)
			continue
		}
		d.grabbed, d.guarded = true, true
		// Queued events reached the desktop before the grab.
		if e := discardQueued(d); e != nil {
			s.dropGuard(d, e)
			continue
		}
		keys, e := queryKeys(d)
		if e != nil {
			s.dropGuard(d, e)
			continue
		}
		events = append(events, syncDevice(d, keys, true)...)
		d.delivered = d.keys
		if anyKey(keys) {
			// Pressed after the grab: the desktop only sees its release.
			s.unguard(d)
		}
	}
	return events
}

// SetMouseGuard turns LOCAL pass-through on or off.
func (s *linuxSource) SetMouseGuard(on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.guard = on
	s.guardFailed = false
	s.guardWarning = ""
	if on {
		return
	}
	for _, d := range s.devices {
		if s.remote {
			d.guarded = false // REMOTE keeps its own grab; the virtual device holds nothing.
		} else {
			s.unguard(d)
		}
		s.releaseVirtual(d)
	}
}

// MouseGuardWarning reports why the guard is not in effect.
func (s *linuxSource) MouseGuardWarning() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.guardWarning
}
