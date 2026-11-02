package protocol

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"github.com/jjundot/EspKVM/internal/protocol/pb"
	"google.golang.org/protobuf/proto"
	"os"
	"strings"
	"testing"
)

func TestV2Vectors(t *testing.T) {
	raw, e := os.ReadFile("../../../protocol/testdata/v2.txt")
	if e != nil {
		t.Fatal(e)
	}
	v := map[string][]byte{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		p := strings.Fields(line)
		b, e := hex.DecodeString(p[1])
		if e != nil {
			t.Fatal(e)
		}
		v[p[0]] = b
	}
	equal := func(name string, b []byte) {
		t.Helper()
		if !bytes.Equal(b, v[name]) {
			t.Fatalf("%s mismatch", name)
		}
	}
	sum := sha256.Sum256(v["challenge_frame"])
	equal("transcript", sum[:])
	bound, e := AuthContext(sum[:], v["owner"], true)
	if e != nil {
		t.Fatal(e)
	}
	equal("context", bound)
	for name, label := range map[string]string{"proof_c": "EK-AUTH-C", "proof_d": "EK-AUTH-D"} {
		p, e := Proof(v["psk"], bound, v["nonce"], label)
		if e != nil {
			t.Fatal(e)
		}
		equal(name, p)
	}
	key, e := SessionKey(v["psk"], bound, v["nonce"])
	if e != nil {
		t.Fatal(e)
	}
	equal("key", key)
	for name, b := range v {
		if !strings.HasPrefix(name, "pb_") {
			continue
		}
		m := new(pb.Msg)
		if e := proto.Unmarshal(b, m); e != nil {
			t.Fatal(e)
		}
		encoded, e := proto.MarshalOptions{Deterministic: true}.Marshal(m)
		if e != nil {
			t.Fatal(e)
		}
		equal(name, encoded)
		if name == "pb_password_empty" && (m.GetCfgSet().Password == nil || m.GetCfgSet().Psk != nil) {
			t.Fatal("credential presence lost")
		}
	}
	for i, p := range [][2]string{{"release_frame", "pb_release"}, {"mouse_frame", "pb_mouse"}} {
		c, _ := NewCipher(key, byte(i))
		frame, e := c.Seal(v[p[1]])
		if e != nil {
			t.Fatal(e)
		}
		equal(p[0], frame)
	}
	for _, change := range []func([]byte, []byte) ([]byte, []byte, bool){
		func(t, o []byte) ([]byte, []byte, bool) { t[0] ^= 1; return t, o, true },
		func(t, o []byte) ([]byte, []byte, bool) { o[0] ^= 1; return t, o, true },
		func(t, o []byte) ([]byte, []byte, bool) { return t, o, false },
	} {
		tr, owner, intent := change(append([]byte(nil), sum[:]...), append([]byte(nil), v["owner"]...))
		b, _ := AuthContext(tr, owner, intent)
		proof, _ := Proof(v["psk"], b, v["nonce"], "EK-AUTH-C")
		if bytes.Equal(proof, v["proof_c"]) {
			t.Fatal("unbound authentication metadata")
		}
	}
}
func TestOptionalCredentials(t *testing.T) {
	c := validConfig()
	c.Password = nil
	c.Psk = nil
	if _, e := ValidateConfig(c, true); e != nil {
		t.Fatal(e)
	}
	if _, e := ValidateConfig(c, false); e == nil {
		t.Fatal("first provisioning accepted absent credentials")
	}
	c.Psk = []byte{}
	if _, e := ValidateConfig(c, true); e == nil {
		t.Fatal("explicit empty PSK accepted")
	}
}
