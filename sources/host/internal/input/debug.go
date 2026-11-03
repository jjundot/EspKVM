package input

import (
	"log"
	"os"
)

var debugMouseButtons = os.Getenv("ESPKVM_DEBUG_MOUSE_BUTTONS") == "1"

// DebugMousef writes opt-in mouse button diagnostics without logging normal
// operation or any device credentials/input contents.
func DebugMousef(format string, args ...any) {
	if debugMouseButtons {
		log.Printf("[mouse-debug] "+format, args...)
	}
}
