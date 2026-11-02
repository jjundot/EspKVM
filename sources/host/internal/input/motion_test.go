package input

import (
	"errors"
	"github.com/jjundot/EspKVM/internal/protocol/pb"
	"testing"
	"time"
)

func TestMotionSumsAndButtonBoundary(t *testing.T) {
	var m MotionBuffer
	now := time.Unix(0, 0)
	if !m.Add(Event{DX: 1500000, DY: -2000000, Wheel: 3}, 1, now) || !m.Add(Event{DX: 1500000, DY: -1, Wheel: -1}, 1, now) {
		t.Fatal("valid movement rejected")
	}
	if m.Add(Event{DX: 1}, 0, now) {
		t.Fatal("merged across button boundary")
	}
	var x, y, w int64
	count := 0
	if e := m.Drain(now, func(v *pb.Mouse) error {
		if v.Buttons != 1 || v.Dx > 1000000 || v.Dy < -1000000 {
			t.Fatal(v)
		}
		x += int64(v.Dx)
		y += int64(v.Dy)
		w += int64(v.Wheel)
		count++
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	if x != 3000000 || y != -2000001 || w != 2 || count != 3 {
		t.Fatal(x, y, w, count)
	}
}
func TestMotionFailureRetainsRemainder(t *testing.T) {
	var m MotionBuffer
	now := time.Unix(0, 0)
	m.Add(Event{DX: 30}, 0, now)
	e := m.Drain(now, func(*pb.Mouse) error { return errors.New("write failed") })
	if e == nil || m.axes[0] != 30 {
		t.Fatal("failed movement consumed")
	}
	if e = m.Drain(now.Add(100*time.Millisecond), func(*pb.Mouse) error { t.Fatal("stale input sent"); return nil }); e == nil {
		t.Fatal("stale movement accepted")
	}
}
