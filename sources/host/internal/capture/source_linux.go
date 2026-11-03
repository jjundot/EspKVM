//go:build linux && (amd64 || arm64)

package capture

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"github.com/jjundot/EspKVM/internal/input"
	"github.com/jjundot/EspKVM/internal/keymap"
	"golang.org/x/sys/unix"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unsafe"
)

type linuxDevice struct {
	fd               int
	path, name       string
	keys             [96]byte
	delivered        [96]byte
	pending          []input.Event
	dropped, grabbed bool
	boundary         bool
	// foreign: another program holds EVIOCGRAB (keyd, input-remapper, a VM), so
	// neither we nor the desktop receive its events. grabLater: it appeared in
	// REMOTE with keys held and is grabbed at its next all-released boundary.
	foreign, grabLater bool
}
type linuxSource struct {
	boundaryEvents       []input.Event
	mu                   sync.Mutex
	devices              map[string]*linuxDevice
	remote               bool
	requested, confirmed bool
	epoch                uint64
	warning              string
	route                routing
}

func New() Source                                    { return &linuxSource{devices: map[string]*linuxDevice{}} }
func (s *linuxSource) SetHotkey(keys []uint16) error { return s.route.setHotkey(keys) }
func (s *linuxSource) SetRecording(active bool)      { s.route.setRecording(active) }
func ioctl(fd int, request uintptr, ptr uintptr) error {
	_, _, e := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), request, ptr)
	if e != 0 {
		return e
	}
	return nil
}
func ioctlBuffer(fd int, request uintptr, buffer unsafe.Pointer) error {
	_, _, e := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), request, uintptr(buffer))
	runtime.KeepAlive(buffer)
	if e != 0 {
		return e
	}
	return nil
}
func readKeys(d *linuxDevice) ([96]byte, error) {
	var keys [96]byte
	e := ioctlBuffer(d.fd, uintptr(2<<30|96<<16|int('E')<<8|0x18), unsafe.Pointer(&keys[0]))
	return keys, e
}
func supportedDevice(events [4]byte, keys [96]byte) bool {
	if events[0]&2 == 0 {
		return false
	}
	if events[0]&4 != 0 && keys[0x110/8]&(1<<(0x110%8)) != 0 {
		return true
	}
	for code := uint16(0); code < 256; code++ {
		if keys[code/8]&(1<<(code%8)) == 0 {
			continue
		}
		if _, ok := keymap.Linux(code); ok {
			return true
		}
		if _, ok := keymap.ConsumerLinux(code); ok {
			return true
		}
	}
	return false
}
func linuxEvent(path string, code uint16, down bool) (input.Event, bool) {
	e := input.Event{Device: path, Down: down}
	if code >= 0x110 && code <= 0x114 {
		e.Kind = input.Button
		e.Code = code - 0x110
		return e, true
	}
	if k, ok := keymap.Linux(code); ok {
		e.Kind = input.Key
		e.Code = k
		return e, true
	}
	if k, ok := keymap.ConsumerLinux(code); ok {
		e.Kind = input.Consumer
		e.Code = k
		return e, true
	}
	return e, false
}
func syncDevice(d *linuxDevice, keys [96]byte, resync bool) []input.Event {
	var events []input.Event
	for code := uint16(0); code < 768; code++ {
		old := d.keys[code/8]&(1<<(code%8)) != 0
		down := keys[code/8]&(1<<(code%8)) != 0
		if old != down {
			if e, ok := linuxEvent(d.path, code, down); ok {
				e.Resync = resync
				events = append(events, e)
			}
		}
	}
	d.keys = keys
	return events
}
func anyKey(keys [96]byte) bool {
	for _, b := range keys {
		if b != 0 {
			return true
		}
	}
	return false
}
func (s *linuxSource) localLocked() {
	for _, d := range s.devices {
		if d.grabbed {
			grabDevice(d, false)
			d.grabbed = false
		}
		d.grabLater = false
	}
	s.remote = false
	s.requested = false
	s.confirmed = false
	s.warning = ""
	s.epoch++
}

