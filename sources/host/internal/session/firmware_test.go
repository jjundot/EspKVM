package session

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"github.com/jjundot/EspKVM/internal/protocol"
	"github.com/jjundot/EspKVM/internal/protocol/pb"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Runs the firmware's actual C session code with POSIX task and hardware adapters.
// This tests TCP/PSA/nanopb interoperability; it does not emulate WiFi or USB.
func firmware(t *testing.T) string {
	t.Helper()
	path := os.Getenv("ESPKVM_NETWORK_TEST_SERVER")
	if path == "" {
		t.Skip("native firmware server not configured")
	}
	cmd := exec.Command(path)
	out, e := cmd.StdoutPipe()
	if e != nil {
		t.Fatal(e)
	}
	cmd.Stderr = os.Stderr
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	ready := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(out)
		if scanner.Scan() {
			ready <- scanner.Text()
		} else {
			ready <- ""
		}
	}()
	select {
	case line := <-ready:
		var port int
		if _, e := fmt.Sscanf(line, "READY %d", &port); e != nil {
			t.Fatal(line, e)
		}
		return fmt.Sprintf("127.0.0.1:%d", port)
	case <-time.After(3 * time.Second):
		t.Fatal("firmware server did not start")
		return ""
	}
}
func firmwareDial(t *testing.T, addr string, o Options) *Session {
	t.Helper()
	s, e := DialWithOptions(context.Background(), addr, make([]byte, 16), o)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(s.Close)
	return s
}
func TestFirmwareAuthenticationAndConfig(t *testing.T) {
	addr := firmware(t)
	s := firmwareDial(t, addr, Options{Owner: [16]byte{1}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reply := make(chan *pb.Config, 1)
	go s.Run(ctx, func(m *pb.Msg) {
		if c := m.GetCfgData(); c != nil {
			reply <- c
		}
	})
	if e := s.Send(&pb.Msg{Body: &pb.Msg_CfgGet{CfgGet: &pb.Empty{}}}, 0, false); e != nil {
		t.Fatal(e)
	}
	select {
	case c := <-reply:
		if c.Password != nil || c.Psk != nil || c.Name != "native firmware test" {
			t.Fatal("invalid/redaction", c)
		}
	case <-time.After(time.Second):
		t.Fatal("missing CFG_DATA")
	}
	if e := awaitRelease(t, s.Release(1)); e != nil {
		t.Fatal(e)
	}
}
func TestFirmwareTakeover(t *testing.T) {
	addr := firmware(t)
	owner := Options{Owner: [16]byte{1}}
	first := firmwareDial(t, addr, owner)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ended := make(chan error, 1)
	go func() { ended <- first.Run(ctx, nil) }()
	if s, e := DialWithOptions(ctx, addr, make([]byte, 16), Options{Owner: [16]byte{2}}); e == nil {
		s.Close()
		t.Fatal("implicit foreign takeover accepted")
	}
	select {
	case e := <-ended:
		t.Fatal("unauthorized connection interrupted owner", e)
	default:
	}
	second := firmwareDial(t, addr, Options{Owner: [16]byte{2}, Takeover: true})
	select {
	case e := <-ended:
		if !errors.Is(e, ErrReplaced) {
			t.Fatal("missing authenticated replacement reason", e)
		}
	case <-time.After(time.Second):
		t.Fatal("old owner stayed active")
	}
	done := make(chan error, 1)
	go func() { done <- second.Run(ctx, nil) }()
	third := firmwareDial(t, addr, Options{Owner: [16]byte{2}})
	_ = third
	select {
	case e := <-done:
		if !errors.Is(e, ErrReplaced) {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("same-owner reconnect failed")
	}
}
func TestFirmwareWatchdog(t *testing.T) {
	s := firmwareDial(t, firmware(t), Options{Owner: [16]byte{1}})
	start := time.Now()
	s.conn.SetReadDeadline(start.Add(4 * time.Second))
	_, e := io.Copy(io.Discard, s.conn)
	if e != nil {
		t.Fatal(e)
	}
	if elapsed := time.Since(start); elapsed < 2800*time.Millisecond || elapsed > 3500*time.Millisecond {
		t.Fatal("watchdog deadline", elapsed)
	}
}
func TestFirmwareDirectionAndMissingAuth(t *testing.T) {
	addr := firmware(t)
	s := firmwareDial(t, addr, Options{Owner: [16]byte{1}})
	// Bypass host validation to inject an authenticated but invalid direction.
	if e := s.send(pending{msg: &pb.Msg{Body: &pb.Msg_Status{Status: &pb.Status{}}}}); e != nil {
		t.Fatal(e)
	}
	s.conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, e := io.Copy(io.Discard, s.conn); e != nil {
		t.Fatal(e)
	}
	c, e := net.Dial("tcp", addr)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(time.Second))
	if _, _, _, e = protocol.ReadFrame(c, 0); e != nil {
		t.Fatal(e)
	}
	if e = plain(c, &pb.Msg{Body: &pb.Msg_Auth{Auth: &pb.Auth{}}}); e != nil {
		t.Fatal(e)
	}
	var b [1]byte
	if _, e = c.Read(b[:]); e == nil {
		t.Fatal("missing auth fields accepted")
	}
}
func TestFirmwareThreeHandshakeSlots(t *testing.T) {
	addr := firmware(t)
	var connections []net.Conn
	for i := 2; i <= 4; i++ {
		dialer := net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP(fmt.Sprintf("127.0.0.%d", i))}}
		c, e := dialer.Dial("tcp", addr)
		if e != nil {
			t.Fatal(e)
		}
		defer c.Close()
		connections = append(connections, c)
		c.SetReadDeadline(time.Now().Add(time.Second))
		if _, _, _, e := protocol.ReadFrame(c, 0); e != nil {
			t.Fatal(e)
		}
	}
	c, e := net.Dial("tcp", addr)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	c.SetReadDeadline(time.Now().Add(time.Second))
	var b [1]byte
	if _, e = c.Read(b[:]); e == nil || strings.Contains(e.Error(), "timeout") {
		t.Fatal("full slots did not reject promptly", e)
	}
	for _, c := range connections {
		c.SetReadDeadline(time.Now().Add(2500 * time.Millisecond))
		if _, e := io.Copy(io.Discard, c); e != nil {
			t.Fatal(e)
		}
	}
	firmwareDial(t, addr, Options{Owner: [16]byte{1}})
}
