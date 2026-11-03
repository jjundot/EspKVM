// Package debuglog provides opt-in diagnostic logging for the host process.
package debuglog

import (
	"log"
	"sync/atomic"
)

var enabled atomic.Bool

func SetEnabled(value bool) { enabled.Store(value) }

func Printf(component, format string, args ...any) {
	if enabled.Load() {
		log.Printf("[debug:%s] "+format, append([]any{component}, args...)...)
	}
}
