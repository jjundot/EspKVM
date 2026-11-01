package protocol

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"errors"
)

func Proof(psk, transcript, nonce []byte, label string) ([]byte, error) {
	if len(psk) != 16 || len(transcript) != 32 || len(nonce) != 16 {
		return nil, ErrFrame
	}
	h := hmac.New(sha256.New, psk)
	h.Write([]byte(label))
	h.Write(transcript)
	h.Write(nonce)
	return h.Sum(nil), nil
}
func SessionKey(psk, transcript, nonce []byte) ([]byte, error) {
	p, e := Proof(psk, transcript, nonce, "EK-KEY")
	if e != nil {
		return nil, e
	}
	return p[:16], nil
}

type Cipher struct {
	aead      cipher.AEAD
	direction byte
	seq       uint32
}

func NewCipher(key []byte, direction byte) (*Cipher, error) {
	if len(key) != 16 || direction > 1 {
		return nil, ErrFrame
	}
	b, e := aes.NewCipher(key)
	if e != nil {
		return nil, e
	}
	a, e := cipher.NewGCM(b)
	return &Cipher{aead: a, direction: direction}, e
}
func (c *Cipher) nonce() [12]byte {
	var n [12]byte
	n[0] = c.direction
	binary.LittleEndian.PutUint32(n[4:8], c.seq)
	return n
}
func (c *Cipher) Seal(body []byte) ([]byte, error) {
	if c.seq >= SequenceLimit || len(body) == 0 || len(body) > MaxBody {
		return nil, ErrFrame
	}
	h, e := (Header{uint16(len(body)), Encrypted, c.seq}).Encode()
	if e != nil {
		return nil, e
	}
	n := c.nonce()
	out := make([]byte, HeaderLen, HeaderLen+len(body)+16)
	copy(out, h[:])
	out = c.aead.Seal(out, n[:], body, h[:])
	c.seq++
	return out, nil
}
func (c *Cipher) Open(header, body []byte) ([]byte, error) {
	h, e := DecodeHeader(header, Encrypted)
	if e != nil || h.Seq != c.seq || c.seq >= SequenceLimit || len(body) != int(h.Length)+16 {
		return nil, ErrFrame
	}
	n := c.nonce()
	p, e := c.aead.Open(nil, n[:], body, header)
	if e != nil {
		return nil, errors.New("authentication failed")
	}
	c.seq++
	return p, nil
}
