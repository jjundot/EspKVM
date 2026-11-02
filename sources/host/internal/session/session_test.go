package session

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"github.com/jjundot/EspKVM/internal/protocol"
	"github.com/jjundot/EspKVM/internal/protocol/pb"
	"google.golang.org/protobuf/proto"
	"net"
	"testing"
	"time"
)

func device(c net.Conn, key []byte, corrupt bool) error {
	defer c.Close()
	c.SetDeadline(time.Now().Add(3 * time.Second))
	m := &pb.Msg{Body: &pb.Msg_Challenge{Challenge: &pb.Challenge{ProtoVer: protocol.Version, NonceD: make([]byte, 16), Mac: make([]byte, 6), Paired: true, Name: "test"}}}
	body, _ := proto.Marshal(m)
	h, _ := (protocol.Header{Length: uint16(len(body))}).Encode()
	raw := append(h[:], body...)
	sum := sha256.Sum256(raw)
	if e := writeAll(c, raw); e != nil {
		return e
	}
	_, _, body, e := protocol.ReadFrame(c, 0)
	if e != nil {
		return e
	}
	m.Reset()
	if e = proto.Unmarshal(body, m); e != nil {
		return e
	}
	auth := m.GetAuth()
	if auth == nil {
		return protocol.ErrFrame
	}
	bound, _ := protocol.AuthContext(sum[:], auth.Owner, auth.Takeover)
	expected, _ := protocol.Proof(key, bound, auth.NonceC, "EK-AUTH-C")
	if !hmac.Equal(expected, auth.Hmac) {
		return protocol.ErrFrame
	}
	proof, _ := protocol.Proof(key, bound, auth.NonceC, "EK-AUTH-D")
	if corrupt {
		proof[0] ^= 1
	}
	if e = plain(c, &pb.Msg{Body: &pb.Msg_AuthOk{AuthOk: &pb.AuthOk{Hmac: proof}}}); e != nil {
		return e
	}
	if corrupt {
		return nil
	}
	derived, _ := protocol.SessionKey(key, bound, auth.NonceC)
	rx, _ := protocol.NewCipher(derived, 0)
	_, hdr, b, e := protocol.ReadFrame(c, protocol.Encrypted)
	if e != nil {
		return e
	}
	body, e = rx.Open(hdr, b)
	if e != nil {
		return e
	}
	m.Reset()
	if e = proto.Unmarshal(body, m); e != nil {
		return e
	}
	if m.GetReleaseAll() == nil {
		return protocol.ErrFrame
	}
	tx, _ := protocol.NewCipher(derived, 1)
	reply, _ := proto.Marshal(&pb.Msg{Body: &pb.Msg_Released{Released: &pb.Released{Id: m.GetReleaseAll().Id}}})
	raw, e = tx.Seal(reply)
	if e != nil {
		return e
	}
	return writeAll(c, raw)
}
func TestMutualAuthenticationAndRelease(t *testing.T) {
	client, server := net.Pipe()
	key := make([]byte, 16)
	finished := make(chan error, 1)
	go func() { finished <- device(server, key, false) }()
	s, e := Handshake(client, key)
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer s.Close()
	go s.Run(ctx, nil)
	select {
	case e = <-s.Release(1):
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("release stalled")
	}
	if e = <-finished; e != nil {
		t.Fatal(e)
	}
}
func TestRejectDeviceProof(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	key := make([]byte, 16)
	go device(server, key, true)
	if _, e := Handshake(client, key); e == nil {
		t.Fatal("bad device proof accepted")
	}
}
func TestQueueOverflowCloses(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	s := &Session{conn: client, queue: make(chan pending, 128), done: make(chan struct{})}
	for i := 0; i < 128; i++ {
		if e := s.Send(&pb.Msg{Body: &pb.Msg_Heartbeat{Heartbeat: &pb.Heartbeat{Id: 1}}}, 0, false); e != nil {
			t.Fatal(e)
		}
	}
	if e := s.Send(&pb.Msg{Body: &pb.Msg_Heartbeat{Heartbeat: &pb.Heartbeat{Id: 1}}}, 0, false); e == nil {
		t.Fatal("overflow accepted")
	}
	select {
	case <-s.Done():
	default:
		t.Fatal("session not closed")
	}
}

