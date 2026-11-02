package config

import (
	"os"
	"path/filepath"
)

// WritePrivate creates a new, user-private launch credential file without overwriting.
func WritePrivate(path string, data []byte) error {
	if e := secureDir(filepath.Dir(path)); e != nil {
		return e
	}
	f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	complete := false
	defer func() {
		f.Close()
		if !complete {
			os.Remove(path)
		}
	}()
	if e = secureFile(path); e != nil {
		return e
	}
	if _, e = f.Write(data); e != nil {
		return e
	}
	if e = f.Sync(); e != nil {
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	complete = true
	return nil
}
