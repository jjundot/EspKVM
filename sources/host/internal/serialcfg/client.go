// Package serialcfg implements single-flight transactions with no destructive retries.
package serialcfg

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"github.com/jjundot/EspKVM/internal/debuglog"
	"github.com/jjundot/EspKVM/internal/protocol"
	"github.com/jjundot/EspKVM/internal/protocol/pb"
	"go.bug.st/serial"
	"google.golang.org/protobuf/proto"
	"io"
	"sync"
	"time"
)

var ErrUnknown = errors.New("serial timeout: operation result unknown; inspect state before retrying")

type Client struct {
	mu     sync.Mutex
	port   serial.Port
	seq    uint32
	parser protocol.SerialParser
}

func Ports() ([]string, error) { return serial.GetPortsList() }
func Open(name string) (*Client, error) {
	p, e := serial.Open(name, &serial.Mode{BaudRate: 115200, DataBits: 8, Parity: serial.NoParity, StopBits: serial.OneStopBit})
	if e != nil {
		return nil, e
	}
	fail := func(e error) (*Client, error) { p.Close(); return nil, e }
	if e = p.ResetInputBuffer(); e != nil {
		return fail(e)
	}
	if e = p.SetReadTimeout(20 * time.Millisecond); e != nil {
		return fail(e)
	}
	var b [4]byte
	for binary.LittleEndian.Uint32(b[:]) == 0 {
		if _, e = rand.Read(b[:]); e != nil {
			return fail(e)
		}
	}
	return &Client{port: p, seq: binary.LittleEndian.Uint32(b[:])}, nil
}
func (c *Client) Close() error { return c.port.Close() }

// logText returns a device log frame's text; logs carry their own sequence and
// never answer a request.
func logText(body []byte) (string, bool) {
	p := new(pb.SerialPacket)
	if proto.Unmarshal(body, p) != nil || p.ProtoVer != protocol.Version || p.GetLog() == nil {
		return "", false
	}
	return p.GetLog().Text, true
}

// Logs prints device log frames until the deadline or a read error.
func (c *Client) Logs(until time.Time, print func(string)) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var buf [528]byte
	for until.IsZero() || time.Now().Before(until) {
		n, e := c.port.Read(buf[:])
		if e != nil {
			return e
		}
		for _, frame := range c.parser.Feed(buf[:n], time.Now()) {
			if text, ok := logText(frame.Body); ok {
				print(text)
			}
		}
	}
	return nil
}
func requestKind(p *pb.SerialPacket) byte {
	switch p.Body.(type) {
	case *pb.SerialPacket_InfoGet:
		return 1
	case *pb.SerialPacket_CfgSet:
		return 2
	case *pb.SerialPacket_Reboot, *pb.SerialPacket_FactoryReset:
		return 3
	}
	return 0
}
func (c *Client) Request(p *pb.SerialPacket) (*pb.SerialPacket, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	kind := requestKind(p)
	if kind == 0 {
		return nil, protocol.ErrFrame
	}
	if c.seq == 0 {
		return nil, errors.New("request id exhausted; reopen serial port")
	}
	seq := c.seq
	c.seq++
	p = proto.Clone(p).(*pb.SerialPacket)
	p.ProtoVer = protocol.Version
	b, e := proto.Marshal(p)
	if e != nil {
		return nil, e
	}
	b, e = protocol.EncodeSerial(b, seq)
	if e != nil {
		return nil, e
	}
	// A timeout closes the port, bounding a driver-blocked write as well as reads.
	timer := time.AfterFunc(3*time.Second, func() { c.port.Close() })
	defer timer.Stop()
	end := time.Now().Add(3 * time.Second)
	for len(b) > 0 {
		n, e := c.port.Write(b)
		if e != nil {
			return nil, ErrUnknown
		}
		if n == 0 {
			return nil, io.ErrShortWrite
		}
		b = b[n:]
	}
	var buf [528]byte
	for time.Now().Before(end) {
		n, e := c.port.Read(buf[:])
		if e != nil {
			return nil, ErrUnknown
		}
		for _, frame := range c.parser.Feed(buf[:n], time.Now()) {
			if text, ok := logText(frame.Body); ok {
				debuglog.Printf("device", "%s", text)
				continue
			}
			if frame.Seq != seq {
				continue
			}
			reply := new(pb.SerialPacket)
			if proto.Unmarshal(frame.Body, reply) != nil || reply.ProtoVer != protocol.Version || reply.Body == nil {
				continue
			}
			if a := reply.GetAck(); a != nil {
				if a.Code != 0 {
					return nil, errors.New("device rejected serial request")
				}
				if kind == 3 {
					return reply, nil
				}
				continue
			}
			if kind == 1 && reply.GetInfo() != nil {
				info := reply.GetInfo()
				if len(info.Mac) != 6 || info.Config == nil || info.Config.Password != nil || info.Config.Psk != nil {
					return nil, protocol.ErrFrame
				}
				if info.Paired {
					if _, e := protocol.ValidateConfig(info.Config, true); e != nil {
						return nil, e
					}
				}
				return reply, nil
			}
			if kind == 2 && reply.GetCfgResult() != nil {
				if reply.GetCfgResult().Code > 3 || reply.GetCfgResult().Field > 11 {
					return nil, protocol.ErrFrame
				}
				return reply, nil
			}
		}
	}
	return nil, ErrUnknown
}
