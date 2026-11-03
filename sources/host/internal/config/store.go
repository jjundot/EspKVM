// Package config persists credentials and unresolved device transactions.
package config

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/jjundot/EspKVM/internal/input"
	"github.com/jjundot/EspKVM/internal/protocol/pb"
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
	Devices         []Device  `json:"devices"`
	Hotkey          []uint16  `json:"hotkey"`
	Pending         []Pending `json:"pending"`
	WebPasswordSalt string    `json:"web_password_salt,omitempty"`
	WebPasswordHash string    `json:"web_password_hash,omitempty"`
}
type Store struct {
	mu      sync.Mutex
	path    string
	secured bool
}

func Open(path string) *Store { return &Store{path: path} }
func DefaultPath() (string, error) {
	exe, e := os.Executable()
	if e != nil {
		return "", e
	}
	path := filepath.Join(filepath.Dir(exe), "config.json")
	legacyDir, e := os.UserConfigDir()
	if e != nil {
		return "", e
	}
	legacy := filepath.Join(legacyDir, "EspKVM", "config.json")
	if filepath.Clean(legacy) != filepath.Clean(path) {
		if e = copyConfigIfMissing(legacy, path); e != nil {
			return "", e
		}
	}
	return path, nil
}

// copyConfigIfMissing preserves existing user settings when moving the default
// location next to the executable. The old file is retained as a backup.
func copyConfigIfMissing(src, dst string) error {
	if _, e := os.Stat(dst); e == nil {
		return nil
	} else if !os.IsNotExist(e) {
		return e
	}
	in, e := os.Open(src)
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	defer in.Close()
	if e = ensureConfigDir(filepath.Dir(dst)); e != nil {
		return e
	}
	out, e := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if os.IsExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	complete := false
	defer func() {
		out.Close()
		if !complete {
			os.Remove(dst)
		}
	}()
	if e = secureFile(dst); e != nil {
		return e
	}
	if _, e = io.Copy(out, in); e != nil {
		return e
	}
	if e = out.Sync(); e != nil {
		return e
	}
	if e = out.Close(); e != nil {
		return e
	}
	complete = true
	return nil
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
	d, e := s.load()
	if e != nil {
		return Data{}, e
	}
	changed, e := applyDefaults(&d)
	if e != nil {
		return Data{}, e
	}
	if changed {
		if e = s.save(d); e != nil {
			return Data{}, e
		}
	}
	return d, nil
}

func applyDefaults(d *Data) (bool, error) {
	changed := false
	if len(d.Hotkey) == 0 || slices.Equal(d.Hotkey, []uint16{0xe0, 0xe4}) {
		d.Hotkey = input.DefaultHotkey()
		changed = true
	}
	if d.WebPasswordSalt == "" && d.WebPasswordHash == "" {
		salt := make([]byte, 16)
		if _, e := rand.Read(salt); e != nil {
			return false, e
		}
		d.WebPasswordSalt = hex.EncodeToString(salt)
		d.WebPasswordHash = hex.EncodeToString(passwordDigest("admin", salt))
		changed = true
	} else if d.WebPasswordSalt == "" || d.WebPasswordHash == "" {
		return false, errors.New("invalid web password data in config")
	}
	return changed, nil
}

const passwordIterations = 120000

func passwordDigest(password string, salt []byte) []byte {
	mac := hmac.New(sha256.New, []byte(password))
	mac.Write(salt)
	var block [4]byte
	binary.BigEndian.PutUint32(block[:], 1)
	mac.Write(block[:])
	u := mac.Sum(nil)
	result := append([]byte(nil), u...)
	for i := 1; i < passwordIterations; i++ {
		mac.Reset()
		mac.Write(u)
		u = mac.Sum(u[:0])
		for j := range result {
			result[j] ^= u[j]
		}
	}
	return result
}

func passwordMatches(d Data, password string) bool {
	salt, e := hex.DecodeString(d.WebPasswordSalt)
	if e != nil || len(salt) != 16 {
		return false
	}
	expected, e := hex.DecodeString(d.WebPasswordHash)
	if e != nil || len(expected) != sha256.Size {
		return false
	}
	actual := passwordDigest(password, salt)
	return subtle.ConstantTimeCompare(actual, expected) == 1
}

func (s *Store) VerifyWebPassword(password string) bool {
	d, e := s.Load()
	return e == nil && len(password) <= 128 && passwordMatches(d, password)
}

func (s *Store) ChangeWebPassword(current, next string) error {
	if len(next) < 8 || len(next) > 128 {
		return errors.New("new password must be 8 to 128 bytes")
	}
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
	if _, e = applyDefaults(&d); e != nil {
		return e
	}
	if !passwordMatches(d, current) {
		return errors.New("current password is incorrect")
	}
	salt := make([]byte, 16)
	if _, e = rand.Read(salt); e != nil {
		return e
	}
	d.WebPasswordSalt = hex.EncodeToString(salt)
	d.WebPasswordHash = hex.EncodeToString(passwordDigest(next, salt))
	return s.save(d)
}
func (s *Store) load() (Data, error) {
	b, e := os.ReadFile(s.path)
	if os.IsNotExist(e) {
		return Data{Hotkey: input.DefaultHotkey()}, nil
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
	if _, e = applyDefaults(&d); e != nil {
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
	if e = ensureConfigDir(dir); e != nil {
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
		if e := ensureConfigDir(filepath.Dir(s.path)); e != nil {
			return nil, e
		}
		s.secured = true
	}
	f, e := os.OpenFile(s.path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	if e = secureFile(s.path + ".lock"); e != nil {
		f.Close()
		return nil, e
	}
	if e = lockHandle(f); e != nil {
		f.Close()
		return nil, e
	}
	return func() { f.Close() }, nil
}
