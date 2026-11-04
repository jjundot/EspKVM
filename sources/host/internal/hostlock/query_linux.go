package hostlock

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

const pollInterval = 2 * time.Second

// The system service owns no login session, so follow whichever session is on seat0.
func query(ctx context.Context) uint32 {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	seat, e := exec.CommandContext(ctx, "loginctl", "show-seat", "seat0", "-p", "ActiveSession", "--value").Output()
	if e != nil {
		return Unknown
	}
	session := strings.TrimSpace(string(seat))
	if session == "" {
		return Locked // Nobody is logged in at the seat.
	}
	properties, e := exec.CommandContext(ctx, "loginctl", "show-session", session, "-p", "Class", "-p", "LockedHint").Output()
	if e != nil {
		return Unknown
	}
	return classifyLinux(string(properties))
}
