package protocol

import (
	"bytes"
	"encoding/hex"
	"hash/crc32"
	"os"
	"strings"
	"testing"
	"time"
)

func TestSharedVectors(t *testing.T) {
	b, e := os.ReadFile("../../../protocol/testdata/frames.txt")
	if e != nil {
		t.Fatal(e)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		parts := strings.Fields(line)
		raw, e := hex.DecodeString(parts[1])
		if e != nil {
			t.Fatal(e)
		}
		p := new(SerialParser)
		frames := p.Feed(raw, time.Now())
		if len(frames) != 1 {
			t.Fatalf("%s: %v", parts[0], frames)
		}
		encoded, e := EncodeSerial(frames[0].Body, frames[0].Seq)
		if e != nil || !bytes.Equal(encoded, raw) {
			t.Fatalf("vector mismatch %s", parts[0])
		}
	}
}
func TestCRC(t *testing.T) {
	if crc32.ChecksumIEEE([]byte("123456789")) != 0xcbf43926 {
		t.Fatal("crc")
	}
}
func TestSerialRecovery(t *testing.T) {
	now := time.Now()
	good, _ := EncodeSerial([]byte("EKVM payload"), 42)
	bad, _ := EncodeSerial(append([]byte("bad"), good...), 2)
	bad[len(bad)-1] ^= 1
	p := new(SerialParser)
	var frames []SerialFrame
	for _, v := range append([]byte("boot log"), bad...) {
		frames = append(frames, p.Feed([]byte{v}, now)...)
	}
	if len(frames) != 1 || frames[0].Seq != 42 {
		t.Fatal(frames)
	}
	p = new(SerialParser)
	h, _ := (Header{MaxBody, Serial, 3}).Encode()
	p.Feed(h[:], now)
	p.Feed(good, now.Add(100*time.Millisecond))
	frames = p.Feed(nil, now.Add(time.Second))
	if len(frames) != 1 || frames[0].Seq != 42 {
		t.Fatal("timeout recovery", frames)
	}
}
func TestEncryptedReplayAndTamper(t *testing.T) {
	key := make([]byte, 16)
	tx, _ := NewCipher(key, 0)
	rx, _ := NewCipher(key, 0)
	frame, _ := tx.Seal([]byte{0x9a, 1, 0})
	if _, e := rx.Open(frame[:12], frame[12:]); e != nil {
		t.Fatal(e)
	}
	if _, e := rx.Open(frame[:12], frame[12:]); e == nil {
		t.Fatal("replay accepted")
	}
	rx, _ = NewCipher(key, 0)
	frame[len(frame)-1] ^= 1
	if _, e := rx.Open(frame[:12], frame[12:]); e == nil {
		t.Fatal("bad tag accepted")
	}
}
func TestHeaderReject(t *testing.T) {
	h, _ := (Header{1, Encrypted, 0}).Encode()
	for _, offset := range []int{0, 6, 7} {
		b := h
		b[offset] ^= 0x80
		if _, e := DecodeHeader(b[:], Encrypted); e == nil {
			t.Fatalf("offset %d", offset)
		}
	}
}
func FuzzSerial(f *testing.F) {
	f.Add([]byte("EKVM"))
	f.Fuzz(func(t *testing.T, b []byte) {
		p := new(SerialParser)
		p.Feed(b, time.Unix(0, 0))
		p.Feed(nil, time.Unix(2, 0))
		if p.n > 528 {
			t.Fatal("overflow")
		}
	})
}
