package config

import (
	"crypto/md5"
	"encoding/hex"
	"path/filepath"
	"testing"
)

func TestWebPasswordStoredAsPrefixedMD5(t *testing.T) {
	s := Open(filepath.Join(t.TempDir(), "config.json"))
	d, e := s.Load()
	if e != nil {
		t.Fatal(e)
	}
	sum := md5.Sum([]byte("espkvm_admin"))
	if d.WebPasswordMD5 != hex.EncodeToString(sum[:]) || !s.WebPasswordIsDefault() || !s.VerifyWebPassword("admin") {
		t.Fatal("default password record", d.WebPasswordMD5)
	}
	before := s.WebPasswordStamp()
	if e = s.ChangeWebPassword("admin", "new-secret"); e != nil {
		t.Fatal(e)
	}
	sum = md5.Sum([]byte("espkvm_new-secret"))
	if d, _ = s.Load(); d.WebPasswordMD5 != hex.EncodeToString(sum[:]) {
		t.Fatal("changed password record", d.WebPasswordMD5)
	}
	if s.VerifyWebPassword("admin") || !s.VerifyWebPassword("new-secret") || s.WebPasswordIsDefault() || s.WebPasswordStamp() == before {
		t.Fatal("only the new password may grant access")
	}
	if s.ChangeWebPassword("admin", "another-secret") == nil {
		t.Fatal("old password accepted for change")
	}
}

func TestLegacyWebPasswordMigratesOnLogin(t *testing.T) {
	s := Open(filepath.Join(t.TempDir(), "config.json"))
	// Former PBKDF2 record of "legacy-pass" with an all-zero salt.
	e := s.Update(func(d *Data) error {
		d.WebPasswordMD5 = ""
		d.WebPasswordSalt = "00000000000000000000000000000000"
		d.WebPasswordHash = hex.EncodeToString(legacyDigest("legacy-pass", make([]byte, 16)))
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	if !s.VerifyWebPassword("legacy-pass") {
		t.Fatal("legacy password rejected")
	}
	sum := md5.Sum([]byte("espkvm_legacy-pass"))
	if d, _ := s.Load(); d.WebPasswordMD5 != hex.EncodeToString(sum[:]) || d.WebPasswordSalt != "" || d.WebPasswordHash != "" {
		t.Fatal("legacy record not migrated", d)
	}
}
