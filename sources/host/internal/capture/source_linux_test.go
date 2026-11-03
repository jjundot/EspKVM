//go:build linux && amd64

package capture

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jjundot/EspKVM/internal/input"
	"golang.org/x/sys/unix"
)

func mockDevices(t *testing.T) (*linuxSource, *int) {
	t.Helper()
	oldQuery, oldGrab := queryKeys, grabDevice
	oldDiscard := discardQueued
	discardQueued = func(*linuxDevice) error { return nil }
	t.Cleanup(func() { queryKeys = oldQuery; grabDevice = oldGrab })
	t.Cleanup(func() { discardQueued = oldDiscard })
	queryKeys = func(*linuxDevice) ([96]byte, error) { return [96]byte{}, nil }
	released := new(int)
	grabDevice = func(d *linuxDevice, grab bool) error {
		if !grab {
			*released++
		}
		return nil
	}
	return &linuxSource{devices: map[string]*linuxDevice{"a": {path: "a", boundary: true}, "b": {path: "b", boundary: true}}}, released
}
func TestGrabRequiresBatchBoundaryAndDispatcherConfirmation(t *testing.T) {
	s, _ := mockDevices(t)
	s.devices["a"].boundary = false
	if ready, e := s.Remote(); ready || e != nil {
		t.Fatal(ready, e)
	}
	if ready, e := s.tryGrab(); ready || e != nil {
		t.Fatal("grabbed between SYN_REPORT boundaries", ready, e)
	}
	s.devices["a"].boundary = true
	if ready, e := s.tryGrab(); !ready || e != nil {
		t.Fatal(ready, e)
	}
	if ready, _ := s.Remote(); ready {
		t.Fatal("pre-boundary queued events could enter remote")
	}
	s.ConfirmBoundary(s.epoch - 1)
	if ready, _ := s.Remote(); ready {
		t.Fatal("stale boundary accepted")
	}
	s.ConfirmBoundary(s.epoch)
	if ready, e := s.Remote(); !ready || e != nil {
		t.Fatal(ready, e)
	}
}
func TestGrabFailureRollsBackEntireSet(t *testing.T) {
	s, released := mockDevices(t)
	count := 0
	grabDevice = func(d *linuxDevice, grab bool) error {
		if !grab {
			*released++
			return nil
		}
		count++
		if count == 2 {
			return errors.New("busy")
		}
		return nil
	}
	s.Remote()
	if ready, e := s.tryGrab(); ready || e == nil {
		t.Fatal("failure lost", ready, e)
	}
	if *released != 1 || s.remote {
		t.Fatal("partial grab retained")
	}
	for _, d := range s.devices {
		if d.grabbed {
			t.Fatal("FD still grabbed")
		}
	}
}
func TestPhysicalRaceAfterGrabRollsBack(t *testing.T) {
	s, released := mockDevices(t)
	queries := 0
	queryKeys = func(*linuxDevice) ([96]byte, error) {
		queries++
		keys := [96]byte{}
		if queries > 2 {
			keys[1] = 1
		}
		return keys, nil
	}
	s.Remote()
	if ready, e := s.tryGrab(); ready || e == nil {
		t.Fatal(ready, e)
	}
	if s.remote || *released != 2 {
		t.Fatal("physical race retained grabs")
	}
}
func TestOnlySupportedKeyboardsAndRelativeMiceAreSelected(t *testing.T) {
	events := [4]byte{2}
	keys := [96]byte{}
	keys[30/8] |= 1 << (30 % 8)
	if !supportedDevice(events, keys) {
		t.Fatal("keyboard rejected")
	}
	keys = [96]byte{}
	keys[0x110/8] |= 1 << (0x110 % 8)
	if supportedDevice(events, keys) {
		t.Fatal("absolute touch device treated as relative mouse")
	}
	events[0] |= 4
	if !supportedDevice(events, keys) {
		t.Fatal("relative mouse rejected")
	}
	keys = [96]byte{}
	keys[0x130/8] |= 1 << (0x130 % 8)
	if supportedDevice(events, keys) {
		t.Fatal("gamepad grabbed")
	}
}
func TestNormalLocalWaitsButFaultLocalDoesNot(t *testing.T) {
	s, released := mockDevices(t)
	s.remote = true
	for _, d := range s.devices {
		d.grabbed = true
	}
	s.devices["a"].keys[1] = 1
	if ready, e := s.TryLocal(); ready || e != nil {
		t.Fatal(ready, e)
	}
	if !s.remote || *released != 0 {
		t.Fatal("normal LOCAL released held devices")
	}
	s.Local()
	if s.remote || *released != 2 {
		t.Fatal("fault LOCAL waited for physical release")
	}
}
func TestExclusiveHolderIsSkippedNotFatal(t *testing.T) {
	s, _ := mockDevices(t)
	s.devices["b"].name = "keyd virtual keyboard"
	grabDevice = func(d *linuxDevice, grab bool) error {
		if grab && d.path == "b" {
			return unix.EBUSY
		}
		return nil
	}
	s.Remote()
	if ready, e := s.tryGrab(); !ready || e != nil {
		t.Fatal("exclusive holder aborted the switch", ready, e)
	}
	if !s.devices["a"].grabbed || s.devices["b"].grabbed || !s.devices["b"].foreign {
		t.Fatal("wrong grab set")
	}
	if !strings.Contains(s.Warning(), "keyd virtual keyboard") {
		t.Fatal("exclusive device not reported", s.Warning())
	}
	// Once known, keys typed into the exclusive holder block neither direction.
	queryKeys = func(d *linuxDevice) ([96]byte, error) {
		keys := [96]byte{}
		if d.path == "b" {
			keys[1] = 1
		}
		return keys, nil
	}
	s.devices["b"].keys[1] = 1
	if ready, e := s.TryLocal(); !ready || e != nil || s.Warning() != "" {
		t.Fatal("foreign keys blocked LOCAL", ready, e)
	}
	s.Remote()
	if ready, e := s.tryGrab(); !ready || e != nil || !s.devices["b"].foreign {
		t.Fatal("foreign keys blocked REMOTE", ready, e)
	}
}
func TestHotplugAndRemovalKeepRemote(t *testing.T) {
	s, _ := mockDevices(t)
	for _, d := range s.devices {
		d.fd = -1
	}
	s.Remote()
	if ready, e := s.tryGrab(); !ready || e != nil {
		t.Fatal(ready, e)
	}
	held := &linuxDevice{fd: -1, path: "c", boundary: true, grabLater: true}
	held.keys[1] = 1
	s.devices["c"] = held
	s.grabPending()
	if !s.remote || held.grabbed || !held.grabLater {
		t.Fatal("device with held keys grabbed mid-press, or REMOTE lost")
	}
	held.keys = [96]byte{}
	s.grabPending()
	if !s.remote || !held.grabbed || held.grabLater {
		t.Fatal("released hot-plugged device not grabbed")
	}
	if e := s.remove(s.devices["a"]); e.Kind != input.Removed || !s.remote || s.devices["a"] != nil {
		t.Fatal("removal left REMOTE", e)
	}
}

