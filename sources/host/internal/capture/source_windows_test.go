//go:build windows && amd64

package capture

import (
	"testing"
	"unsafe"
)

func TestWindowsNativeLayouts(t *testing.T) {
	if unsafe.Sizeof(winInput{}) != 40 || unsafe.Alignof(winInput{}) != 8 || unsafe.Offsetof(winInput{}.data) != 8 || unsafe.Sizeof(keyInput{}) != 24 || unsafe.Sizeof(rawInput{}) != 48 || unsafe.Sizeof(mouseHook{}) != 32 {
		t.Fatal("Windows input ABI layout mismatch")
	}
}
func TestInactiveWindowsCaptureCannotClipCursor(t *testing.T) {
	s := New()
	if ready, e := s.Remote(); ready || e == nil {
		t.Fatal("inactive capture allowed remote mode")
	}
}
