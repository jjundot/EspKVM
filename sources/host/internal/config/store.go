// Package config persists credentials and unresolved device transactions.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/jjundot/EspKVM/internal/protocol/pb"
	"os"
	"path/filepath"
	"sync"
)

type Device struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	IP      string `json:"ip"`
	Port    uint16 `json:"port"`
	PSK     string `json:"psk"`
	Enabled bool   `json:"enabled"`
}
type Pending struct {
	Token    string     `json:"transaction"`
	Old      *Device    `json:"old,omitempty"`
	New      Device     `json:"new"`
	State    string     `json:"state"`
	Expected *pb.Config `json:"expected"`
}
type Data struct {
	Devices []Device  `json:"devices"`
	Hotkey  []uint16  `json:"hotkey"`
	Pending []Pending `json:"pending"`
}
type Store struct {
	mu      sync.Mutex
	path    string
	secured bool
}

func Open(path string) *Store { return &Store{path: path} }
func DefaultPath() (string, error) {
	dir, e := os.UserConfigDir()
	if e != nil {
		return "", e
	}
	return filepath.Join(dir, "EspKVM", "config.json"), nil
}
func NewPSK() (string, error) {
	b := make([]byte, 16)
	_, e := rand.Read(b)
	return hex.EncodeToString(b), e
}
func (s *Store) Load() (Data, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, e := s.lockFile()
	if e != nil {
		return Data{}, e
	}
	defer unlock()
	return s.load()
}
func (s *Store) load() (Data, error) {
	b, e := os.ReadFile(s.path)
	if os.IsNotExist(e) {
		return Data{Hotkey: []uint16{0xe0, 0xe4}}, nil
	}
	if e != nil {
		return Data{}, e
	}
	var d Data
	e = json.Unmarshal(b, &d)
	return d, e
}
func (s *Store) Update(fn func(*Data) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, e := s.lockFile()
	if e != nil {
		return e
	}
	defer unlock()
	d, e := s.load()
	if e != nil {
		return e
	}
	if e = fn(&d); e != nil {
		return e
	}
	return s.save(d)
}
func (s *Store) save(d Data) error {
	seen := map[string]bool{}
	for _, v := range d.Devices {
		if v.ID == "" || seen[v.ID] {
			return errors.New("duplicate or empty device id")
		}
		seen[v.ID] = true
		k, e := hex.DecodeString(v.PSK)
		if e != nil || len(k) != 16 || v.Port == 0 {
			return errors.New("invalid device credentials")
		}
	}
	b, e := json.MarshalIndent(d, "", "  ")
	if e != nil {
		return e
	}
	dir := filepath.Dir(s.path)
	if e = secureDir(dir); e != nil {
		return e
	}
	f, e := os.CreateTemp(dir, ".config-*")
	if e != nil {
		return e
	}
	name := f.Name()
	defer os.Remove(name)
	if e = secureFile(name); e != nil {
		f.Close()
		return e
	}
	if _, e = f.Write(b); e != nil {
		f.Close()
		return e
	}
	if e = f.Sync(); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	if e = replace(name, s.path); e != nil {
		return e
	}
	return syncDir(dir)
}

// Lock a stable sidecar because saving atomically replaces the config inode.
func (s *Store) lockFile() (func(), error) {
	if !s.secured {
		if e := secureDir(filepath.Dir(s.path)); e != nil {
			return nil, e
		}
		s.secured = true
	}
	f, e := os.OpenFile(s.path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	if e = lockHandle(f); e != nil {
		f.Close()
		return nil, e
	}
	return func() { f.Close() }, nil
}
