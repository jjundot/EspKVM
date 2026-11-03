//go:build windows && amd64

package capture

import (
	"context"
	"errors"
	"fmt"
	"github.com/jjundot/EspKVM/internal/input"
	"github.com/jjundot/EspKVM/internal/keymap"
	"golang.org/x/sys/windows"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

var user32 = syscall.NewLazyDLL("user32.dll")
var kernel32 = syscall.NewLazyDLL("kernel32.dll")
var setHook = user32.NewProc("SetWindowsHookExW")
var nextHook = user32.NewProc("CallNextHookEx")
var unhook = user32.NewProc("UnhookWindowsHookEx")
var rawData = user32.NewProc("GetRawInputData")
var defWindow = user32.NewProc("DefWindowProcW")
var postThread = user32.NewProc("PostThreadMessageW")

const marker = 0x45534b56

type point struct{ x, y int32 }
type message struct {
	window         uintptr
	kind           uint32
	wparam, lparam uintptr
	tick           uint32
	position       point
	private        uint32
}
type windowClass struct {
	size, style                        uint32
	callback                           uintptr
	clsExtra, windowExtra              int32
	instance, icon, cursor, background uintptr
	menu, name                         *uint16
	smallIcon                          uintptr
}
type keyboardHook struct {
	vk, scan, flags, tick uint32
	extra                 uintptr
}
type mouseHook struct {
	position          point
	data, flags, tick uint32
	extra             uintptr
}
type rawHeader struct {
	kind, size     uint32
	device, wparam uintptr
}
type rawMouse struct {
	flags      uint16
	padding    uint16
	buttons    uint32
	rawButtons uint32
	dx, dy     int32
	extra      uint32
}
type rawInput struct {
	header rawHeader
	mouse  rawMouse
}

type windowsSource struct {
	route           routing
	mu              sync.Mutex
	forwarded       map[uint32]winInput
	sink            func(input.Event) bool
	wheel, pan      int32
	wheelEpoch      uint64
	active, clipped atomic.Bool
}

var current atomic.Pointer[windowsSource]
var keyboardProc = syscall.NewCallback(keyboardCallback)
var mouseProc = syscall.NewCallback(mouseCallback)
var windowProc = syscall.NewCallback(windowCallback)

func New() Source                                      { return &windowsSource{forwarded: map[uint32]winInput{}} }
func (s *windowsSource) SetHotkey(keys []uint16) error { return s.route.setHotkey(keys) }
func (s *windowsSource) SetRecording(active bool)      { s.route.setRecording(active) }

type keyInput struct {
	vk, scan             uint16
	flags, tick, padding uint32
	extra                uintptr
}
type winInput struct {
	_             [0]uintptr // Preserve the native INPUT union's 8-byte alignment.
	kind, padding uint32
	data          [32]byte
}

func keyboardInput(scan uint16, extended bool, vk uint32) winInput {
	out := winInput{kind: 1}
	key := (*keyInput)(unsafe.Pointer(&out.data[0]))
	key.scan = scan
	key.flags = 8 | 2
	key.extra = marker
	if extended {
		key.flags |= 1
	}
	if vk == 0x13 {
		key.vk = 0x13
		key.scan = 0
		key.flags = 2
	}
	return out
}
func sendInputs(events []winInput) error {
	if len(events) == 0 {
		return nil
	}
	n, _, e := user32.NewProc("SendInput").Call(uintptr(len(events)), uintptr(unsafe.Pointer(&events[0])), unsafe.Sizeof(events[0]))
	if n != uintptr(len(events)) {
		return fmt.Errorf("local release delivered %d/%d events: %v", n, len(events), e)
	}
	return nil
}
func (s *windowsSource) Local() {
	s.route.mu.Lock()
	release := s.route.remote || s.route.transition || (s.route.hotkey != nil && s.route.hotkey.Waiting())
	s.route.mu.Unlock()
	s.route.local()
	if s.clipped.Swap(false) {
		user32.NewProc("ClipCursor").Call(0)
	}
	if release {
		if s.releaseForwarded() != nil {
			s.route.abortLocal()
		}
	}
}
func neutral() bool {
	for vk := uintptr(1); vk < 256; vk++ {
		v, _, _ := user32.NewProc("GetAsyncKeyState").Call(vk)
		if v&0x8000 != 0 {
			return false
		}
	}
	return true
}
func (s *windowsSource) Remote() (bool, error) {
	if !s.active.Load() {
		return false, errors.New("input capture is not running")
	}
	if !neutral() {
		return false, nil
	}
	var p point
	if ok, _, e := user32.NewProc("GetCursorPos").Call(uintptr(unsafe.Pointer(&p))); ok == 0 {
		return false, e
	}
	rectangle := struct{ left, top, right, bottom int32 }{p.x, p.y, p.x + 1, p.y + 1}
	if ok, _, e := user32.NewProc("ClipCursor").Call(uintptr(unsafe.Pointer(&rectangle))); ok == 0 {
		return false, e
	}
	s.clipped.Store(true)
	s.route.activate()
	return true, nil
}
func (s *windowsSource) ReleaseLocal(*input.State) error {
	s.route.begin()
	return s.releaseForwarded()
}
func (s *windowsSource) releaseForwarded() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	events := make([]winInput, 0, len(s.forwarded))
	for _, event := range s.forwarded {
		events = append(events, event)
	}
	if e := sendInputs(events); e != nil {
		s.route.abortLocal()
		return e
	}
	s.forwarded = map[uint32]winInput{}
	return nil
}
func (s *windowsSource) fault() {
	s.Local()
	s.route.resetPhysical()
	if s.sink != nil {
		s.sink(input.Event{Kind: input.Fault, Reset: true})
	}
}
func keyboardCallback(code int32, wparam, lparam uintptr) uintptr {
	s := current.Load()
	if code >= 0 && s != nil {
		var data keyboardHook
		var copied uintptr
		if windows.ReadProcessMemory(windows.CurrentProcess(), lparam, (*byte)(unsafe.Pointer(&data)), unsafe.Sizeof(data), &copied) != nil {
			result, _, _ := nextHook.Call(0, uintptr(code), wparam, lparam)
			return result
		}
		raw := &data
		if raw.extra == marker {
			result, _, _ := nextHook.Call(0, uintptr(code), wparam, lparam)
			return result
		}
		usage, ok := keymap.Set1(uint16(raw.scan), raw.flags&1 != 0)
		kind := input.Key
		if button, found := keymap.WindowsBrowserMouseButton(raw.vk); found {
			usage, ok, kind = button, true, input.Button
			input.DebugMousef("WH_KEYBOARD_LL VK=0x%x scan=0x%x flags=0x%x -> XBUTTON%d down=%t", raw.vk, raw.scan, raw.flags, button-2, raw.flags&0x80 == 0)
		}
		if raw.vk == 0x13 {
			usage = 0x48
			ok = true
		}
		if consumer, found := map[uint32]uint16{0xad: 0xe2, 0xae: 0xea, 0xaf: 0xe9, 0xb0: 0xb5, 0xb1: 0xb6, 0xb2: 0xb7, 0xb3: 0xcd}[raw.vk]; found {
			usage = consumer
			ok = true
			kind = input.Consumer
		}
		if ok {
			event := input.Event{Device: "windows-hooks", Kind: kind, Code: usage, Down: raw.flags&0x80 == 0, Captured: time.Now(), Synthetic: raw.flags&0x10 != 0}
			swallow := s.route.routeEvent(&event)
			token := input.Token(kind, usage)
			s.mu.Lock()
			if !swallow {
				if event.Down {
					if kind == input.Button {
						s.forwarded[token] = mouseUp(usage)
					} else {
						s.forwarded[token] = keyboardInput(uint16(raw.scan), raw.flags&1 != 0, raw.vk)
					}
				} else {
					delete(s.forwarded, token)
				}
			}
			s.mu.Unlock()
			if !s.sink(event) {
				s.Local()
			}
			if swallow {
				return 1
			}
		} else {
			s.route.mu.Lock()
			swallow := s.route.remote || s.route.transition
			s.route.mu.Unlock()
			if swallow {
				return 1
			}
		}
	}
	result, _, _ := nextHook.Call(0, uintptr(code), wparam, lparam)
	return result
}
func mouseUp(button uint16) winInput {
	out := winInput{}
	flags := uint32(0)
	data := uint32(0)
	switch button {
	case 0:
		flags = 4
	case 1:
		flags = 16
	case 2:
		flags = 64
	case 3:
		flags = 256
		data = 1
	case 4:
		flags = 256
		data = 2
	}
	type mouseInput struct {
		dx, dy                     int32
		data, flags, tick, padding uint32
		extra                      uintptr
	}
	m := (*mouseInput)(unsafe.Pointer(&out.data[0]))
	m.data = data
	m.flags = flags
	m.extra = marker
	return out
}
func mouseCallback(code int32, wparam, lparam uintptr) uintptr {
	s := current.Load()
	if code >= 0 && s != nil {
		var data mouseHook
		var copied uintptr
		if windows.ReadProcessMemory(windows.CurrentProcess(), lparam, (*byte)(unsafe.Pointer(&data)), unsafe.Sizeof(data), &copied) != nil {
			result, _, _ := nextHook.Call(0, uintptr(code), wparam, lparam)
			return result
		}
		raw := &data
		if raw.extra == marker {
			result, _, _ := nextHook.Call(0, uintptr(code), wparam, lparam)
			return result
		}
		button, down, known := uint16(0), false, true
		switch wparam {
		case 0x201:
			button = 0
			down = true
		case 0x202:
			button = 0
		case 0x204:
			button = 1
			down = true
		case 0x205:
			button = 1
		case 0x207:
			button = 2
			down = true
		case 0x208:
			button = 2
		case 0x20b, 0x20c:
			button = 3
			if raw.data>>16 == 2 {
				button = 4
			}
			down = wparam == 0x20b
		default:
			known = false
		}
		if known {
			if button >= 3 {
				input.DebugMousef("WH_MOUSE_LL wParam=0x%x mouseData=0x%x flags=0x%x -> XBUTTON%d down=%t", wparam, raw.data, raw.flags, button-2, down)
			}
			event := input.Event{Device: "windows-hooks", Kind: input.Button, Code: button, Down: down, Captured: time.Now(), Synthetic: raw.flags&1 != 0}
			swallow := s.route.routeEvent(&event)
			token := input.Token(input.Button, button)
			s.mu.Lock()
			if !swallow {
				if down {
					s.forwarded[token] = mouseUp(button)
				} else {
					delete(s.forwarded, token)
				}
			}
			s.mu.Unlock()
			if !s.sink(event) {
				s.Local()
			}
			if swallow {
				return 1
			}
		} else {
			s.route.mu.Lock()
			swallow := s.route.remote || s.route.transition || s.route.hotkey != nil && s.route.hotkey.Waiting()
			s.route.mu.Unlock()
			if swallow {
				return 1
			}
		}
	}
	result, _, _ := nextHook.Call(0, uintptr(code), wparam, lparam)
	return result
}
func windowCallback(window uintptr, kind uint32, wparam, lparam uintptr) uintptr {
	s := current.Load()
	if s != nil && kind == 0x2b1 && (wparam == 7 || wparam == 6) {
		s.fault()
	}
	if s != nil && kind == 0xff {
		epoch := s.route.epoch.Load()
		var raw rawInput
		size := uint32(unsafe.Sizeof(raw))
		n, _, _ := rawData.Call(lparam, 0x10000003, uintptr(unsafe.Pointer(&raw)), uintptr(unsafe.Pointer(&size)), unsafe.Sizeof(rawHeader{}))
		if uint32(n) != ^uint32(0) && n >= unsafe.Sizeof(raw) && raw.header.kind == 0 {
			event := input.Event{Device: "windows-hooks", Kind: input.Motion, Captured: time.Now()}
			if raw.mouse.flags&1 == 0 {
				event.DX = raw.mouse.dx
				event.DY = raw.mouse.dy
			}
			flags := uint16(raw.mouse.buttons)
			data := int32(int16(raw.mouse.buttons >> 16))
			s.mu.Lock()
			changed := epoch != s.route.epoch.Load()
			if s.wheelEpoch != epoch {
				s.wheel = 0
				s.pan = 0
				s.wheelEpoch = epoch
			}
			if !changed && flags&0x400 != 0 {
				s.wheel += data
				event.Wheel = s.wheel / 120
				s.wheel %= 120
			}
			if !changed && flags&0x800 != 0 {
				s.pan += data
				event.Pan = s.pan / 120
				s.pan %= 120
			}
			s.mu.Unlock()
			s.route.mu.Lock()
			event.Suppressed = changed || s.route.transition || s.route.hotkey != nil && s.route.hotkey.Waiting()
			s.route.mu.Unlock()
			if event.DX != 0 || event.DY != 0 || event.Wheel != 0 || event.Pan != 0 {
				if !s.sink(event) {
					s.Local()
				}
			}
		}
	}
	result, _, _ := defWindow.Call(window, uintptr(kind), wparam, lparam)
	return result
}
func desktopName() string {
	desktop, _, _ := user32.NewProc("OpenInputDesktop").Call(0, 0, 1)
	if desktop == 0 {
		return ""
	}
	defer user32.NewProc("CloseDesktop").Call(desktop)
	var name [256]uint16
	var needed uint32
	ok, _, _ := user32.NewProc("GetUserObjectInformationW").Call(desktop, 2, uintptr(unsafe.Pointer(&name[0])), unsafe.Sizeof(name), uintptr(unsafe.Pointer(&needed)))
	if ok == 0 {
		return ""
	}
	return syscall.UTF16ToString(name[:])
}
func (s *windowsSource) Run(ctx context.Context, sink func(input.Event) bool) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	s.route.resetPhysical()
	s.sink = sink
	if !current.CompareAndSwap(nil, s) {
		return errors.New("input capture already running")
	}
	defer current.Store(nil)
	defer s.Local()
	defer s.active.Store(false)
	thread, _, _ := kernel32.NewProc("GetCurrentThreadId").Call()
	module, _, _ := kernel32.NewProc("GetModuleHandleW").Call(0)
	name, _ := syscall.UTF16PtrFromString("EspKVMInputCapture")
	defer func() { runtime.KeepAlive(name) }()
	cls := windowClass{callback: windowProc, instance: module, name: name}
	cls.size = uint32(unsafe.Sizeof(cls))
	atom, _, e := user32.NewProc("RegisterClassExW").Call(uintptr(unsafe.Pointer(&cls)))
	if atom == 0 {
		return e
	}
	defer user32.NewProc("UnregisterClassW").Call(uintptr(unsafe.Pointer(name)), module)
	window, _, e := user32.NewProc("CreateWindowExW").Call(0, uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(name)), 0, 0, 0, 0, 0, ^uintptr(2), 0, module, 0)
	if window == 0 {
		return e
	}
	defer user32.NewProc("DestroyWindow").Call(window)
	type rawDevice struct {
		page, usage uint16
		flags       uint32
		target      uintptr
	}
	device := rawDevice{page: 1, usage: 2, flags: 0x100, target: window}
	if ok, _, e := user32.NewProc("RegisterRawInputDevices").Call(uintptr(unsafe.Pointer(&device)), 1, unsafe.Sizeof(device)); ok == 0 {
		return e
	}
	defer func() {
		device.flags = 1
		device.target = 0
		user32.NewProc("RegisterRawInputDevices").Call(uintptr(unsafe.Pointer(&device)), 1, unsafe.Sizeof(device))
	}()
	keyboard, _, e := setHook.Call(13, keyboardProc, module, 0)
	if keyboard == 0 {
		return e
	}
	defer unhook.Call(keyboard)
	mouse, _, e := setHook.Call(14, mouseProc, module, 0)
	if mouse == 0 {
		return e
	}
	defer unhook.Call(mouse)
	s.active.Store(true)
	wts := syscall.NewLazyDLL("wtsapi32.dll")
	wts.NewProc("WTSRegisterSessionNotification").Call(window, 0)
	defer wts.NewProc("WTSUnRegisterSessionNotification").Call(window)
	done := make(chan struct{})
	monitorDone := make(chan struct{})
	defer func() { close(done); <-monitorDone }()
	go func() {
		defer close(monitorDone)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		previous := desktopName()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				s.Local()
				postThread.Call(thread, 0x12, 0, 0)
				return
			case <-ticker.C:
				next := desktopName()
				if next == "" || next != previous {
					s.fault()
				}
				previous = next
			}
		}
	}()
	var msg message
	for {
		result, _, e := user32.NewProc("GetMessageW").Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(result) == -1 {
			return e
		}
		if result == 0 {
			return nil
		}
		user32.NewProc("TranslateMessage").Call(uintptr(unsafe.Pointer(&msg)))
		user32.NewProc("DispatchMessageW").Call(uintptr(unsafe.Pointer(&msg)))
	}
}
