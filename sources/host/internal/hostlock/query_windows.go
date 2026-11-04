package hostlock

import (
	"context"
	"syscall"
	"time"
	"unsafe"
)

const pollInterval = time.Second

var (
	wtsapi32 = syscall.NewLazyDLL("wtsapi32.dll")
	wtsQuery = wtsapi32.NewProc("WTSQuerySessionInformationW")
	wtsFree  = wtsapi32.NewProc("WTSFreeMemory")
)

// wtsInfoEx is the leading part of WTSINFOEXW. The level union holds a
// LARGE_INTEGER, which aligns it to 8 bytes.
type wtsInfoEx struct {
	level        uint32
	_            uint32
	sessionID    uint32
	sessionState uint32
	sessionFlags int32
}

// Do not derive this from the input desktop: UAC also switches to Winlogon.
func query(context.Context) uint32 {
	const currentServer, currentSession, sessionInfoEx = uintptr(0), uintptr(0xFFFFFFFF), uintptr(25)
	var info *wtsInfoEx
	var size uint32
	ok, _, _ := wtsQuery.Call(currentServer, currentSession, sessionInfoEx, uintptr(unsafe.Pointer(&info)), uintptr(unsafe.Pointer(&size)))
	if ok == 0 || info == nil {
		return Unknown
	}
	defer wtsFree.Call(uintptr(unsafe.Pointer(info)))
	if uintptr(size) < unsafe.Sizeof(*info) {
		return Unknown
	}
	return classifyWindows(info.level, info.sessionID, info.sessionState, info.sessionFlags)
}
