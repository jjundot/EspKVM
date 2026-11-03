package config

import (
	"os"
	"os/exec"
	"os/user"
	"syscall"
	"unsafe"
)

func acl(p string, dir bool) error {
	u, e := user.Current()
	if e != nil {
		return e
	}
	grant := "*" + u.Uid + ":F"
	system := "*S-1-5-18:F"
	if dir {
		grant = "*" + u.Uid + ":(OI)(CI)F"
		system = "*S-1-5-18:(OI)(CI)F"
	}
	return exec.Command("icacls.exe", p, "/inheritance:r", "/grant:r", grant, system).Run()
}
func secureDir(p string) error {
	if e := os.MkdirAll(p, 0700); e != nil {
		return e
	}
	return acl(p, true)
}
func ensureConfigDir(p string) error { return os.MkdirAll(p, 0700) }
func secureFile(p string) error { return acl(p, false) }
func replace(a, b string) error {
	src, e := syscall.UTF16PtrFromString(a)
	if e != nil {
		return e
	}
	dst, e := syscall.UTF16PtrFromString(b)
	if e != nil {
		return e
	}
	r, _, e := syscall.NewLazyDLL("kernel32.dll").NewProc("MoveFileExW").Call(uintptr(unsafe.Pointer(src)), uintptr(unsafe.Pointer(dst)), 0x1|0x8)
	if r == 0 {
		return e
	}
	return nil
}
func syncDir(string) error { return nil }
