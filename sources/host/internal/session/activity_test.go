package session

import "testing"

func TestHeartbeatCarriesHostActivity(t *testing.T) {
	s := &Session{}
	if s.heartbeat(1, false).HostActivityKnown {
		t.Fatal("missing capture must be unknown")
	}
	if s.heartbeat(1, false).HostLockState != 0 {
		t.Fatal("missing lock detection must be unknown")
	}
	s.HostActivity = func() (uint32, bool) { return 123, true }
	s.HostLock = func() uint32 { return 2 }
	for _, reply := range []bool{false, true} {
		h := s.heartbeat(7, reply)
		if h.Id != 7 || h.Reply != reply || h.HostIdleS != 123 || !h.HostActivityKnown || h.HostLockState != 2 {
			t.Fatalf("heartbeat omitted physical activity: %+v", h)
		}
	}
}