// grabOne takes one device for REMOTE. A device another program already holds
// delivers nothing to us or to the desktop, so it is skipped and reported: one
// exclusive holder must not stop the other keyboards and mice from switching.
func (s *linuxSource) grabOne(d *linuxDevice) error {
	if e := grabDevice(d, true); e != nil {
		if errors.Is(e, unix.EBUSY) {
			d.foreign = true
			return nil
		}
		return e
	}
	d.foreign = false
	d.grabbed = true
	return nil
}
func (s *linuxSource) updateWarning() {
	var names []string
	for _, d := range s.devices {
		if d.foreign {
			names = append(names, d.label())
		}
	}
	s.warning = ""
	if len(names) > 0 {
		sort.Strings(names)
		s.warning = "input held exclusively by another program, not forwarded: " + strings.Join(names, ", ")
	}
}
func (d *linuxDevice) label() string {
	if d.name != "" {
		return d.name + " (" + filepath.Base(d.path) + ")"
	}
	return filepath.Base(d.path)
}

// Warning reports devices left out of the current REMOTE grab.
func (s *linuxSource) Warning() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.warning
}

// grabPending takes devices that appeared during REMOTE once they are released,
// instead of leaving REMOTE: hot-plug (e.g. a Bluetooth reconnect) keeps the session.
func (s *linuxSource) grabPending() {
	if !s.remote {
		return
	}
	for _, d := range s.devices {
		if !d.grabLater || !d.boundary || d.dropped || anyKey(d.keys) {
			continue
		}
		d.grabLater = false
		if e := s.grabOne(d); e != nil {
			d.foreign = true // Report it rather than retrying an unusable device forever.
		}
	}
	s.updateWarning()
}
func (s *linuxSource) remove(d *linuxDevice) input.Event {
	unix.Close(d.fd) // Closing also ends our grab.
	delete(s.devices, d.path)
	s.updateWarning()
	return input.Event{Device: d.path, Kind: input.Removed}
}
func (s *linuxSource) Local()                          { s.mu.Lock(); s.localLocked(); s.mu.Unlock(); s.route.local() }
func (s *linuxSource) ReleaseLocal(*input.State) error { return nil }

// A normal return waits for a released batch boundary. Local itself remains the
// immediate fault/exit path and never waits for physical input.
func (s *linuxSource) TryLocal() (bool, error) {
	s.mu.Lock()
	if !s.remote {
		s.requested = false // A cancelled switch must not grab later.
		s.mu.Unlock()
		return true, nil
	}
	for _, d := range s.devices {
		if d.foreign {
			continue
		}
		if !d.boundary || d.dropped {
			s.mu.Unlock()
			return false, nil
		}
		for _, b := range d.keys {
			if b != 0 {
				s.mu.Unlock()
				return false, nil
			}
		}
		keys, e := queryKeys(d)
		if e != nil {
			s.localLocked()
			s.mu.Unlock()
			s.route.local()
			return false, e
		}
		for _, b := range keys {
			if b != 0 {
				s.mu.Unlock()
				return false, nil
			}
		}
	}
	s.localLocked()
	s.mu.Unlock()
	s.route.local()
	return true, nil
}

var queryKeys = readKeys
var discardQueued = func(d *linuxDevice) error {
	var raw [24 * 64]byte
	for i := 0; i < 32; i++ {
		n, e := unix.Read(d.fd, raw[:])
		if e == unix.EAGAIN {
			return nil
		}
		if e == unix.EINTR {
			continue
		}
		if e != nil {
			return e
		}
		if n <= 0 {
			return errors.New("input device disconnected during grab")
		}
	}
	return errors.New("input queue did not reach a safe grab boundary")
}
var grabDevice = func(d *linuxDevice, grab bool) error {
	flag := uintptr(0)
	if grab {
		flag = 1
	}
	return ioctl(d.fd, 0x40044590, flag)
}

func (s *linuxSource) ConfirmBoundary(epoch uint64) {
	s.mu.Lock()
	if s.remote && s.epoch == epoch {
		s.confirmed = true
	}
	s.mu.Unlock()
}
func (s *linuxSource) Remote() (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.remote {
		return s.confirmed, nil
	}
	if len(s.devices) == 0 {
		return false, errors.New("no readable physical input devices")
	}
	s.requested = true
	return false, nil
}

