// Package debuglog provides opt-in diagnostic logging for the host process.
package debuglog

import (
	"fmt"
	"log"
	"sync"
	"sync/atomic"
)

var enabled atomic.Bool
var asyncOnce sync.Once
var asyncMessages chan string

func SetEnabled(value bool) { enabled.Store(value) }

func Printf(component, format string, args ...any) {
	if enabled.Load() {
		log.Printf("[debug:%s] "+format, append([]any{component}, args...)...)
	}
}

// PrintfAsync is for low-level input callbacks, where blocking on console I/O
// can cause Windows to silently remove the hook after its timeout.
func PrintfAsync(component, format string, args ...any) {
	if !enabled.Load() {
		return
	}
	asyncOnce.Do(func() {
		asyncMessages = make(chan string, 2048)
		go func() {
			for message := range asyncMessages {
				log.Print(message)
			}
		}()
	})
	message := fmt.Sprintf("[debug:%s] %s", component, fmt.Sprintf(format, args...))
	select {
	case asyncMessages <- message:
	default:
		// Diagnostics must never stall or change input capture behavior.
	}
}
