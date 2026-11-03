package protocol

import (
	"github.com/jjundot/EspKVM/internal/protocol/pb"
	"google.golang.org/protobuf/proto"
	"testing"
)

func validConfig() *pb.Config {
	return &pb.Config{Name: "设备", Ssid: "network", Password: proto.String(""), Psk: make([]byte, 16), UsbVid: 1, UsbPid: 1, Ip: 0xc0a8010a, Gw: 0xc0a80101, Mask: 0xffffff00, TcpPort: 24800}
}
func TestConfigurationValidation(t *testing.T) {
	base := validConfig()
	if _, e := ValidateConfig(base, false); e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		field  uint32
		change func(*pb.Config)
	}{
		{1, func(c *pb.Config) { c.Name = string(make([]byte, 32)) }}, {2, func(c *pb.Config) { c.Ssid = "" }}, {3, func(c *pb.Config) { c.Password = proto.String("short") }}, {4, func(c *pb.Config) { c.Psk = nil }}, {7, func(c *pb.Config) { c.Ip = 0xc0a801ff }}, {8, func(c *pb.Config) { c.Gw = 0xc0a80201 }}, {9, func(c *pb.Config) { c.Mask = 0xff00ff00 }}, {10, func(c *pb.Config) { c.TcpPort = 65536 }},
	} {
		c := proto.Clone(base).(*pb.Config)
		tc.change(c)
		f, e := ValidateConfig(c, false)
		if e == nil || f != tc.field {
			t.Fatalf("field %d: %d %v", tc.field, f, e)
		}
	}
}
func TestZeroReleasePresence(t *testing.T) {
	for _, m := range []*pb.Msg{{Body: &pb.Msg_Kb{Kb: &pb.KbState{}}}, {Body: &pb.Msg_ReleaseAll{ReleaseAll: &pb.ReleaseAll{Id: 1}}}} {
		b, e := proto.Marshal(m)
		if e != nil || len(b) == 0 {
			t.Fatal("missing empty state", e)
		}
		v := new(pb.Msg)
		if proto.Unmarshal(b, v) != nil || ValidateMessage(v, false) != nil {
			t.Fatal("empty release rejected")
		}
	}
}
func TestKeepAwakeValidation(t *testing.T) {
	c := validConfig()
	c.KeepAwake = &pb.KeepAwake{Enabled: true, IntervalMinS: 30, IntervalMaxS: 60}
	if _, err := ValidateConfig(c, false); err != nil {
		t.Fatal("C=0/D=0 should be valid", err)
	}
	for _, p := range []*pb.KeepAwake{{IntervalMinS: 0, IntervalMaxS: 60}, {IntervalMinS: 61, IntervalMaxS: 60}, {IntervalMinS: 1, IntervalMaxS: 86401}} {
		c.KeepAwake = p
		if field, err := ValidateConfig(c, false); err == nil || field != 12 {
			t.Fatalf("invalid policy accepted: %+v", p)
		}
	}
}
func TestInputBounds(t *testing.T) {
	for _, m := range []*pb.Msg{{Body: &pb.Msg_Kb{Kb: &pb.KbState{KeysLo: 1}}}, {Body: &pb.Msg_Kb{Kb: &pb.KbState{Keys_192_255: 1 << 32}}}, {Body: &pb.Msg_Mouse{Mouse: &pb.Mouse{Dx: 1000001}}}, {Body: &pb.Msg_Consumer{Consumer: &pb.Consumer{Usage: 65535}}}} {
		if ValidateMessage(m, false) == nil {
			t.Fatal("invalid input accepted")
		}
	}
}
