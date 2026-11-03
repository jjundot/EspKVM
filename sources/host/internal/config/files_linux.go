package config

import "os"

func secureDir(p string) error {
	if e := os.MkdirAll(p, 0700); e != nil {
		return e
	}
	return os.Chmod(p, 0700)
}
func ensureConfigDir(p string) error { return os.MkdirAll(p, 0700) }
func secureFile(p string) error { return os.Chmod(p, 0600) }
func replace(a, b string) error { return os.Rename(a, b) }
func syncDir(p string) error {
	f, e := os.Open(p)
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}
