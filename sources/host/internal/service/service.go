// Package service registers the host executable for automatic start: a systemd
// system unit on Linux, a logon scheduled task on Windows. The executable is
// registered in place, so config.json keeps living beside it and the release
// stays a single file.
package service

import (
	"os"
	"path/filepath"
)

// RunArgs are the flags the registered service starts the host with; there is
// no desktop to open a browser in, and the UI is reached by password login.
var RunArgs = []string{"-browser=false"}

var commands = map[string]func() error{
	"install":   install,
	"uninstall": uninstall,
	"start":     start,
	"stop":      stop,
	"restart":   restart,
	"status":    status,
}

func IsCommand(name string) bool { _, ok := commands[name]; return ok }
func Run(name string) error      { return commands[name]() }

func executable() (string, error) {
	p, e := os.Executable()
	if e != nil {
		return "", e
	}
	return filepath.EvalSymlinks(p)
}

func sameFile(a, b string) bool {
	x, e := os.Stat(a)
	if e != nil {
		return false
	}
	y, e := os.Stat(b)
	return e == nil && os.SameFile(x, y)
}