// Called only by the reader after all delivered batches, never between events.
func (s *linuxSource) tryGrab() (bool, error) {
	if !s.requested || s.remote {
		return false, nil
	}
	for _, d := range s.devices {
		if d.foreign {
			continue // Its keys belong to the exclusive holder; we never see them released.
		}
		if !d.boundary || d.dropped {
			return false, nil
		}
		keys, e := queryKeys(d)
		if e != nil {
			return false, e
		}
		if anyKey(keys) {
			return false, nil
		}
	}
	for _, d := range s.devices {
		if e := s.grabOne(d); e != nil {
			s.localLocked()
			return false, e
		}
	}
	for _, d := range s.devices {
		if !d.grabbed {
			continue
		}
		if e := discardQueued(d); e != nil {
			s.localLocked()
			return false, e
		}
	}
	snapshots := map[*linuxDevice][96]byte{}
	for _, d := range s.devices {
		if !d.grabbed {
			continue
		}
		keys, e := queryKeys(d)
		if e != nil {
			s.localLocked()
			return false, e
		}
		if anyKey(keys) {
			s.localLocked()
			return false, errors.New("physical input changed during grab; returned to LOCAL")
		}
		snapshots[d] = keys
	}
	for d, keys := range snapshots {
		s.boundaryEvents = append(s.boundaryEvents, syncDevice(d, keys, true)...)
		d.delivered = d.keys
	}
	s.remote = true
	s.confirmed = false
	s.requested = false
	s.epoch++
	s.updateWarning()
	s.route.activate()
	return true, nil
}
func (s *linuxSource) scan() ([]input.Event, error) {
	paths, e := filepath.Glob("/dev/input/event*")
	if e != nil {
		return nil, e
	}
	present := map[string]bool{}
	var events []input.Event
	for _, path := range paths {
		present[path] = true
	}
	for path, d := range s.devices {
		if !present[path] {
			events = append(events, s.remove(d))
		}
	}
	for _, path := range paths {
		if s.devices[path] != nil {
			continue
		}
		fd, e := unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		if e != nil {
			continue
		}
		var name [128]byte
		if ioctlBuffer(fd, uintptr(2<<30|len(name)<<16|int('E')<<8|0x06), unsafe.Pointer(&name[0])) != nil {
			unix.Close(fd)
			continue
		}
		if strings.HasPrefix(string(name[:]), "EspKVM") {
			unix.Close(fd)
			continue
		}
		var capability [4]byte
		if ioctlBuffer(fd, uintptr(2<<30|len(capability)<<16|int('E')<<8|0x20), unsafe.Pointer(&capability[0])) != nil || capability[0]&2 == 0 {
			unix.Close(fd)
			continue
		}
		var keysSupported [96]byte
		if ioctlBuffer(fd, uintptr(2<<30|len(keysSupported)<<16|int('E')<<8|0x21), unsafe.Pointer(&keysSupported[0])) != nil || !supportedDevice(capability, keysSupported) {
			unix.Close(fd)
			continue
		}
		d := &linuxDevice{fd: fd, path: path, name: strings.TrimRight(string(name[:]), "\x00"), boundary: true}
		keys, e := readKeys(d)
		if e != nil {
			unix.Close(fd)
			continue
		}
		s.devices[path] = d
		events = append(events, syncDevice(d, keys, true)...)
		d.delivered = d.keys
		d.grabLater = s.remote
	}
	return events, nil
}
func (s *linuxSource) Run(ctx context.Context, sink func(input.Event) bool) error {
	s.route.resetPhysical()
	defer func() {
		s.Local()
		s.mu.Lock()
		for _, d := range s.devices {
			unix.Close(d.fd)
		}
		s.devices = map[string]*linuxDevice{}
		s.mu.Unlock()
	}()
	nextScan := time.Time{}
	started := false
	for ctx.Err() == nil {
		s.mu.Lock()
		var events []input.Event
		if time.Now().After(nextScan) {
			var e error
			events, e = s.scan()
			if e != nil {
				s.mu.Unlock()
				return e
			}
			nextScan = time.Now().Add(500 * time.Millisecond)
		}
		if !started && len(s.devices) == 0 {
			s.mu.Unlock()
			return errors.New("no readable evdev devices; configure espkvm group permissions on a Linux desktop")
		}
		started = true
		poll := make([]unix.PollFd, 0, len(s.devices))
		mapping := make([]*linuxDevice, 0, len(s.devices))
		for _, d := range s.devices {
			poll = append(poll, unix.PollFd{Fd: int32(d.fd), Events: unix.POLLIN})
			mapping = append(mapping, d)
		}
		s.mu.Unlock()
		for _, e := range events {
			e.Captured = time.Now()
			s.route.observe(e)
			sink(e)
		}
		if _, e := unix.Poll(poll, 20); e != nil && e != unix.EINTR {
			return e
		}
		events = nil
		s.mu.Lock()
		for i, p := range poll {
			if p.Revents == 0 {
				continue
			}
			d := mapping[i]
			if s.devices[d.path] != d {
				continue
			}
			var raw [24 * 64]byte
			n, e := unix.Read(d.fd, raw[:])
			if e != nil {
				if e != unix.EAGAIN && e != unix.EINTR {
					events = append(events, s.remove(d))
				}
				continue
			}
			if n == 0 {
				events = append(events, s.remove(d))
				continue
			}
			if n%24 != 0 {
				s.mu.Unlock()
				return fmt.Errorf("partial evdev event from %s", filepath.Base(d.path))
			}
			for j := 0; j < n; j += 24 {
				kind, code, value := binary.LittleEndian.Uint16(raw[j+16:]), binary.LittleEndian.Uint16(raw[j+18:]), int32(binary.LittleEndian.Uint32(raw[j+20:]))
				d.boundary = kind == 0 && code == 0
				if kind == 0 && code == 3 {
					// Rebuild from EVIOCGKEY at the next SYN_REPORT (design 7.1); losing
					// buffered events is no reason to leave REMOTE or drop the session.
					d.dropped = true
					d.pending = nil
					d.keys = d.delivered
					continue
				}
				if kind == 0 && code == 0 && d.dropped {
					keys, e := readKeys(d)
					if e != nil {
						s.mu.Unlock()
						return e
					}
					events = append(events, syncDevice(d, keys, true)...)
					d.dropped = false
					d.delivered = d.keys
					continue
				}
				if kind == 0 && code == 0 {
					events = append(events, d.pending...)
					d.pending = nil
					d.delivered = d.keys
					continue
				}
				if d.dropped {
					continue
				}
				if kind == 1 && code < 768 && value != 2 {
					keys := d.keys
					if value != 0 {
						keys[code/8] |= 1 << (code % 8)
					} else {
						keys[code/8] &^= 1 << (code % 8)
					}
					d.pending = append(d.pending, syncDevice(d, keys, false)...)
				}
				if kind == 2 {
					event := input.Event{Device: d.path, Kind: input.Motion}
					switch code {
					case 0:
						event.DX = value
					case 1:
						event.DY = value
					case 8:
						event.Wheel = value
					case 6:
						event.Pan = value
					default:
						continue
					}
					d.pending = append(d.pending, event)
				}
				if len(d.pending) > 128 {
					s.mu.Unlock()
					return errors.New("evdev batch exceeded 128 events; input resynchronization required")
				}
				for i := range d.pending {
					if d.pending[i].Captured.IsZero() {
						d.pending[i].Captured = time.Now()
					}
				}
			}
		}
		captured := time.Now()
		for i := range events {
			if events[i].Captured.IsZero() {
				events[i].Captured = captured
			}
		}
		s.mu.Unlock()
		// No source mutex is held when a queue overflow calls Local synchronously.
		for _, e := range events {
			s.route.observe(e)
			sink(e)
		}
		s.mu.Lock()
		s.grabPending()
		acquired, grabError := s.tryGrab()
		epoch := s.epoch
		updates := s.boundaryEvents
		s.boundaryEvents = nil
		s.mu.Unlock()
		if grabError != nil {
			s.Local()
			return grabError
		}
		if acquired {
			for _, e := range updates {
				e.Captured = time.Now()
				s.route.observe(e)
				sink(e)
			}
			sink(input.Event{Kind: input.Boundary, Epoch: epoch})
		}

	}
	return ctx.Err()
}
