package protocol

import (
	"encoding/hex"
	"errors"
	"github.com/jjun/esp-kvm/internal/protocol/pb"
	"unicode/utf8"
)

func ValidateConfig(c *pb.Config, paired bool) (uint32, error) {
	bad := func(f uint32) (uint32, error) { return f, errors.New("invalid configuration field") }
	if c == nil {
		return bad(0)
	}
	if !utf8.ValidString(c.Name) || len(c.Name) > 31 {
		return bad(1)
	}
	if len(c.Ssid) < 1 || len(c.Ssid) > 32 || !utf8.ValidString(c.Ssid) {
		return bad(2)
	}
	if len(c.Password) != 0 && (len(c.Password) < 8 || len(c.Password) > 63) {
		if len(c.Password) != 64 {
			return bad(3)
		}
		if _, e := hex.DecodeString(c.Password); e != nil {
			return bad(3)
		}
	}
	if len(c.Psk) != 16 && (!paired || len(c.Psk) != 0) {
		return bad(4)
	}
	if c.UsbVid == 0 || c.UsbVid > 65535 {
		return bad(5)
	}
	if c.UsbPid == 0 || c.UsbPid > 65535 {
		return bad(6)
	}
	inv := ^c.Mask
	if inv < 3 || inv&(inv+1) != 0 || c.Mask == 0 {
		return bad(9)
	}
	validHost := func(ip uint32) bool {
		host := ip & inv
		return ip>>24 != 0 && ip>>24 != 127 && ip>>24 < 224 && host != 0 && host != inv
	}
	if !validHost(c.Ip) {
		return bad(7)
	}
	if !validHost(c.Gw) || c.Gw&c.Mask != c.Ip&c.Mask || c.Gw == c.Ip {
		return bad(8)
	}
	if c.TcpPort == 0 || c.TcpPort > 65535 {
		return bad(10)
	}
	return 0, nil
}

var Consumers = map[uint32]bool{0: true, 0xe2: true, 0xe9: true, 0xea: true, 0xcd: true, 0xb5: true, 0xb6: true, 0xb7: true}

func ValidateMessage(m *pb.Msg, fromDevice bool) error {
	if m == nil || m.Body == nil {
		return ErrFrame
	}
	if fromDevice {
		switch b := m.Body.(type) {
		case *pb.Msg_Heartbeat:
			if b.Heartbeat != nil {
				return nil
			}
		case *pb.Msg_Status:
			if b.Status != nil && b.Status.KbLeds <= 7 {
				return nil
			}
		case *pb.Msg_CfgData:
			if b.CfgData != nil && len(b.CfgData.Psk) == 0 && b.CfgData.Password == "" {
				_, err := ValidateConfig(b.CfgData, true)
				return err
			}
		case *pb.Msg_CfgResult:
			if b.CfgResult != nil && b.CfgResult.Code <= 3 && b.CfgResult.Field <= 10 {
				return nil
			}
		}
		return ErrFrame
	}
	switch b := m.Body.(type) {
	case *pb.Msg_Kb:
		k := b.Kb
		if k != nil && k.Mods <= 255 && k.KeysLo&15 == 0 && k.Keys_192_255&(uint64(255)<<32) == 0 {
			return nil
		}
	case *pb.Msg_Mouse:
		v := b.Mouse
		if v != nil && v.Buttons <= 31 && axis(v.Dx) && axis(v.Dy) && axis(v.Wheel) && axis(v.Hwheel) {
			return nil
		}
	case *pb.Msg_Consumer:
		if b.Consumer != nil && Consumers[b.Consumer.Usage] {
			return nil
		}
	case *pb.Msg_ReleaseAll:
		if b.ReleaseAll != nil {
			return nil
		}
	case *pb.Msg_Heartbeat:
		if b.Heartbeat != nil {
			return nil
		}
	case *pb.Msg_CfgGet:
		if b.CfgGet != nil {
			return nil
		}
	case *pb.Msg_CfgSet:
		_, e := ValidateConfig(b.CfgSet, true)
		return e
	case *pb.Msg_Reboot:
		if b.Reboot != nil {
			return nil
		}
	}
	return ErrFrame
}
func axis(x int32) bool { return x >= -1000000 && x <= 1000000 }
