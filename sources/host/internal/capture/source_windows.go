//go:build windows && amd64

package capture

import (
	"context"
	"errors"
	"fmt"
	"github.com/jjundot/EspKVM/internal/debuglog"
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
type rawKeyboard struct {
	makeCode, flags, reserved, vkey uint16
	message, extra                  uint32
}
type rawKeyboardInput struct {
	header   rawHeader
	keyboard rawKeyboard
}

type windowsSource struct {
	route           routing
	mu              sync.Mutex
	forwarded       map[uint32]winInput
	sink            func(input.Event) bool
	wheel, pan      int32
	wheelEpoch      uint64
	active, clipped atomic.Bool
	globalHotkey    atomic.Bool
	hotkeyWindow    atomic.Uintptr
	keyboardHook    atomic.Uintptr
	rawCtrlLeft     bool
	rawCtrlRight    bool
	rawAltLeft      bool
	rawAltRight     bool
	rawWDown        bool
	rawComboActive  bool
	rawKeyMu        sync.Mutex
	chordButtons    uint8
}

var current atomic.Pointer[windowsSource]
var keyboardProc = syscall.NewCallback(keyboardCallback)
var mouseProc = syscall.NewCallback(mouseCallback)
var windowProc = syscall.NewCallback(windowCallback)

func New() Source {
	s := &windowsSource{forwarded: map[uint32]winInput{}}
	s.globalHotkey.Store(true)
	return s
}
func isDefaultHotkey(keys []uint16) bool {
	if len(keys) != 3 {
		return false
	}
	seen := map[uint16]bool{}
	for _, key := range keys {
		seen[key] = true
	}
	return len(seen) == 3 && seen[0xe0] && seen[0xe2] && seen[0x1a]
}
func (s *windowsSource) SetHotkey(keys []uint16) error {
	if err := s.route.setHotkey(keys); err != nil {
		return err
	}
	s.globalHotkey.Store(isDefaultHotkey(keys))
	if window := s.hotkeyWindow.Load(); window != 0 {
		user32.NewProc("PostMessageW").Call(window, 0x8001, 0, 0)
	}
	return nil
}
func (s *windowsSource) SetRecording(active bool) { s.route.setRecording(active) }

const (
	espkvmHotkeyID     = 0x4553
	espkvmHotkeyUpdate = 0x8001
	espkvmHookRefresh  = 0x8002
	wmHotkey           = 0x0312
	modAlt             = 0x0001
	modControl         = 0x0002
	modNoRepeat        = 0x4000
	virtualKeyW        = 0x57
)

func (s *windowsSource) updateGlobalHotkey(window uintptr) {
	unregister := user32.NewProc("UnregisterHotKey")
	register := user32.NewProc("RegisterHotKey")
	unregister.Call(window, espkvmHotkeyID)
	if !s.globalHotkey.Load() {
		return
	}
	if ok, _, err := register.Call(window, espkvmHotkeyID, modControl|modAlt|modNoRepeat, virtualKeyW); ok == 0 {
		debuglog.Printf("capture", "RegisterHotKey Ctrl+Alt+W failed: %v", err)
	} else {
		debuglog.Printf("capture", "registered system Ctrl+Alt+W hotkey")
	}
}

func (s *windowsSource) refreshKeyboardHook() {
	if old := s.keyboardHook.Swap(0); old != 0 {
		unhook.Call(old)
	}
	module, _, _ := kernel32.NewProc("GetModuleHandleW").Call(0)
	hook, _, err := setHook.Call(13, keyboardProc, module, 0)
	if hook == 0 {
		debuglog.Printf("capture", "WH_KEYBOARD_LL hook refresh failed: %v", err)
		return
	}
	s.keyboardHook.Store(hook)
	debuglog.Printf("capture", "WH_KEYBOARD_LL hook moved to front of chain")
}

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
	debuglog.Printf("capture", "local route requested; previous remote/transition state=%t", release)
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
	debuglog.Printf("capture", "remote keyboard and mouse capture activated")
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
	s.rawKeyMu.Lock()
	s.rawCtrlLeft, s.rawCtrlRight = false, false
	s.rawAltLeft, s.rawAltRight = false, false
	s.rawWDown, s.rawComboActive = false, false
	s.rawKeyMu.Unlock()
	if s.sink != nil {
		s.sink(input.Event{Kind: input.Fault, Reset: true})
	}
}
func (s *windowsSource) observeRawKeyboard(key rawKeyboard) {
	down := key.flags&1 == 0 // RI_KEY_BREAK
	extended := key.flags&2 != 0
	s.route.mu.Lock()
	remote := s.route.remote || s.route.transition
	s.route.mu.Unlock()
	debuglog.PrintfAsync("capture", "raw keyboard VK=0x%x make=0x%x flags=0x%x down=%t remote=%t", key.vkey, key.makeCode, key.flags, down, remote)
	tracked := false
	switch key.vkey {
	case 0x11, 0xa2, 0xa3, 0x12, 0xa4, 0xa5, virtualKeyW:
		tracked = true
		debuglog.PrintfAsync("capture", "raw hotkey key VK=0x%x flags=0x%x down=%t", key.vkey, key.flags, down)
	}
	s.rawKeyMu.Lock()
	switch key.vkey {
	case 0x11: // VK_CONTROL (the E0 flag distinguishes right Ctrl)
		if extended {
			s.rawCtrlRight = down
		} else {
			s.rawCtrlLeft = down
		}
	case 0xa2:
		s.rawCtrlLeft = down
	case 0xa3:
		s.rawCtrlRight = down
	case 0x12: // VK_MENU
		if extended {
			s.rawAltRight = down
		} else {
			s.rawAltLeft = down
		}
	case 0xa4:
		s.rawAltLeft = down
	case 0xa5:
		s.rawAltRight = down
	case virtualKeyW:
		s.rawWDown = down
	}
	combo := (s.rawCtrlLeft || s.rawCtrlRight) && (s.rawAltLeft || s.rawAltRight) && s.rawWDown
	trigger := combo && !s.rawComboActive
	s.rawComboActive = combo
	s.rawKeyMu.Unlock()
	if tracked {
		debuglog.PrintfAsync("capture", "raw hotkey state ctrl=%t alt=%t w=%t combo=%t", s.rawCtrlLeft || s.rawCtrlRight, s.rawAltLeft || s.rawAltRight, s.rawWDown, combo)
	}
	if trigger && s.globalHotkey.Load() && !s.route.recordingActive() && !s.route.recentHotkeyTrigger() && s.sink != nil {
		s.route.markHotkeyTrigger()
		debuglog.Printf("capture", "raw input recognized Ctrl+Alt+W")
		if !s.sink(input.Event{Kind: input.Shortcut, Device: "windows-raw-hotkey", Captured: time.Now()}) {
			s.Local()
		}
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
			debuglog.PrintfAsync("capture", "keyboard hook VK=0x%x scan=0x%x flags=0x%x HID=0x%x down=%t swallowed=%t", raw.vk, raw.scan, raw.flags, usage, event.Down, swallow)
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
			debuglog.PrintfAsync("capture", "unmapped keyboard hook VK=0x%x scan=0x%x flags=0x%x swallowed=%t", raw.vk, raw.scan, raw.flags, swallow)
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
			chord := false
			if button == 3 || button == 4 {
				controlHeld := s.route.controlHeld()
				mask := uint8(1 << (button - 3))
				s.mu.Lock()
				if down && controlHeld {
					s.chordButtons |= mask
					chord = true
				} else if !down && s.chordButtons&mask != 0 {
					s.chordButtons &^= mask
					chord = true
				}
				s.mu.Unlock()
			}
			event := input.Event{Device: "windows-hooks", Kind: input.Button, Code: button, Down: down, Captured: time.Now(), Synthetic: raw.flags&1 != 0}
			if chord {
				event.Suppressed = true
			}
			swallow := s.route.routeEvent(&event)
			token := input.Token(input.Button, button)
			s.mu.Lock()
			if chord {
				delete(s.forwarded, token)
			} else if !swallow {
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
			if chord && down && s.sink != nil {
				direction := uint16(0) // XBUTTON2 (forward) cycles forward; XBUTTON1 (back) cycles backward.
				if button == 3 {
					direction = 1
				}
				if !s.sink(input.Event{Kind: input.Shortcut, Code: direction, Device: "windows-mouse-hotkey", Captured: time.Now()}) {
					s.Local()
				}
			}
			if swallow || chord {
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
	if s != nil && kind == espkvmHookRefresh {
		s.refreshKeyboardHook()
		return 0
	}
	if s != nil && kind == espkvmHotkeyUpdate {
		s.updateGlobalHotkey(window)
		return 0
	}
	if s != nil && kind == wmHotkey && wparam == espkvmHotkeyID {
		debuglog.Printf("capture", "received WM_HOTKEY")
		if s.globalHotkey.Load() && !s.route.recentHotkeyTrigger() && !s.route.recordingActive() && s.sink != nil {
			s.route.markHotkeyTrigger()
			debuglog.Printf("capture", "dispatching WM_HOTKEY shortcut")
			if !s.sink(input.Event{Kind: input.Shortcut, Device: "windows-global-hotkey", Captured: time.Now()}) {
				s.Local()
			}
		}
		return 0
	}
	if s != nil && kind == 0x2b1 && (wparam == 7 || wparam == 6) {
		s.fault()
	}
	if s != nil && kind == 0xff {
		epoch := s.route.epoch.Load()
		var raw rawInput
		size := uint32(unsafe.Sizeof(raw))
		n, _, _ := rawData.Call(lparam, 0x10000003, uintptr(unsafe.Pointer(&raw)), uintptr(unsafe.Pointer(&size)), unsafe.Sizeof(rawHeader{}))
		if uint32(n) != ^uint32(0) && n >= unsafe.Sizeof(rawHeader{}) {
			if raw.header.kind == 1 && n >= unsafe.Sizeof(rawKeyboardInput{}) {
				s.observeRawKeyboard((*rawKeyboardInput)(unsafe.Pointer(&raw)).keyboard)
			}
			if raw.header.kind == 0 && n >= unsafe.Sizeof(raw) {
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
	s.hotkeyWindow.Store(window)
	s.updateGlobalHotkey(window)
	defer func() {
		user32.NewProc("UnregisterHotKey").Call(window, espkvmHotkeyID)
		s.hotkeyWindow.Store(0)
	}()
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
	keyboardDevice := rawDevice{page: 1, usage: 6, flags: 0x100, target: window}
	if ok, _, err := user32.NewProc("RegisterRawInputDevices").Call(uintptr(unsafe.Pointer(&keyboardDevice)), 1, unsafe.Sizeof(keyboardDevice)); ok == 0 {
		debuglog.Printf("capture", "RegisterRawInputDevices keyboard failed: %v", err)
	} else {
		debuglog.Printf("capture", "registered background raw keyboard input")
	}
	defer func() {
		keyboardDevice.flags = 1
		keyboardDevice.target = 0
		user32.NewProc("RegisterRawInputDevices").Call(uintptr(unsafe.Pointer(&keyboardDevice)), 1, unsafe.Sizeof(keyboardDevice))
	}()
	keyboard, _, e := setHook.Call(13, keyboardProc, module, 0)
	if keyboard == 0 {
		return e
	}
	s.keyboardHook.Store(keyboard)
	debuglog.Printf("capture", "WH_KEYBOARD_LL hook installed thread=%d desktop=%q", thread, desktopName())
	defer func() {
		if hook := s.keyboardHook.Swap(0); hook != 0 {
			unhook.Call(hook)
		}
	}()
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
		var foreground, foregroundCandidate uintptr
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				s.Local()
				postThread.Call(thread, 0x12, 0, 0)
				return
			case <-ticker.C:
				activeWindow, _, _ := user32.NewProc("GetForegroundWindow").Call()
				if activeWindow != 0 {
					rootWindow, _, _ := user32.NewProc("GetAncestor").Call(activeWindow, 2) // GA_ROOT
					if rootWindow != 0 && rootWindow != foreground {
						if foregroundCandidate == rootWindow {
							foreground = rootWindow
							foregroundCandidate = 0
							debuglog.Printf("capture", "foreground window changed hwnd=0x%x; refreshing keyboard hook chain", foreground)
							user32.NewProc("PostMessageW").Call(window, espkvmHookRefresh, 0, 0)
						} else {
							foregroundCandidate = rootWindow
						}
					} else if rootWindow == foreground {
						foregroundCandidate = 0
					}
				}
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
