// Package session owns each connection's handshake, cipher and bounded writer.
package session

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"github.com/jjundot/EspKVM/internal/protocol"
	"github.com/jjundot/EspKVM/internal/protocol/pb"
	"google.golang.org/protobuf/proto"
	"io"
	"net"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

var ErrClosed = errors.New("session closed or congested")

// Release confirmation bounds. A sleeping or re-enumerating USB host can delay the
// device's release barrier, so the bound is extended while the device reports it.
const (
	ReleaseTimeout         = 500 * time.Millisecond
	SleepingReleaseTimeout = 20 * time.Second
)

type pending struct {
	msg        *pb.Msg
	generation uint64
	input      bool
	at         time.Time
	barrier    uint32
}
type releaseWaiter struct {
	result chan error
	timer  *time.Timer
}
type Session struct {
	releaseID      uint32
	releaseWaiters map[uint32]releaseWaiter
	probeMu        sync.Mutex
	probeID        uint32
	probeAt        time.Time
	samples        []time.Duration
	sampleCount    uint64
	conn           net.Conn
	reader         *bufio.Reader
	tx, rx         *protocol.Cipher
	queue          chan pending
	control        chan struct{}
	controlMu      sync.Mutex
	releases       []pending
	done           chan struct{}
	once           sync.Once
	generation     atomic.Uint64
	sleeping       atomic.Bool
	last           atomic.Int64
	epoch          time.Time
	Challenge      *pb.Challenge
	HostActivity   func() (uint32, bool)
	HostLock       func() uint32
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
	return DialWithOptions(ctx, address, psk, Options{Owner: processOwner})
}
func DialWithOptions(ctx context.Context, address string, psk []byte, options Options) (*Session, error) {
	c, e := (&net.Dialer{Timeout: 2 * time.Second, KeepAlive: time.Second}).DialContext(ctx, "tcp", address)
	if e != nil {
		return nil, e
	}
	s, e := HandshakeWithOptions(c, psk, options)
	if e != nil {
		c.Close()
		return nil, e
	}
	return s, nil
}

type Options struct {
	Owner    [16]byte
	Takeover bool
}

var processOwner = func() (owner [16]byte) {
	_, _ = rand.Read(owner[:])
	return
}()

