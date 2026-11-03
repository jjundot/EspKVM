package switcher

import (
	"github.com/jjundot/EspKVM/internal/input"
	"testing"
	"time"
)

func TestHostActivityAcrossTargets(t *testing.T) {
	d := New(nil)
	if _, known := d.HostActivity(); known {
		t.Fatal("startup must have unknown activity")
	}
	d.Input(input.Event{Kind: input.Key, Down: true, Resync: true})
	d.Input(input.Event{Kind: input.Motion, DX: 1, Synthetic: true})
	if _, known := d.HostActivity(); known {
		t.Fatal("synthetic and resync events must not count")
	}
	d.Input(input.Event{Kind: input.Motion, DX: 1, Captured: time.Now().Add(-5 * time.Second)})
	if idle, known := d.HostActivity(); !known || idle != 5 {
		t.Fatalf("LOCAL movement not recorded: idle=%d known=%v", idle, known)
	}
	d.view.Store(View{Target: "another-device"})
	d.Input(input.Event{Kind: input.Key, Down: true, Suppressed: true, Captured: time.Now()})
	if idle, known := d.HostActivity(); !known || idle != 0 {
		t.Fatal("target/suppression must not filter physical activity")
	}
	d.Input(input.Event{Kind: input.Fault})
	if _, known := d.HostActivity(); known {
		t.Fatal("failed capture must not report recent input")
	}
}
