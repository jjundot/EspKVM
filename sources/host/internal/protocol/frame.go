package protocol

import (
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"time"
)

const (
	HeaderLen     = 12
	MaxBody       = 512
	Encrypted     = 1
	Serial        = 2
	SequenceLimit = 0xffffff00
)

var ErrFrame = errors.New("invalid EK frame")

type Header struct {
	Length uint16
	Flags  byte
	Seq    uint32
}

func (h Header) Encode() ([HeaderLen]byte, error) {
	var b [HeaderLen]byte
	if h.Length == 0 || h.Length > MaxBody || h.Flags > Serial || (h.Flags == 0 && h.Seq != 0) || (h.Flags == Serial && h.Seq == 0) {
		return b, ErrFrame
	}
	copy(b[:4], "EKVM")
	binary.LittleEndian.PutUint16(b[4:6], h.Length)
	b[6] = h.Flags
	binary.LittleEndian.PutUint32(b[8:], h.Seq)
	return b, nil
}
func DecodeHeader(b []byte, flags byte) (Header, error) {
	if len(b) != HeaderLen || string(b[:4]) != "EKVM" || b[7] != 0 || b[6] != flags {
		return Header{}, ErrFrame
	}
	h := Header{binary.LittleEndian.Uint16(b[4:6]), b[6], binary.LittleEndian.Uint32(b[8:])}
	_, err := h.Encode()
	return h, err
}
func EncodeSerial(body []byte, seq uint32) ([]byte, error) {
	if len(body) > MaxBody {
		return nil, ErrFrame
	}
	h, err := (Header{uint16(len(body)), Serial, seq}).Encode()
	if err != nil {
		return nil, err
	}
	b := make([]byte, HeaderLen+len(body)+4)
	copy(b, h[:])
	copy(b[HeaderLen:], body)
	binary.LittleEndian.PutUint32(b[len(b)-4:], crc32.ChecksumIEEE(b[:len(b)-4]))
	return b, nil
}
func ReadFrame(r io.Reader, flags byte) (Header, []byte, []byte, error) {
	raw := make([]byte, HeaderLen)
	if _, err := io.ReadFull(r, raw); err != nil {
		return Header{}, nil, nil, err
	}
	h, err := DecodeHeader(raw, flags)
	if err != nil {
		return h, nil, nil, err
	}
	n := int(h.Length)
	if flags == Encrypted {
		n += 16
	}
	if flags == Serial {
		n += 4
	}
	body := make([]byte, n)
	_, err = io.ReadFull(r, body)
	return h, raw, body, err
}

// SerialParser holds at most one maximum frame, including arrival times.
// A failed candidate advances one byte, retaining nested candidates.
type SerialParser struct {
	data    [528]byte
	arrived [528]time.Time
	n       int
}
type SerialFrame struct {
	Seq  uint32
	Body []byte
}

func (p *SerialParser) poll(now time.Time) []SerialFrame {
	var out []SerialFrame
	discard := func(n int) { copy(p.data[:], p.data[n:p.n]); copy(p.arrived[:], p.arrived[n:p.n]); p.n -= n }
	for p.n >= 4 {
		if string(p.data[:4]) != "EKVM" {
			discard(1)
			continue
		}
		if now.Sub(p.arrived[0]) >= time.Second {
			discard(1)
			continue
		}
		if p.n < HeaderLen {
			break
		}
		h, err := DecodeHeader(p.data[:HeaderLen], Serial)
		if err != nil {
			discard(1)
			continue
		}
		total := HeaderLen + int(h.Length) + 4
		if p.n < total {
			break
		}
		if crc32.ChecksumIEEE(p.data[:total-4]) != binary.LittleEndian.Uint32(p.data[total-4:total]) {
			discard(1)
			continue
		}
		out = append(out, SerialFrame{h.Seq, append([]byte(nil), p.data[HeaderLen:total-4]...)})
		discard(total)
	}
	return out
}
func (p *SerialParser) Feed(b []byte, now time.Time) []SerialFrame {
	out := p.poll(now)
	for _, v := range b {
		if p.n == len(p.data) {
			copy(p.data[:], p.data[1:])
			copy(p.arrived[:], p.arrived[1:])
			p.n--
		}
		p.data[p.n] = v
		p.arrived[p.n] = now
		p.n++
		out = append(out, p.poll(now)...)
	}
	return out
}
