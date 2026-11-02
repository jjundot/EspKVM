package input

import (
	"github.com/jjundot/EspKVM/internal/protocol/pb"
	"math"
	"time"
)

type MotionBuffer struct {
	axes    [4]int64
	buttons uint32
	at      time.Time
	pending bool
}

func (m *MotionBuffer) Clear() { *m = MotionBuffer{} }
func (m *MotionBuffer) Add(e Event, buttons uint32, now time.Time) bool {
	if m.pending && (m.buttons != buttons || now.Sub(m.at) >= 100*time.Millisecond) {
		return false
	}
	values := [4]int64{int64(e.DX), int64(e.DY), int64(e.Wheel), int64(e.Pan)}
	for i, v := range values {
		if (v > 0 && m.axes[i] > math.MaxInt64-v) || (v < 0 && m.axes[i] < math.MinInt64-v) {
			return false
		}
	}
	if !m.pending {
		m.buttons = buttons
		m.at = now
		m.pending = true
	}
	for i, v := range values {
		m.axes[i] += v
	}
	return true
}
func (m *MotionBuffer) Drain(now time.Time, send func(*pb.Mouse) error) error {
	for m.pending {
		if now.Sub(m.at) >= 100*time.Millisecond {
			return &MotionExpired{}
		}
		var values [4]int32
		for i, v := range m.axes {
			if v > 1000000 {
				v = 1000000
			}
			if v < -1000000 {
				v = -1000000
			}
			values[i] = int32(v)
		}
		if e := send(&pb.Mouse{Buttons: m.buttons, Dx: values[0], Dy: values[1], Wheel: values[2], Hwheel: values[3]}); e != nil {
			return e
		}
		remaining := false
		for i, v := range values {
			m.axes[i] -= int64(v)
			remaining = remaining || m.axes[i] != 0
		}
		m.pending = remaining
	}
	return nil
}

type MotionExpired struct{}

func (*MotionExpired) Error() string { return "mouse movement exceeded 100ms queue age" }