func releaseTestSession(t *testing.T) *Session {
	t.Helper()
	c, peer := net.Pipe()
	t.Cleanup(func() { peer.Close() })
	go func() {
		rx, _ := protocol.NewCipher(make([]byte, 16), 0)
		tx, _ := protocol.NewCipher(make([]byte, 16), 1)
		for {
			_, h, b, e := protocol.ReadFrame(peer, protocol.Encrypted)
			if e != nil {
				return
			}
			b, e = rx.Open(h, b)
			if e != nil {
				return
			}
			m := new(pb.Msg)
			if proto.Unmarshal(b, m) != nil {
				return
			}
			if r := m.GetReleaseAll(); r != nil {
				b, _ = proto.Marshal(&pb.Msg{Body: &pb.Msg_Released{Released: &pb.Released{Id: r.Id}}})
				b, e = tx.Seal(b)
				if e != nil {
					return
				}
				if writeAll(peer, b) != nil {
					return
				}
			}
		}
	}()
	tx, _ := protocol.NewCipher(make([]byte, 16), 0)
	rx, _ := protocol.NewCipher(make([]byte, 16), 1)
	s := &Session{conn: c, reader: bufio.NewReader(c), rx: rx, epoch: time.Now(), tx: tx, queue: make(chan pending, 128), control: make(chan struct{}, 1), done: make(chan struct{})}
	t.Cleanup(s.Close)
	return s
}
func awaitRelease(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case e := <-ch:
		return e
	case <-time.After(time.Second):
		t.Fatal("release stalled")
		return nil
	}
}
func TestReleaseAfterClose(t *testing.T) {
	s := releaseTestSession(t)
	s.Close()
	for i := 0; i < 100; i++ {
		if e := awaitRelease(t, s.Release(uint64(i))); e != ErrClosed {
			t.Fatal(e)
		}
	}
}
func TestRepeatedRelease(t *testing.T) {
	s := releaseTestSession(t)
	a, b := s.Release(1), s.Release(2)
	select {
	case <-s.Done():
		t.Fatal("repeated release closed session")
	default:
	}
	go s.Run(context.Background(), nil)
	if e := awaitRelease(t, a); e != nil {
		t.Fatal(e)
	}
	if e := awaitRelease(t, b); e != nil {
		t.Fatal(e)
	}
}
func TestWriterFailureCompletesReleases(t *testing.T) {
	s := releaseTestSession(t)
	a, b := s.Release(1), s.Release(2)
	s.conn.Close()
	go s.writer()
	if e := awaitRelease(t, a); e == nil {
		t.Fatal("missing write failure")
	}
	if e := awaitRelease(t, b); e != ErrClosed {
		t.Fatal(e)
	}
}
func TestReleaseWithoutWriterHasBoundedCompletion(t *testing.T) {
	s := releaseTestSession(t)
	if e := awaitRelease(t, s.Release(1)); e != ErrClosed {
		t.Fatal(e)
	}
	select {
	case <-s.Done():
	default:
		t.Fatal("unconfirmed barrier did not close session")
	}
}
func TestProbeIgnoresUnknownDuplicateAndExpiredReplies(t *testing.T) {
	s := releaseTestSession(t)
	now := time.Now()
	s.probeID = 1
	s.probeAt = now
	s.acceptProbe(2, now.Add(10*time.Millisecond))
	if s.RTT().Count != 0 {
		t.Fatal("unknown reply counted")
	}
	s.acceptProbe(1, now.Add(10*time.Millisecond))
	s.acceptProbe(1, now.Add(20*time.Millisecond))
	s.probeID = 2
	s.probeAt = now
	s.acceptProbe(2, now.Add(2*time.Second))
	stats := s.RTT()
	if stats.Count != 1 || stats.Window != 1 || stats.P50 != 10*time.Millisecond {
		t.Fatal(stats)
	}
}
