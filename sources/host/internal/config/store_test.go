package config

import (
	"errors"
	"github.com/jjundot/EspKVM/internal/protocol/pb"
	"google.golang.org/protobuf/proto"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

type lostReply struct {
	t     *testing.T
	store *Store
	sets  int
}

func (f *lostReply) Request(p *pb.SerialPacket) (*pb.SerialPacket, error) {
	if p.GetInfoGet() != nil {
		return &pb.SerialPacket{Body: &pb.SerialPacket_Info{Info: &pb.SerialInfo{}}}, nil
	}
	f.sets++
	d, e := f.store.Load()
	if e != nil || len(d.Pending) != 1 {
		f.t.Fatal("pending not durable before write", e)
	}
	if d.Pending[0].Expected.Password != nil || len(d.Pending[0].Expected.Psk) != 0 {
		f.t.Fatal("snapshot not redacted")
	}
	return nil, errors.New("response lost")
}
func TestLostProvisionReply(t *testing.T) {
	s := Open(filepath.Join(t.TempDir(), "private", "config.json"))
	fake := &lostReply{t: t, store: s}
	c := &pb.Config{Ssid: "wifi", Password: proto.String("password"), Psk: make([]byte, 16), UsbVid: 1, UsbPid: 1, Ip: 0xc0a8010a, Gw: 0xc0a80101, Mask: 0xffffff00, TcpPort: 24800}
	if e := s.Provision(fake, "dev", c); e == nil {
		t.Fatal("missing failure")
	}
	if e := s.Provision(fake, "dev", c); e == nil {
		t.Fatal("unresolved pending overwritten")
	}
	if fake.sets != 1 {
		t.Fatal("destructive retry", fake.sets)
	}
	d, e := s.Load()
	if e != nil || len(d.Pending) != 1 || len(d.Devices) != 0 {
		t.Fatal(d, e)
	}
	if runtime.GOOS == "linux" {
		info, _ := os.Stat(s.path)
		if info.Mode().Perm() != 0600 {
			t.Fatal(info.Mode())
		}
		info, _ = os.Stat(filepath.Dir(s.path))
		if info.Mode().Perm() != 0700 {
			t.Fatal(info.Mode())
		}
	}
}
func TestFailedUpdatePreservesConfig(t *testing.T) {
	s := Open(filepath.Join(t.TempDir(), "config.json"))
	if e := s.Update(func(d *Data) error { d.Hotkey = []uint16{4, 5}; return nil }); e != nil {
		t.Fatal(e)
	}
	s.Update(func(d *Data) error { d.Hotkey = nil; return errors.New("abort") })
	d, e := s.Load()
	if e != nil || len(d.Hotkey) != 2 {
		t.Fatal(d, e)
	}
}

type rejectedReply struct{ code uint32 }

func (r rejectedReply) Request(p *pb.SerialPacket) (*pb.SerialPacket, error) {
	if p.GetInfoGet() != nil {
		return &pb.SerialPacket{Body: &pb.SerialPacket_Info{Info: &pb.SerialInfo{}}}, nil
	}
	return &pb.SerialPacket{Body: &pb.SerialPacket_CfgResult{CfgResult: &pb.ConfigResult{Code: r.code}}}, nil
}
func TestRejectedProvisionPending(t *testing.T) {
	for _, code := range []uint32{1, 2, 3} {
		t.Run(string(rune('0'+code)), func(t *testing.T) {
			s := Open(filepath.Join(t.TempDir(), "config.json"))
			c := &pb.Config{Ssid: "wifi", Password: proto.String("password"), Psk: make([]byte, 16), UsbVid: 1, UsbPid: 1, Ip: 0xc0a8010a, Gw: 0xc0a80101, Mask: 0xffffff00, TcpPort: 24800}
			if e := s.Provision(rejectedReply{code}, "dev", c); e == nil {
				t.Fatal("missing rejection")
			}
			d, e := s.Load()
			if e != nil {
				t.Fatal(e)
			}
			if (len(d.Pending) == 1) != (code == 2) {
				t.Fatal("unexpected pending", d.Pending)
			}
			if code == 2 {
				if e := s.DiscardPending("dev"); e != nil {
					t.Fatal(e)
				}
				d, e = s.Load()
				if e != nil || len(d.Pending) != 0 {
					t.Fatal(d, e)
				}
			}
		})
	}
}

func TestStoreProcessLock(t *testing.T) {
	if path := os.Getenv("ESPKVM_LOCK_TEST_PATH"); path != "" {
		s := Open(path)
		for i := 0; i < 5; i++ {
			if e := s.Update(func(d *Data) error { d.Hotkey = append(d.Hotkey, 1); return nil }); e != nil {
				t.Fatal(e)
			}
		}
		return
	}
	path := filepath.Join(t.TempDir(), "config.json")
	s := Open(path)
	if e := s.Update(func(d *Data) error { d.Hotkey = nil; return nil }); e != nil {
		t.Fatal(e)
	}
	var workers []*exec.Cmd
	for i := 0; i < 3; i++ {
		c := exec.Command(os.Args[0], "-test.run=^TestStoreProcessLock$")
		c.Env = append(os.Environ(), "ESPKVM_LOCK_TEST_PATH="+path)
		if e := c.Start(); e != nil {
			t.Fatal(e)
		}
		workers = append(workers, c)
	}
	for _, c := range workers {
		if e := c.Wait(); e != nil {
			t.Fatal(e)
		}
	}
	d, e := s.Load()
	if e != nil || len(d.Hotkey) != 15 {
		t.Fatalf("lost concurrent updates: %v %v", d.Hotkey, e)
	}
}
