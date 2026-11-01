// Package session owns each connection's handshake, cipher and bounded writer.
package session

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"github.com/jjun/esp-kvm/internal/protocol"
	"github.com/jjun/esp-kvm/internal/protocol/pb"
	"google.golang.org/protobuf/proto"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

var ErrClosed = errors.New("session closed or congested")

type pending struct {
	msg        *pb.Msg
	generation uint64
	input      bool
	at         time.Time
	done       chan error
}
type Session struct {
	conn       net.Conn
	reader     *bufio.Reader
	tx, rx     *protocol.Cipher
	queue      chan pending
	control    chan pending
	done       chan struct{}
	once       sync.Once
	generation atomic.Uint64
	last       atomic.Int64
	epoch      time.Time
	Challenge  *pb.Challenge
}

func writeAll(w io.Writer, b []byte) error {
	for len(b) > 0 {
		n, e := w.Write(b)
		if e != nil {
			return e
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		b = b[n:]
	}
	return nil
}
func plain(c net.Conn, m *pb.Msg) error {
	b, e := proto.Marshal(m)
	if e != nil {
		return e
	}
	h, e := (protocol.Header{Length: uint16(len(b))}).Encode()
	if e != nil {
		return e
	}
	return writeAll(c, append(h[:], b...))
}
func Dial(ctx context.Context, address string, psk []byte) (*Session, error) {
	c, e := (&net.Dialer{Timeout: 2 * time.Second, KeepAlive: time.Second}).DialContext(ctx, "tcp", address)
	if e != nil {
		return nil, e
	}
	s, e := Handshake(c, psk)
	if e != nil {
		c.Close()
		return nil, e
	}
	return s, nil
}
func Handshake(c net.Conn, psk []byte) (*Session, error) {
	if len(psk) != 16 {
		return nil, protocol.ErrFrame
	}
	total := time.Now().Add(2 * time.Second)
	c.SetDeadline(total)
	r := bufio.NewReader(c)
	read := func() (protocol.Header, []byte, []byte, error) {
		c.SetReadDeadline(total)
		if _, err := r.Peek(1); err != nil {
			return protocol.Header{}, nil, nil, err
		}
		limit := time.Now().Add(time.Second)
		if total.Before(limit) {
			limit = total
		}
		c.SetReadDeadline(limit)
		return protocol.ReadFrame(r, 0)
	}
	_, hdr, body, e := read()
	if e != nil {
		return nil, e
	}
	m := new(pb.Msg)
	if e = proto.Unmarshal(body, m); e != nil {
		return nil, e
	}
	ch := m.GetChallenge()
	if ch == nil || ch.ProtoVer != 1 || !ch.Paired || len(ch.NonceD) != 16 || len(ch.Mac) != 6 || len(ch.Name) > 31 {
		return nil, protocol.ErrFrame
	}
	transcript := sha256.Sum256(append(hdr, body...))
	nonce := make([]byte, 16)
	if _, e = rand.Read(nonce); e != nil {
		return nil, e
	}
	proof, _ := protocol.Proof(psk, transcript[:], nonce, "EK-AUTH-C")
	if e = plain(c, &pb.Msg{Body: &pb.Msg_Auth{Auth: &pb.Auth{NonceC: nonce, Hmac: proof}}}); e != nil {
		return nil, e
	}
	_, _, body, e = read()
	if e != nil {
		return nil, e
	}
	m.Reset()
	if e = proto.Unmarshal(body, m); e != nil {
		return nil, e
	}
	ok := m.GetAuthOk()
	expected, _ := protocol.Proof(psk, transcript[:], nonce, "EK-AUTH-D")
	if ok == nil || !hmac.Equal(expected, ok.Hmac) {
		return nil, errors.New("device authentication failed")
	}
	key, _ := protocol.SessionKey(psk, transcript[:], nonce)
	tx, _ := protocol.NewCipher(key, 0)
	rx, _ := protocol.NewCipher(key, 1)
	c.SetDeadline(time.Time{})
	s := &Session{conn: c, reader: r, tx: tx, rx: rx, queue: make(chan pending, 128), control: make(chan pending, 1), done: make(chan struct{}), Challenge: ch}
	s.epoch = time.Now()
	return s, nil
}
func (s *Session) Close()                { s.once.Do(func() { close(s.done); s.conn.Close() }) }
func (s *Session) Done() <-chan struct{} { return s.done }
func (s *Session) Generation() uint64    { return s.generation.Load() }
func (s *Session) Send(m *pb.Msg, generation uint64, input bool) error {
	if protocol.ValidateMessage(m, false) != nil {
		return protocol.ErrFrame
	}
	p := pending{msg: proto.Clone(m).(*pb.Msg), generation: generation, input: input, at: time.Now()}
	select {
	case <-s.done:
		return ErrClosed
	default:
	}
	select {
	case s.queue <- p:
		return nil
	default:
		s.Close()
		return ErrClosed
	}
}

// Release invalidates queued input before asking the sole writer for a barrier.
func (s *Session) Release(generation uint64) <-chan error {
	s.generation.Store(generation)
	result := make(chan error, 1)
	p := pending{msg: &pb.Msg{Body: &pb.Msg_ReleaseAll{ReleaseAll: &pb.Empty{}}}, done: result}
	select {
	case s.control <- p:
	case <-s.done:
		result <- ErrClosed
	default:
		s.Close()
		result <- ErrClosed
	}
	return result
}
func (s *Session) send(p pending) error {
	if p.input && (p.generation != s.generation.Load()) {
		return nil
	}
	if p.input && time.Since(p.at) > 100*time.Millisecond {
		return ErrClosed
	}
	b, e := proto.MarshalOptions{}.MarshalAppend(nil, p.msg)
	if e == nil {
		b, e = s.tx.Seal(b)
	}
	if e == nil {
		s.conn.SetWriteDeadline(time.Now().Add(100 * time.Millisecond))
		e = writeAll(s.conn, b)
	}
	if p.done != nil {
		p.done <- e
	}
	return e
}
func (s *Session) writer() {
	defer s.Close()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case p := <-s.control:
			if s.send(p) != nil {
				return
			}
			continue
		default:
		}
		select {
		case <-s.done:
			return
		case p := <-s.control:
			if s.send(p) != nil {
				return
			}
		case p := <-s.queue:
			if s.send(p) != nil {
				return
			}
		case <-tick.C:
			if s.send(pending{msg: &pb.Msg{Body: &pb.Msg_Heartbeat{Heartbeat: &pb.Empty{}}}}) != nil {
				return
			}
		}
	}
}

