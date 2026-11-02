package capture

import (
	"context"
	"errors"
	"github.com/jjundot/EspKVM/internal/input"
)

var ErrHotkey = errors.New("hotkey requires 2..8 distinct HID keys")

type Source interface {
	Run(context.Context, func(input.Event) bool) error
	Local()
	Remote() (bool, error)
	ReleaseLocal(*input.State) error
	SetHotkey([]uint16) error
}