// feed runs one evdev event plus SYN_REPORT through the reader logic.
func feed(t *testing.T, s *linuxSource, d *linuxDevice, kind, code uint16, value int32) []input.Event {
	t.Helper()
	first, e := s.handleRaw(d, kind, code, value)
	if e != nil {
		t.Fatal(e)
	}
	batch, e := s.handleRaw(d, 0, 0, 0)
	if e != nil {
		t.Fatal(e)
	}
	return append(first, batch...)
}
func shortcuts(events []input.Event) []uint16 {
	var codes []uint16
	for _, e := range events {
		if e.Kind == input.Shortcut {
			codes = append(codes, e.Code)
		}
	}
	return codes
}
func TestControlSideButtonCyclesAndIsSuppressed(t *testing.T) {
	kbd, mouse := &linuxDevice{path: "kbd", boundary: true}, &linuxDevice{path: "mouse", boundary: true}
	s := &linuxSource{devices: map[string]*linuxDevice{"kbd": kbd, "mouse": mouse}}
	plain := feed(t, s, mouse, 1, btnSide, 1)
	if len(shortcuts(plain)) != 0 || plain[0].Suppressed {
		t.Fatal("side button without Ctrl switched", plain)
	}
	feed(t, s, mouse, 1, btnSide, 0)
	feed(t, s, kbd, 1, keyRightCtrl, 1)
	for code, direction := range map[uint16]uint16{btnSide: 1, btnExtra: 0} {
		down := feed(t, s, mouse, 1, code, 1)
		if got := shortcuts(down); len(got) != 1 || got[0] != direction || !down[0].Suppressed {
			t.Fatal("Ctrl+side button did not cycle", code, down)
		}
		feed(t, s, kbd, 1, keyRightCtrl, 0) // Releasing Ctrl first must not leak the button release.
		up := feed(t, s, mouse, 1, code, 0)
		if len(shortcuts(up)) != 0 || !up[0].Suppressed {
			t.Fatal("chord release leaked or cycled", code, up)
		}
		feed(t, s, kbd, 1, keyRightCtrl, 1)
	}
	s.route.setRecording(true)
	if down := feed(t, s, mouse, 1, btnExtra, 1); len(shortcuts(down)) != 0 || !down[0].Suppressed {
		t.Fatal("chord cycled while recording", down)
	}
}