// Run starts writer and independent inactivity watchdog. onMessage must not block.
func (s *Session) Run(ctx context.Context, onMessage func(*pb.Msg)) error {
	defer s.Close()
	go s.writer()
	go func() {
		t := time.NewTicker(50 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				s.Close()
				return
			case <-s.done:
				return
			case <-t.C:
				if time.Since(s.epoch.Add(time.Duration(s.last.Load()))) >= 3*time.Second {
					s.Close()
					return
				}
			}
		}
	}()
	for {
		// Idle waiting is bounded by the watchdog; the frame deadline starts on its first byte.
		s.conn.SetReadDeadline(s.epoch.Add(time.Duration(s.last.Load())).Add(3 * time.Second))
		if _, e := s.reader.Peek(1); e != nil {
			return e
		}
		deadline := time.Now().Add(time.Second)
		idle := s.epoch.Add(time.Duration(s.last.Load())).Add(3 * time.Second)
		if idle.Before(deadline) {
			deadline = idle
		}
		s.conn.SetReadDeadline(deadline)
		_, h, b, e := protocol.ReadFrame(s.reader, protocol.Encrypted)
		if e != nil {
			return e
		}
		b, e = s.rx.Open(h, b)
		if e != nil {
			return e
		}
		m := new(pb.Msg)
		if e = proto.Unmarshal(b, m); e != nil {
			return e
		}
		if e = protocol.ValidateMessage(m, true); e != nil {
			return e
		}
		s.last.Store(int64(time.Since(s.epoch)))
		if onMessage != nil {
			onMessage(m)
		}
	}
}
