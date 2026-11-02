package config

import (
	"golang.org/x/sys/unix"
	"os"
)

func lockHandle(f *os.File) error { return unix.Flock(int(f.Fd()), unix.LOCK_EX) }
