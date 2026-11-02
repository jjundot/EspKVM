//go:build linux && amd64

package capture

import (
	"errors"
	"testing"
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
