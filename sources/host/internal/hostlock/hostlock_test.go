package hostlock

import "testing"

func TestClassifyWindows(t *testing.T) {
	const disconnected = 4
	for _, c := range []struct {
		name                  string
		level, session, state uint32
		flags                 int32
		want                  uint32
	}{
		{"unlocked", 1, 2, 0, 1, Unlocked},
		{"locked", 1, 2, 0, 0, Locked},
		{"unknown flags", 1, 2, 0, -1, Unknown},
		{"disconnected", 1, 2, disconnected, 1, Locked},
		{"session zero", 1, 0, 0, 1, Unknown},
		{"unexpected level", 2, 2, 0, 1, Unknown},
	} {
		if got := classifyWindows(c.level, c.session, c.state, c.flags); got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
}
func TestClassifyLinux(t *testing.T) {
	for _, c := range []struct {
		properties string
		want       uint32
	}{
		{"Class=user\nLockedHint=no\n", Unlocked},
		{"Class=user\nLockedHint=yes\n", Locked},
		{"Class=user-early\nLockedHint=no\n", Unlocked},
		{"Class=greeter\nLockedHint=no\n", Locked},
		{"Class=lock-screen\nLockedHint=no\n", Locked},
		{"Class=user\n", Unknown},
		{"Class=manager\nLockedHint=no\n", Unknown},
		{"", Unknown},
	} {
		if got := classifyLinux(c.properties); got != c.want {
			t.Errorf("%q: got %d, want %d", c.properties, got, c.want)
		}
	}
}
