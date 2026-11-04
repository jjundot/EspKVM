// Package hostlock reports whether the HOST's interactive session is locked.
package hostlock

import (
	"context"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jjundot/EspKVM/internal/debuglog"
)

// Values of Heartbeat.host_lock_state.
const (
	Unknown uint32 = iota
	Unlocked
	Locked
)

// Monitor caches the state so heartbeats never wait on a system query.
type Monitor struct{ state atomic.Uint32 }

func Start(ctx context.Context) *Monitor {
	m := &Monitor{}
	go func() {
		ticker := time.NewTicker(pollInterval)
		defer ticker.Stop()
		for {
			next := query(ctx)
			if previous := m.state.Swap(next); previous != next {
				debuglog.Printf("hostlock", "state changed from %d to %d", previous, next)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return m
}
func (m *Monitor) State() uint32 { return m.state.Load() }

// classifyWindows reads WTSINFOEX_LEVEL1. Session 0 has no interactive desktop,
// and a session that is not active (user switch, RDP disconnect) has nobody at it.
func classifyWindows(level, session, connectState uint32, flags int32) uint32 {
	const wtsActive, wtsSessionLock, wtsSessionUnlock = 0, 0, 1
	if level != 1 || session == 0 {
		return Unknown
	}
	if connectState != wtsActive {
		return Locked
	}
	switch flags {
	case wtsSessionLock:
		return Locked
	case wtsSessionUnlock:
		return Unlocked
	}
	return Unknown
}

// classifyLinux reads the Key=Value lines of loginctl show-session.
func classifyLinux(properties string) uint32 {
	values := map[string]string{}
	for _, line := range strings.Split(properties, "\n") {
		if key, value, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			values[key] = value
		}
	}
	class := values["Class"]
	if class == "greeter" || class == "lock-screen" {
		return Locked
	}
	if !strings.HasPrefix(class, "user") {
		return Unknown
	}
	switch values["LockedHint"] {
	case "yes":
		return Locked
	case "no":
		return Unlocked
	}
	return Unknown
}
