package config

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/jjundot/EspKVM/internal/protocol"
	"github.com/jjundot/EspKVM/internal/protocol/pb"
	"github.com/jjundot/EspKVM/internal/session"
	"google.golang.org/protobuf/proto"
	"net"
	"strconv"
	"time"
)

type SerialRequester interface {
	Request(*pb.SerialPacket) (*pb.SerialPacket, error)
}

func ipv4(n uint32) string { return net.IPv4(byte(n>>24), byte(n>>16), byte(n>>8), byte(n)).String() }

// Provision saves old/new credentials before the only CFG_SET attempt.
func (s *Store) Provision(c SerialRequester, id string, candidate *pb.Config) error {
	if id == "" || candidate == nil {
		return errors.New("device id and full config required")
	}
	token, e := NewPSK()
	if e != nil {
		return e
	}
	candidate = proto.Clone(candidate).(*pb.Config)
	reply, e := c.Request(&pb.SerialPacket{Body: &pb.SerialPacket_InfoGet{InfoGet: &pb.Empty{}}})
	if e != nil {
		return e
	}
	info := reply.GetInfo()
	if info == nil {
		return protocol.ErrFrame
	}
	if field, validateErr := protocol.ValidateConfig(candidate, info.Paired); validateErr != nil {
		return fmt.Errorf("invalid configuration field %d: %w", field, validateErr)
	}
	e = s.Update(func(d *Data) error {
		for _, p := range d.Pending {
			if p.New.ID == id {
				return errors.New("unresolved transaction exists; verify or recover it first")
			}
		}
		var old *Device
		for _, v := range d.Devices {
			if v.ID == id {
				copy := v
				old = &copy
				break
			}
		}
		key := hex.EncodeToString(candidate.Psk)
		if candidate.Psk == nil {
			if old == nil {
				return errors.New("existing PSK unavailable; supply new PSK")
			}
			key = old.PSK
		}
		next := Device{ID: id, Name: candidate.Name, IP: ipv4(candidate.Ip), Port: uint16(candidate.TcpPort), PSK: key, Enabled: true}
		if old != nil {
			next.Enabled = old.Enabled
		}
		snapshot := proto.Clone(candidate).(*pb.Config)
		snapshot.Password = nil
		snapshot.Psk = nil
		d.Pending = append(d.Pending, Pending{Token: token, Old: old, New: next, State: "unknown", Expected: snapshot})
		return nil
	})
	if e != nil {
		return e
	}
	reply, e = c.Request(&pb.SerialPacket{Body: &pb.SerialPacket_CfgSet{CfgSet: candidate}})
	if e != nil {
		return e
	}
	result := reply.GetCfgResult()
	if result == nil {
		return protocol.ErrFrame
	}
	if result.Code == 1 || result.Code == 3 {
		if e := s.discardPending(id, &token); e != nil {
			return fmt.Errorf("configuration rejected (code %d); clearing pending: %w", result.Code, e)
		}
		return fmt.Errorf("configuration rejected (code %d, field %d); pending cleared", result.Code, result.Field)
	}
	if result.Code != 0 {
		return fmt.Errorf("configuration rejected (code %d, field %d); pending retained", result.Code, result.Field)
	}
	return s.Update(func(d *Data) error {
		for i := range d.Pending {
			if d.Pending[i].New.ID == id {
				d.Pending[i].State = "persisted"
			}
		}
		return nil
	})
}

// Verify makes one bounded authentication/read attempt; never retries CFG_SET.
func (s *Store) Verify(ctx context.Context, id string) error {
	return s.verify(ctx, id, nil)
}
func (s *Store) VerifyWithOptions(ctx context.Context, id string, options session.Options) error {
	return s.verify(ctx, id, &options)
}
func (s *Store) verify(ctx context.Context, id string, options *session.Options) error {
	d, e := s.Load()
	if e != nil {
		return e
	}
	var p *Pending
	for i := range d.Pending {
		if d.Pending[i].New.ID == id {
			p = &d.Pending[i]
			break
		}
	}
	if p == nil {
		return errors.New("no pending transaction")
	}
	key, e := hex.DecodeString(p.New.PSK)
	if e != nil {
		return e
	}
	var conn *session.Session
	address := net.JoinHostPort(p.New.IP, strconv.Itoa(int(p.New.Port)))
	if options == nil {
		conn, e = session.Dial(ctx, address, key)
	} else {
		conn, e = session.DialWithOptions(ctx, address, key, *options)
	}
	clear(key)
	if e != nil {
		return e
	}
	defer conn.Close()
	runCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	received := make(chan *pb.Config, 1)
	done := make(chan error, 1)
	go func() {
		done <- conn.Run(runCtx, func(m *pb.Msg) {
			if c := m.GetCfgData(); c != nil {
				select {
				case received <- c:
				default:
				}
			}
		})
	}()
	if e = conn.Send(&pb.Msg{Body: &pb.Msg_CfgGet{CfgGet: &pb.Empty{}}}, 0, false); e != nil {
		return e
	}
	var actual *pb.Config
	select {
	case <-runCtx.Done():
		return runCtx.Err()
	case e = <-done:
		select {
		case actual = <-received:
		default:
			return e
		}
	case actual = <-received:
	}
	if p.Expected == nil || !proto.Equal(actual, p.Expected) {
		return errors.New("device config does not match pending candidate")
	}
	return s.Update(func(d *Data) error {
		found := false
		for _, current := range d.Pending {
			if current.New.ID == id && current.Token == p.Token {
				found = true
				break
			}
		}
		if !found {
			return errors.New("pending transaction changed during verification")
		}
		replaced := false
		for i, v := range d.Devices {
			if v.ID == id {
				d.Devices[i] = p.New
				replaced = true
				break
			}
		}
		if !replaced {
			d.Devices = append(d.Devices, p.New)
		}
		for i, v := range d.Pending {
			if v.New.ID == id {
				d.Pending = append(d.Pending[:i], d.Pending[i+1:]...)
				break
			}
		}
		return nil
	})
}

// DiscardPending removes local recovery data; it never changes the device.
func (s *Store) DiscardPending(id string) error { return s.discardPending(id, nil) }
func (s *Store) discardPending(id string, token *string) error {
	if id == "" {
		return errors.New("device id required")
	}
	return s.Update(func(d *Data) error {
		for i, p := range d.Pending {
			if p.New.ID == id && (token == nil || p.Token == *token) {
				d.Pending = append(d.Pending[:i], d.Pending[i+1:]...)
				return nil
			}
		}
		return errors.New("no pending transaction")
	})
}
