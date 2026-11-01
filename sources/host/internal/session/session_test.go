package session

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"github.com/jjun/esp-kvm/internal/protocol"
	"github.com/jjun/esp-kvm/internal/protocol/pb"
	"google.golang.org/protobuf/proto"
	"net"
	"testing"
	"time"
)

func device(c net.Conn, key []byte, corrupt bool) error {
	defer c.Close()
	c.SetDeadline(time.Now().Add(3 * time.Second))
	m := &pb.Msg{Body: &pb.Msg_Challenge{Challenge: &pb.Challenge{ProtoVer: 1, NonceD: make([]byte, 16), Mac: make([]byte, 6), Paired: true, Name: "test"}}}
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
	expected, _ := protocol.Proof(key, sum[:], auth.NonceC, "EK-AUTH-C")
	if !hmac.Equal(expected, auth.Hmac) {
		return protocol.ErrFrame
	}
	proof, _ := protocol.Proof(key, sum[:], auth.NonceC, "EK-AUTH-D")
	if corrupt {
		proof[0] ^= 1
	}
	if e = plain(c, &pb.Msg{Body: &pb.Msg_AuthOk{AuthOk: &pb.AuthOk{Hmac: proof}}}); e != nil {
		return e
	}
	if corrupt {
		return nil
	}
	derived, _ := protocol.SessionKey(key, sum[:], auth.NonceC)
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
	return nil
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
		if e := s.Send(&pb.Msg{Body: &pb.Msg_Heartbeat{Heartbeat: &pb.Empty{}}}, 0, false); e != nil {
			t.Fatal(e)
		}
	}
	if e := s.Send(&pb.Msg{Body: &pb.Msg_Heartbeat{Heartbeat: &pb.Empty{}}}, 0, false); e == nil {
		t.Fatal("overflow accepted")
	}
	select {
	case <-s.Done():
	default:
		t.Fatal("session not closed")
	}
}
