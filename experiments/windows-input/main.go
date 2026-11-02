//go:build windows && amd64

// inputprobe is an isolated M0 experiment, never imported by the client.
// It records counts only. Blocking and tagged F24 injection require explicit flags.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime"
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
var block atomic.Bool
var keyEvents, buttonEvents, motionEvents, wheelEvents, ownEvents, blockedEvents atomic.Uint64

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

func keyboardCallback(code int32, wparam, lparam uintptr) uintptr {
	if code >= 0 {
		event := (*keyboardHook)(unsafe.Pointer(lparam))
		if event.extra == marker {
			ownEvents.Add(1)
		} else {
			keyEvents.Add(1)
			if block.Load() {
				blockedEvents.Add(1)
				return 1
			}
		}
	}
	result, _, _ := nextHook.Call(0, uintptr(code), wparam, lparam)
	return result
}
func mouseCallback(code int32, wparam, lparam uintptr) uintptr {
	if code >= 0 {
		event := (*mouseHook)(unsafe.Pointer(lparam))
		if event.extra != marker {
			switch wparam {
			case 0x201, 0x202, 0x204, 0x205, 0x207, 0x208, 0x20b, 0x20c:
				buttonEvents.Add(1)
			}
			// Hook is only a gate. Relative motion/wheel counts come exclusively from Raw Input.
			if block.Load() {
				blockedEvents.Add(1)
				return 1
			}
		}
	}
	result, _, _ := nextHook.Call(0, uintptr(code), wparam, lparam)
	return result
}
func windowCallback(window uintptr, kind uint32, wparam, lparam uintptr) uintptr {
	if kind == 0xff {
		var data rawInput
		size := uint32(unsafe.Sizeof(data))
		n, _, _ := rawData.Call(lparam, 0x10000003, uintptr(unsafe.Pointer(&data)), uintptr(unsafe.Pointer(&size)), unsafe.Sizeof(rawHeader{}))
		if uint32(n) != ^uint32(0) && n >= unsafe.Sizeof(data) && data.header.kind == 0 {
			if data.mouse.flags&1 == 0 && (data.mouse.dx != 0 || data.mouse.dy != 0) {
				motionEvents.Add(1)
			}
			if uint16(data.mouse.buttons)&0xc00 != 0 {
				wheelEvents.Add(1)
			}
		}
	}
	result, _, _ := defWindow.Call(window, uintptr(kind), wparam, lparam)
	return result
}
func neutral() bool {
	proc := user32.NewProc("GetAsyncKeyState")
	for vk := uintptr(1); vk < 256; vk++ {
		state, _, _ := proc.Call(vk)
		if state&0x8000 != 0 {
			return false
		}
	}
	return true
}
func injectF24() error {
	type keyInput struct {
		vk, scan             uint16
		flags, tick, padding uint32
		extra                uintptr
	}
	type input struct {
		kind, padding uint32
		key           keyInput
		tail          [8]byte
	}
	data := [2]input{{kind: 1, key: keyInput{vk: 0x87, extra: marker}}, {kind: 1, key: keyInput{vk: 0x87, flags: 2, extra: marker}}}
	n, _, err := user32.NewProc("SendInput").Call(2, uintptr(unsafe.Pointer(&data[0])), unsafe.Sizeof(data[0]))
	if n != 2 {
		return fmt.Errorf("SendInput delivered %d/2 events: %v", n, err)
	}
	return nil
}
func run() error {
	duration := flag.Duration("duration", 30*time.Second, "probe duration (counts only)")
	blockMS := flag.Int("block-ms", 0, "explicit bounded blocking test after 3s and all keys/buttons released; maximum 2000ms")
	inject := flag.Bool("inject-f24", false, "explicitly inject tagged F24 down/up to test exclusion")
	flag.Parse()
	if *blockMS < 0 || *blockMS > 2000 || *duration <= 0 {
		return fmt.Errorf("invalid probe duration or blocking duration")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	thread, _, _ := kernel32.NewProc("GetCurrentThreadId").Call()
	module, _, _ := kernel32.NewProc("GetModuleHandleW").Call(0)
	name, _ := syscall.UTF16PtrFromString("EspKVMInputProbe")
	cls := windowClass{callback: syscall.NewCallback(windowCallback), instance: module, name: name}
	cls.size = uint32(unsafe.Sizeof(cls))
	atom, _, err := user32.NewProc("RegisterClassExW").Call(uintptr(unsafe.Pointer(&cls)))
	if atom == 0 {
		return err
	}
	defer func() { runtime.KeepAlive(name) }()
	defer user32.NewProc("UnregisterClassW").Call(uintptr(unsafe.Pointer(name)), module)
	window, _, err := user32.NewProc("CreateWindowExW").Call(0, uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(name)), 0, 0, 0, 0, 0, ^uintptr(2), 0, module, 0)
	if window == 0 {
		return err
	}
	defer user32.NewProc("DestroyWindow").Call(window)
	type rawDevice struct {
		page, usage uint16
		flags       uint32
		target      uintptr
	}
	device := rawDevice{page: 1, usage: 2, flags: 0x100, target: window}
	ok, _, err := user32.NewProc("RegisterRawInputDevices").Call(uintptr(unsafe.Pointer(&device)), 1, unsafe.Sizeof(device))
	if ok == 0 {
		return err
	}
	keyboard, _, err := setHook.Call(13, syscall.NewCallback(keyboardCallback), module, 0)
	if keyboard == 0 {
		return err
	}
	defer unhook.Call(keyboard)
	mouse, _, err := setHook.Call(14, syscall.NewCallback(mouseCallback), module, 0)
	if mouse == 0 {
		return err
	}
	defer unhook.Call(mouse)
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt)
	defer signal.Stop(quit)
	done := make(chan struct{})
	defer close(done)
	defer block.Store(false)
	go func() {
		timer := time.NewTimer(*duration)
		defer timer.Stop()
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-quit:
				postThread.Call(thread, 0x12, 0, 0)
				return
			case <-timer.C:
				postThread.Call(thread, 0x12, 0, 0)
				return
			case <-tick.C:
				fmt.Printf("blocking=%v keyboard=%d buttons=%d raw-motion=%d raw-wheel=%d own-injected=%d swallowed=%d\n", block.Load(), keyEvents.Load(), buttonEvents.Load(), motionEvents.Load(), wheelEvents.Load(), ownEvents.Load(), blockedEvents.Load())
			}
		}
	}()
	if *blockMS > 0 || *inject {
		go func() {
			select {
			case <-time.After(3 * time.Second):
			case <-done:
				return
			}
			until := time.Now().Add(3 * time.Second)
			for !neutral() {
				if time.Now().After(until) {
					fmt.Println("probe not armed: keys/buttons held")
					return
				}
				select {
				case <-time.After(10 * time.Millisecond):
				case <-done:
					return
				}
			}
			if *inject {
				if err := injectF24(); err != nil {
					fmt.Fprintln(os.Stderr, err)
				}
			}
			if *blockMS > 0 {
				block.Store(true)
				defer block.Store(false)
				select {
				case <-time.After(time.Duration(*blockMS) * time.Millisecond):
				case <-done:
				}
			}
		}()
	}
	var msg message
	for {
		result, _, err := user32.NewProc("GetMessageW").Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(result) == -1 {
			return err
		}
		if result == 0 {
			return nil
		}
		user32.NewProc("TranslateMessage").Call(uintptr(unsafe.Pointer(&msg)))
		user32.NewProc("DispatchMessageW").Call(uintptr(unsafe.Pointer(&msg)))
	}
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