// mockVirtual records what the guard writes to uinput.
func mockVirtual(t *testing.T) (*[][3]int32, *int) {
	t.Helper()
	oldOpen, oldWrite, oldClose := openVirtual, writeVirtual, closeVirtual
	t.Cleanup(func() { openVirtual, writeVirtual, closeVirtual = oldOpen, oldWrite, oldClose })
	written, closed := new([][3]int32), new(int)
	openVirtual = func(*linuxDevice) (int, error) { return 99, nil }
	writeVirtual = func(fd int, b []byte) error {
		for i := 0; i+24 <= len(b); i += 24 {
			*written = append(*written, [3]int32{int32(binary.LittleEndian.Uint16(b[i+16:])), int32(binary.LittleEndian.Uint16(b[i+18:])), int32(binary.LittleEndian.Uint32(b[i+20:]))})
		}
		return nil
	}
	closeVirtual = func(int) { *closed++ }
	return written, closed
}
func TestMouseGuardPassesThroughAllButChord(t *testing.T) {
	s, released := mockDevices(t)
	written, closed := mockVirtual(t)
	kbd, mouse := s.devices["a"], s.devices["b"]
	mouse.guardable = true
	s.guard = true
	s.guardPending()
	if !mouse.grabbed || !mouse.guarded || mouse.virtual != 99 || kbd.grabbed {
		t.Fatal("guardable mouse not taken for pass-through")
	}
	feed(t, s, mouse, 2, 0, 5)
	feed(t, s, mouse, 1, 0x110, 1)
	s.flushVirtual(mouse)
	want := [][3]int32{{2, 0, 5}, {0, 0, 0}, {1, 0x110, 1}, {0, 0, 0}}
	if fmt.Sprint(*written) != fmt.Sprint(want) {
		t.Fatal("pass-through", *written)
	}
	*written = nil
	feed(t, s, kbd, 1, keyLeftCtrl, 1)
	feed(t, s, mouse, 1, btnExtra, 1)
	feed(t, s, mouse, 1, btnExtra, 0)
	s.flushVirtual(mouse)
	for _, e := range *written {
		if e[0] == 1 && e[1] == btnExtra {
			t.Fatal("chord side button reached the desktop", *written)
		}
	}
	*written = nil
	s.localLocked() // Fault path: the left button held on the virtual device is released.
	if mouse.grabbed || mouse.guarded || *released != 1 || fmt.Sprint(*written) != fmt.Sprint([][3]int32{{1, 0x110, 0}, {0, 0, 0}}) {
		t.Fatal("local did not release the virtual device", *written, *released)
	}
	feed(t, s, mouse, 1, 0x110, 0)
	feed(t, s, kbd, 1, keyLeftCtrl, 0)
	s.guardPending()
	if !mouse.guarded {
		t.Fatal("pass-through not resumed after release")
	}
	s.SetMouseGuard(false)
	if mouse.grabbed || mouse.guarded || mouse.virtual != 0 || *closed != 1 {
		t.Fatal("disabling the guard kept the mouse")
	}
}
func TestRemoteReusesGuardGrab(t *testing.T) {
	s, _ := mockDevices(t)
	mockVirtual(t)
	mouse := s.devices["b"]
	mouse.guardable = true
	s.guard = true
	s.guardPending()
	grabDevice = func(d *linuxDevice, grab bool) error {
		if grab && d.grabbed {
			return unix.EBUSY
		}
		return nil
	}
	s.Remote()
	if ready, e := s.tryGrab(); !ready || e != nil || mouse.foreign {
		t.Fatal("REMOTE grab failed on a guarded mouse", ready, e)
	}
	feed(t, s, mouse, 2, 0, 5)
	if len(mouse.out) != 0 {
		t.Fatal("REMOTE input passed through to the desktop")
	}
}