func Handshake(c net.Conn, psk []byte) (*Session, error) {
	return HandshakeWithOptions(c, psk, Options{Owner: processOwner})
}
func HandshakeWithOptions(c net.Conn, psk []byte, options Options) (*Session, error) {
	if options.Owner == ([16]byte{}) {
		if _, e := rand.Read(options.Owner[:]); e != nil {
			return nil, e
		}
	}
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
	if ch == nil || ch.ProtoVer != protocol.Version || !ch.Paired || len(ch.NonceD) != 16 || len(ch.Mac) != 6 || len(ch.Name) > 31 {
		return nil, protocol.ErrFrame
	}
	transcript := sha256.Sum256(append(hdr, body...))
	nonce := make([]byte, 16)
	if _, e = rand.Read(nonce); e != nil {
		return nil, e
	}
	bound, _ := protocol.AuthContext(transcript[:], options.Owner[:], options.Takeover)
	proof, _ := protocol.Proof(psk, bound, nonce, "EK-AUTH-C")
	if e = plain(c, &pb.Msg{Body: &pb.Msg_Auth{Auth: &pb.Auth{NonceC: nonce, Hmac: proof, Owner: options.Owner[:], Takeover: options.Takeover}}}); e != nil {
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
	expected, _ := protocol.Proof(psk, bound, nonce, "EK-AUTH-D")
	if ok == nil || !hmac.Equal(expected, ok.Hmac) {
		return nil, errors.New("device authentication failed")
	}
	key, _ := protocol.SessionKey(psk, bound, nonce)
	tx, _ := protocol.NewCipher(key, 0)
	rx, _ := protocol.NewCipher(key, 1)
	c.SetDeadline(time.Time{})
	s := &Session{conn: c, reader: r, tx: tx, rx: rx, queue: make(chan pending, 128), control: make(chan struct{}, 1), done: make(chan struct{}), Challenge: ch}
	s.epoch = time.Now()
	return s, nil
}
func (s *Session) Close() {
	s.once.Do(func() {
		s.controlMu.Lock()
		close(s.done)
		for _, p := range s.releaseWaiters {
			p.timer.Stop()
			p.result <- ErrClosed
		}
		s.releaseWaiters = nil
		s.releases = nil
		s.controlMu.Unlock()
		s.conn.Close()
	})
}
func (s *Session) Done() <-chan struct{} { return s.done }
func (s *Session) Generation() uint64    { return s.generation.Load() }
func (s *Session) Send(m *pb.Msg, generation uint64, input bool) error {
	if protocol.ValidateMessage(m, false) != nil {
		return protocol.ErrFrame
	}
	p := pending{msg: proto.Clone(m).(*pb.Msg), generation: generation, input: input, at: time.Now()}
	s.controlMu.Lock()
	select {
	case <-s.done:
		s.controlMu.Unlock()
		return ErrClosed
	default:
	}
	select {
	case s.queue <- p:
		s.controlMu.Unlock()
		return nil
	default:
		s.controlMu.Unlock()
		s.Close()
		return ErrClosed
	}
}

// Release invalidates queued input before asking the sole writer for a barrier.
func (s *Session) Release(generation uint64) <-chan error {
	result := make(chan error, 1)
	s.controlMu.Lock()
	select {
	case <-s.done:
		s.controlMu.Unlock()
		result <- ErrClosed
		return result
	default:
	}
	if len(s.releaseWaiters) >= 128 || s.releaseID == ^uint32(0) {
		s.controlMu.Unlock()
		s.Close()
		result <- ErrClosed
		return result
	}
	s.generation.Store(generation)
	s.releaseID++
	id := s.releaseID
	if s.releaseWaiters == nil {
		s.releaseWaiters = map[uint32]releaseWaiter{}
	}
	timeout := ReleaseTimeout
	if s.sleeping.Load() {
		timeout = SleepingReleaseTimeout
	}
	timer := time.AfterFunc(timeout, s.Close)
	s.releaseWaiters[id] = releaseWaiter{result, timer}
	s.releases = append(s.releases, pending{msg: &pb.Msg{Body: &pb.Msg_ReleaseAll{ReleaseAll: &pb.ReleaseAll{Id: id}}}, barrier: id})
	select {
	case s.control <- struct{}{}:
	default:
	}
	s.controlMu.Unlock()
	return result
}
func (s *Session) finishRelease(id uint32, e error) bool {
	s.controlMu.Lock()
	defer s.controlMu.Unlock()
	p, ok := s.releaseWaiters[id]
	if !ok {
		return false
	}
	delete(s.releaseWaiters, id)
	p.timer.Stop()
	p.result <- e
	return true
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
	if p.barrier != 0 && e != nil {
		s.finishRelease(p.barrier, e)
	}
	return e
}
func (s *Session) nextRelease() (pending, bool) {
	s.controlMu.Lock()
	defer s.controlMu.Unlock()
	if len(s.releases) == 0 {
		return pending{}, false
	}
	p := s.releases[0]
	s.releases[0] = pending{}
	s.releases = s.releases[1:]
	return p, true
}
func (s *Session) writer() {
	defer s.Close()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-s.done:
			return
		default:
		}
		if p, ok := s.nextRelease(); ok {
			if s.send(p) != nil {
				return
			}
			continue
		}
		select {
		case <-s.done:
			return
		case <-s.control:
		case p := <-s.queue:
			if s.send(p) != nil {
				return
			}
		case <-tick.C:
			if s.sendProbe() != nil {
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
		if status := m.GetStatus(); status != nil {
			s.sleeping.Store(status.UsbSuspended)
		}
		if h := m.GetHeartbeat(); h != nil {
			if h.Reply {
				s.acceptProbe(h.Id, time.Now())
			} else if e := s.Send(&pb.Msg{Body: &pb.Msg_Heartbeat{Heartbeat: s.heartbeat(h.Id, true)}}, 0, false); e != nil {
				return e
			}
		}
		if released := m.GetReleased(); released != nil {
			if !s.finishRelease(released.Id, nil) {
				return protocol.ErrFrame
			}
		}
		if m.GetSessionEnd() != nil {
			return ErrReplaced
		}
		if onMessage != nil {
			onMessage(m)
		}
	}
}

var ErrReplaced = errors.New("session replaced by authenticated owner; explicit takeover required")

type RTT struct {
	Count  uint64        `json:"count"`
	Window int           `json:"window"`
	P50    time.Duration `json:"p50_ns"`
	P95    time.Duration `json:"p95_ns"`
	P99    time.Duration `json:"p99_ns"`
}

func (s *Session) RTT() RTT {
	s.probeMu.Lock()
	defer s.probeMu.Unlock()
	values := append([]time.Duration(nil), s.samples...)
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	r := RTT{Count: s.sampleCount, Window: len(values)}
	if len(values) > 0 {
		r.P50 = values[(len(values)-1)*50/100]
		r.P95 = values[(len(values)-1)*95/100]
		r.P99 = values[(len(values)-1)*99/100]
	}
	return r
}
func (s *Session) sendProbe() error {
	s.probeMu.Lock()
	now := time.Now()
	if !s.probeAt.IsZero() && now.Sub(s.probeAt) < time.Second {
		s.probeMu.Unlock()
		return nil
	}
	s.probeID++
	if s.probeID == 0 {
		s.probeID++
	}
	id := s.probeID
	s.probeAt = now
	s.probeMu.Unlock()
	return s.send(pending{msg: &pb.Msg{Body: &pb.Msg_Heartbeat{Heartbeat: s.heartbeat(id, false)}}})
}
func (s *Session) heartbeat(id uint32, reply bool) *pb.Heartbeat {
	h := &pb.Heartbeat{Id: id, Reply: reply}
	if s.HostActivity != nil {
		h.HostIdleS, h.HostActivityKnown = s.HostActivity()
	}
	if s.HostLock != nil {
		h.HostLockState = s.HostLock()
	}
	return h
}
func (s *Session) acceptProbe(id uint32, now time.Time) {
	s.probeMu.Lock()
	defer s.probeMu.Unlock()
	if id != s.probeID || s.probeAt.IsZero() || now.Sub(s.probeAt) > time.Second {
		return
	}
	s.samples = append(s.samples, now.Sub(s.probeAt))
	if len(s.samples) > 128 {
		s.samples = s.samples[1:]
	}
	s.sampleCount++
	s.probeAt = time.Time{}
}
