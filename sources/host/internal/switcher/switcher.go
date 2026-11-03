// Package switcher owns target selection and asynchronous release barriers.
package switcher

import (
	"context"
	"errors"
	"github.com/jjundot/EspKVM/internal/debuglog"
	"github.com/jjundot/EspKVM/internal/input"
	"github.com/jjundot/EspKVM/internal/protocol/pb"
	"sync/atomic"
	"time"
)

const Local = "LOCAL"

type Endpoint interface {
	Send(*pb.Msg, uint64, bool) error
	Release(uint64) <-chan error
	Close()
}

// Capture methods perform local platform operations only, never network I/O.
// Remote returns false while Linux is waiting for a safe all-released grab boundary.
type Capture interface {
	Local()
	Remote() (bool, error)
	ReleaseLocal(*input.State) error
}
type View struct {
	Recording  bool   `json:"recording"`
	Warning    string `json:"warning,omitempty"`
	Target     string `json:"target"`
	Phase      string `json:"phase"`
	Generation uint64 `json:"generation"`
	Error      string `json:"error,omitempty"`
}
type link struct {
	endpoint Endpoint
	ready    bool
}
type command struct {
	id        string
	endpoint  Endpoint
	ready     bool
	order     []string
	hotkey    []uint16
	detach    bool
	recording *bool
}
type completion struct {
	generation uint64
	phase      string
	link       *link
	err        error
}
type Dispatcher struct {
	capture      Capture
	events       chan input.Event
	commands     chan command
	complete     chan completion
	fault        chan struct{}
	done         chan struct{}
	view         atomic.Value
	active       atomic.Pointer[link]
	lastPhysical atomic.Pointer[time.Time]
}

func New(c Capture) *Dispatcher {
	d := &Dispatcher{capture: c, events: make(chan input.Event, 128), commands: make(chan command, 128), complete: make(chan completion, 4), fault: make(chan struct{}, 1), done: make(chan struct{})}
	d.view.Store(View{Target: Local, Phase: "idle"})
	return d
}
func (d *Dispatcher) View() View { return d.view.Load().(View) }
func (d *Dispatcher) Input(e input.Event) bool {
	select {
	case <-d.done:
		return false
	default:
	}
	if e.Kind == input.Fault {
		d.lastPhysical.Store(nil)
	} else if !e.Resync && !e.Synthetic && (e.Kind == input.Key || e.Kind == input.Button ||
		e.Kind == input.Consumer || (e.Kind == input.Motion && (e.DX != 0 || e.DY != 0 || e.Wheel != 0 || e.Pan != 0))) {
		at := e.Captured
		if at.IsZero() {
			at = time.Now()
		}
		d.lastPhysical.Store(&at)
	}
	select {
	case d.events <- e:
		return true
	default:
		d.capture.Local()
		if l := d.active.Load(); l != nil {
			l.endpoint.Close()
		}
		select {
		case d.fault <- struct{}{}:
		default:
		}
		return false
	}
}

// HostActivity includes physical input on LOCAL and every DEVICE, before routing.
func (d *Dispatcher) HostActivity() (uint32, bool) {
	at := d.lastPhysical.Load()
	if at == nil {
		return 0, false
	}
	seconds := time.Since(*at) / time.Second
	if seconds < 0 {
		seconds = 0
	}
	if seconds > time.Duration(^uint32(0)) {
		return ^uint32(0), true
	}
	return uint32(seconds), true
}
func (d *Dispatcher) send(c command) error {
	select {
	case <-d.done:
		return errors.New("dispatcher closed")
	case d.commands <- c:
		return nil
	default:
		return errors.New("dispatcher command queue full")
	}
}
func (d *Dispatcher) Switch(id string) error      { return d.send(command{id: id}) }
func (d *Dispatcher) Recording(active bool) error { return d.send(command{recording: &active}) }
func (d *Dispatcher) Attach(id string, e Endpoint, ready bool) error {
	return d.send(command{id: id, endpoint: e, ready: ready})
}
func (d *Dispatcher) Detach(id string, e Endpoint) error {
	return d.send(command{id: id, endpoint: e, detach: true})
}
func (d *Dispatcher) Order(ids []string) error {
	return d.send(command{order: append([]string{}, ids...)})
}
func (d *Dispatcher) Hotkey(keys []uint16) error {
	if _, ok := input.NewHotkey(keys); !ok {
		return errors.New("hotkey requires 2..8 distinct HID keys")
	}
	return d.send(command{hotkey: append([]uint16{}, keys...)})
}
func (d *Dispatcher) Run(ctx context.Context) {
	defer close(d.done)
	defer d.capture.Local()
	links := map[string]*link{}
	var order []string
	var physical input.State
	var motion input.MotionBuffer
	var activatedAt time.Time
	var recordUntil time.Time
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	hotkey, _ := input.NewHotkey(input.DefaultHotkey())
	suppressed := map[uint32]bool{}
	view := View{Target: Local, Phase: "idle"}
	desired, queued := "", ""
	var pendingLink *link
	publish := func() { d.view.Store(view); d.active.Store(links[view.Target]) }
	suppress := func() {
		for _, k := range physical.HeldTokens() {
			suppressed[k] = true
		}
	}
	barrier := func(l *link, phase string) {
		view.Phase = phase
		pendingLink = l
		generation := view.Generation
		result := l.endpoint.Release(generation)
		go func() {
			select {
			case e := <-result:
				select {
				case d.complete <- completion{generation, phase, l, e}:
				case <-ctx.Done():
				}
			case <-ctx.Done():
			}
		}()
	}
	var request func(string, bool)
	var activate func()
	fallback := func(reason string) {
		motion.Clear()
		view.Generation++
		view.Error = reason
		view.Phase = "idle"
		desired = ""
		queued = ""
		suppress()
		d.capture.Local()
		if view.Target != Local {
			view.Warning = "old target USB release unconfirmed; its watchdog may take up to 3s"
		}
		if l := links[view.Target]; l != nil {
			l.endpoint.Close()
		}
		view.Target = Local
		publish()
	}
	flush := func() bool {
		if view.Target == Local || view.Phase != "idle" || hotkey.Waiting() {
			motion.Clear()
			return true
		}
		l := links[view.Target]
		if l == nil || !l.ready {
			fallback("target unavailable")
			return false
		}
		if e := motion.Drain(time.Now(), func(m *pb.Mouse) error {
			return l.endpoint.Send(&pb.Msg{Body: &pb.Msg_Mouse{Mouse: m}}, view.Generation, true)
		}); e != nil {
			fallback(e.Error())
			return false
		}
		return true
	}
	activate = func() {
		if desired == Local {
			if source, ok := d.capture.(interface{ TryLocal() (bool, error) }); ok {
				ready, e := source.TryLocal()
				if e != nil {
					fallback(e.Error())
					return
				}
				if !ready {
					view.Phase = "waiting-local-release"
					publish()
					return
				}
			} else {
				d.capture.Local()
			}
			debuglog.Printf("switcher", "local target activated")
			view.Target = Local
			view.Phase = "idle"
			publish()
			return
		}
		l := links[desired]
		if l == nil || !l.ready {
			fallback("target unavailable")
			return
		}
		if pendingLink != l || view.Phase == "old-release" {
			barrier(l, "new-release")
			publish()
			return
		}
		ok, e := d.capture.Remote()
		if e != nil {
			fallback(e.Error())
			return
		}
		if !ok {
			debuglog.Printf("switcher", "waiting for physical key/button release before activating target=%s", desired)
			view.Phase = "waiting-release"
			publish()
			return
		}
		view.Target = desired
		activatedAt = time.Now()
		view.Phase = "idle"
		view.Error = ""
		// E.g. Linux devices held exclusively by another program stay with that program.
		if source, ok := d.capture.(interface{ Warning() string }); ok {
			view.Warning = source.Warning()
		}
		debuglog.Printf("switcher", "device target activated id=%s", desired)
		publish()
	}
	// cancel returns a switch in progress to LOCAL without closing any session.
	// An old remote target still finishes its release barrier first; a new
	// target's pending barrier is abandoned and its late result ignored.
	cancel := func() {
		queued = ""
		desired = Local
		if view.Phase == "waiting-local-release" || (view.Phase == "old-release" && view.Target != Local) {
			publish()
			return
		}
		view.Generation++
		pendingLink = nil
		motion.Clear()
		suppress()
		activate()
	}
	request = func(id string, fault bool) {
		debuglog.Printf("switcher", "request target=%s fault=%t current=%s phase=%s", id, fault, view.Target, view.Phase)
		if id == "" {
			id = Local
		}
		if id != Local && view.Recording {
			return
		}
		if fault {
			fallback("active device unavailable")
			return
		}
		if view.Phase != "idle" {
			if id == Local {
				cancel()
			} else {
				queued = id
			}
			return
		}
		if id == view.Target {
			return
		}
		if id != Local {
			if l := links[id]; l == nil || !l.ready {
				return
			}
		}
		view.Generation++
		motion.Clear()
		view.Warning = ""
		suppress()
		desired = id
		pendingLink = nil
		if view.Target == Local {
			if e := d.capture.ReleaseLocal(&physical); e != nil {
				fallback(e.Error())
				return
			}
			view.Phase = "old-release"
			activate()
		} else if l := links[view.Target]; l != nil {
			barrier(l, "old-release")
		} else {
			activate()
		}
		publish()
	}
	record := func(active bool) {
		view.Recording = active
		recordUntil = time.Now().Add(30 * time.Second)
		if source, ok := d.capture.(interface{ SetRecording(bool) }); ok {
			source.SetRecording(active)
		}
		hotkey.Disarm()
		hotkey.Observe(&physical, active)
		if active {
			request(Local, false)
		}
		publish()
	}
	cycle := func(direction int) {
		targets := []string{Local}
		for _, id := range order {
			if l := links[id]; l != nil && l.ready {
				targets = append(targets, id)
			}
		}
		base := view.Target
		if view.Phase != "idle" && desired != "" {
			base = desired
		}
		if queued != "" {
			base = queued
		}
		index := -1
		for i, id := range targets {
			if id == base {
				index = i
				break
			}
		}
		next := 0
		if index >= 0 {
			next = (index + direction + len(targets)) % len(targets)
		} else if direction < 0 {
			next = len(targets) - 1
		}
		debuglog.Printf("switcher", "cycle direction=%d from=%s to=%s available=%v", direction, base, targets[next], targets)
		request(targets[next], false)
	}
	for {
		select {
		case <-ctx.Done():
			if l := links[view.Target]; l != nil {
				l.endpoint.Close()
			}
			return
		case <-d.fault:
			fallback("input queue overflow")
			physical = input.State{}
			suppressed = map[uint32]bool{}
			hotkey.Observe(&physical, false)
		case <-tick.C:
			if view.Phase == "waiting-local-release" {
				activate()
			}
			if view.Recording && time.Now().After(recordUntil) {
				record(false)
			}
			flush()
		case c := <-d.commands:
			switch {
			case c.recording != nil:
				record(*c.recording)
			case c.order != nil:
				order = c.order
			case c.hotkey != nil:
				if source, ok := d.capture.(interface{ SetHotkey([]uint16) error }); ok {
					if e := source.SetHotkey(c.hotkey); e != nil {
						fallback(e.Error())
						continue
					}
				}
				hotkey, _ = input.NewHotkey(c.hotkey)
				hotkey.Disarm()
				hotkey.Observe(&physical, false)
				if view.Recording {
					record(false)
				}
			case c.detach:
				if l := links[c.id]; l != nil && l.endpoint == c.endpoint {
					delete(links, c.id)
					if view.Target == c.id || desired == c.id {
						request(Local, true)
					}
				}
			case c.endpoint != nil:
				old := links[c.id]
				if old != nil && old.endpoint != c.endpoint && (view.Target == c.id || desired == c.id) {
					request(Local, true)
				}
				if old != nil && old.endpoint == c.endpoint {
					old.ready = c.ready
				} else {
					links[c.id] = &link{c.endpoint, c.ready}
				}
				if (view.Target == c.id || desired == c.id) && !c.ready {
					request(Local, true)
				}
			default:
				request(c.id, false)
			}
		case result := <-d.complete:
			if result.generation != view.Generation || result.phase != view.Phase || result.link != pendingLink {
				continue
			}
			if result.err != nil {
				result.link.endpoint.Close()
				if result.phase == "old-release" {
					view.Warning = "old target USB release unconfirmed; its watchdog may take up to 3s"
				}
				if result.phase == "new-release" {
					fallback(result.err.Error())
					continue
				}
			}
			activate()
		case event := <-d.events:
			if event.Kind == input.Boundary {
				if capture, ok := d.capture.(interface{ ConfirmBoundary(uint64) }); ok {
					capture.ConfirmBoundary(event.Epoch)
				}
				if view.Phase == "waiting-release" {
					activate()
				}
				continue
			}
			if event.Kind == input.Fault {
				fallback("input capture interrupted")
				if event.Reset {
					physical = input.State{}
					suppressed = map[uint32]bool{}
					hotkey.Disarm()
					hotkey.Observe(&physical, false)
				}
				continue
			}
			if event.Kind == input.Shortcut {
				debuglog.Printf("switcher", "shortcut event code=%d target=%s", event.Code, view.Target)
				if event.Code == 1 {
					cycle(-1)
				} else {
					cycle(1)
				}
				continue
			}
			if event.Kind != input.Motion {
				flush()
			}
			changed := physical.Apply(event)
			stale := event.Suppressed || event.Resync || (!event.Captured.IsZero() && event.Captured.Before(activatedAt))
			if stale && event.Down {
				suppressed[input.Token(event.Kind, event.Code)] = true
			}
			if stale && event.Kind == input.Motion {
				continue
			}
			if !event.Down && event.Kind != input.Motion && !physical.Held(event.Kind, event.Code) {
				delete(suppressed, input.Token(event.Kind, event.Code))
			}
			// Removal can release the last physical contribution to several suppressed usages.
			if event.Kind == input.Removed {
				for k := range suppressed {
					if !physical.Held(input.Kind(k>>16), uint16(k)) {
						delete(suppressed, k)
					}
				}
			}
			trigger := hotkey.Observe(&physical, event.Resync || view.Recording)
			if view.Phase != "idle" || hotkey.Waiting() {
				suppress()
			}
			if trigger {
				cycle(1)
			}
			if view.Phase == "waiting-release" {
				activate()
			}
			if !changed || view.Phase != "idle" || view.Target == Local || hotkey.Waiting() {
				continue
			}
			l := links[view.Target]
			if l == nil || !l.ready {
				fallback("target unavailable")
				continue
			}
			var msg *pb.Msg
			switch event.Kind {
			case input.Key, input.Removed:
				msg = &pb.Msg{Body: &pb.Msg_Kb{Kb: physical.Keyboard(suppressed)}}
			case input.Button, input.Motion:
				buttons := physical.Buttons()
				for i := uint16(0); i < 5; i++ {
					if suppressed[input.Token(input.Button, i)] {
						buttons &^= 1 << i
					}
				}
				if event.Kind == input.Motion {
					if !motion.Add(event, buttons, time.Now()) {
						fallback("mouse accumulator overflow or stale segment")
					}
					continue
				}
				msg = &pb.Msg{Body: &pb.Msg_Mouse{Mouse: &pb.Mouse{Buttons: buttons, Dx: event.DX, Dy: event.DY, Wheel: event.Wheel, Hwheel: event.Pan}}}
			case input.Consumer:
				usage := physical.Consumer(suppressed)
				msg = &pb.Msg{Body: &pb.Msg_Consumer{Consumer: &pb.Consumer{Usage: usage}}}
			}
			if msg != nil {
				if event.Kind == input.Key {
					debuglog.Printf("switcher", "sending keyboard usage=%d down=%t to target=%s generation=%d", event.Code, event.Down, view.Target, view.Generation)
				}
				if e := l.endpoint.Send(msg, view.Generation, true); e != nil {
					fallback(e.Error())
				}
			}
			if event.Kind == input.Removed && view.Target != Local {
				buttons := physical.Buttons()
				for i := uint16(0); i < 5; i++ {
					if suppressed[input.Token(input.Button, i)] {
						buttons &^= 1 << i
					}
				}
				for _, msg := range []*pb.Msg{{Body: &pb.Msg_Mouse{Mouse: &pb.Mouse{Buttons: buttons}}}, {Body: &pb.Msg_Consumer{Consumer: &pb.Consumer{Usage: physical.Consumer(suppressed)}}}} {
					if e := l.endpoint.Send(msg, view.Generation, true); e != nil {
						fallback(e.Error())
						break
					}
				}
			}
		}
		if view.Phase == "idle" && queued != "" {
			id := queued
			queued = ""
			request(id, false)
		}
	}
}
