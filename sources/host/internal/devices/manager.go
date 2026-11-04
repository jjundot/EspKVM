// Package devices owns independent session lifetimes; reconnect never selects a target.
package devices

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"github.com/jjundot/EspKVM/internal/config"
	"github.com/jjundot/EspKVM/internal/debuglog"
	"github.com/jjundot/EspKVM/internal/hostlock"
	"github.com/jjundot/EspKVM/internal/input"
	"github.com/jjundot/EspKVM/internal/protocol/pb"
	"github.com/jjundot/EspKVM/internal/session"
	"github.com/jjundot/EspKVM/internal/switcher"
	"google.golang.org/protobuf/proto"
	"net"
	"slices"
	"strconv"
	"sync"
	"time"
)

type View struct {
	Firmware            uint32      `json:"firmware"`
	FirmwareVersion     string      `json:"firmware_version"`
	KeepAwakeState      uint32      `json:"keep_awake_state"`
	KeepAwakeMoves      uint32      `json:"keep_awake_moves"`
	KeepAwakeLastMoveAt time.Time   `json:"keep_awake_last_move_at,omitempty"`
	MAC                 string      `json:"mac"`
	Uptime              uint32      `json:"uptime_s"`
	ID                  string      `json:"id"`
	Name                string      `json:"name"`
	IP                  string      `json:"ip"`
	Port                uint16      `json:"port"`
	Enabled             bool        `json:"enabled"`
	Connected           bool        `json:"connected"`
	USB                 bool        `json:"usb"`
	USBSleeping         bool        `json:"usb_sleeping"`
	RSSI                int32       `json:"rssi"`
	LEDs                uint32      `json:"leds"`
	LastValid           time.Time   `json:"last_valid,omitempty"`
	RTT                 session.RTT `json:"rtt"`
	Error               string      `json:"error,omitempty"`
}
type reply struct {
	session *session.Session
	message *pb.Msg
}
type record struct {
	paused  bool
	device  config.Device
	view    View
	cancel  context.CancelFunc
	current *session.Session
	wake    chan struct{}
	replies chan reply
	op      sync.Mutex
}
type Manager struct {
	ctx        context.Context
	cancel     context.CancelFunc
	store      *config.Store
	dispatcher *switcher.Dispatcher
	hostLock   *hostlock.Monitor
	owner      [16]byte
	mu         sync.Mutex
	records    map[string]*record
	order      []string
	sentOrder  []string
	hotkey     []uint16
	workers    sync.WaitGroup
}

func New(ctx context.Context, store *config.Store, d *switcher.Dispatcher) (*Manager, error) {
	ctx, cancel := context.WithCancel(ctx)
	m := &Manager{ctx: ctx, cancel: cancel, store: store, dispatcher: d, hostLock: hostlock.Start(ctx), records: map[string]*record{}}
	if _, e := rand.Read(m.owner[:]); e != nil {
		cancel()
		return nil, e
	}
	if e := m.Reload(); e != nil {
		m.Close()
		return nil, e
	}
	return m, nil
}
func (m *Manager) Close() {
	m.cancel()
	m.mu.Lock()
	for _, r := range m.records {
		r.cancel()
		if r.current != nil {
			r.current.Close()
		}
	}
	m.mu.Unlock()
	m.workers.Wait()
}
func (m *Manager) Reload() error {
	data, e := m.store.Load()
	if e != nil {
		return e
	}
	if _, ok := input.NewHotkey(data.Hotkey); !ok {
		return errors.New("invalid configured hotkey")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ctx.Err() != nil {
		return m.ctx.Err()
	}
	keep := map[string]bool{}
	m.order = nil
	for _, device := range data.Devices {
		keep[device.ID] = true
		m.order = append(m.order, device.ID)
		if old := m.records[device.ID]; old != nil && old.device == device && !old.paused {
			continue
		}
		if old := m.records[device.ID]; old != nil {
			old.cancel()
			if old.current != nil {
				old.current.Close()
			}
		}
		ctx, cancel := context.WithCancel(m.ctx)
		r := &record{device: device, cancel: cancel, wake: make(chan struct{}, 1), replies: make(chan reply, 1), view: View{ID: device.ID, Name: device.Name, IP: device.IP, Port: device.Port, Enabled: device.Enabled}}
		m.records[device.ID] = r
		if device.Enabled {
			m.workers.Add(1)
			go func() { defer m.workers.Done(); m.lifecycle(ctx, r) }()
		}
	}
	for id, r := range m.records {
		if !keep[id] {
			r.cancel()
			if r.current != nil {
				r.current.Close()
			}
			delete(m.records, id)
		}
	}
	if !slices.Equal(m.sentOrder, m.order) {
		if e = m.dispatcher.Order(m.order); e != nil {
			return e
		}
		m.sentOrder = append([]string(nil), m.order...)
	}
	if !slices.Equal(m.hotkey, data.Hotkey) {
		if e = m.dispatcher.Hotkey(data.Hotkey); e != nil {
			return e
		}
		m.hotkey = append([]uint16(nil), data.Hotkey...)
	}
	return nil
}
func (m *Manager) Watch() {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-tick.C:
			if e := m.Reload(); e != nil {
				m.dispatcher.Switch(switcher.Local)
			}
		}
	}
}
func (m *Manager) Views() []View {
	m.mu.Lock()
	defer m.mu.Unlock()
	views := make([]View, 0, len(m.order))
	for _, id := range m.order {
		r := m.records[id]
		v := r.view
		if r.current != nil {
			v.RTT = r.current.RTT()
		}
		views = append(views, v)
	}
	return views
}
func (m *Manager) Takeover(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.records[id]
	if r == nil || !r.device.Enabled {
		return errors.New("enabled device required")
	}
	if r.current != nil {
		return nil
	}
	select {
	case r.wake <- struct{}{}:
	default:
	}
	return nil
}
func (m *Manager) lifecycle(ctx context.Context, r *record) {
	key, e := hex.DecodeString(r.device.PSK)
	if e != nil {
		return
	}
	defer clear(key)
	attempt := 0
	takeover := false
	for ctx.Err() == nil {
		select {
		case <-r.wake:
			takeover = true
		default:
		}
		address := net.JoinHostPort(r.device.IP, strconv.Itoa(int(r.device.Port)))
		debuglog.Printf("device", "connecting id=%s address=%s takeover=%t attempt=%d", r.device.ID, address, takeover, attempt+1)
		conn, e := session.DialWithOptions(ctx, address, key, session.Options{Owner: m.owner, Takeover: takeover})
		takeover = false
		if e == nil {
			m.mu.Lock()
			if ctx.Err() != nil || m.records[r.device.ID] != r {
				m.mu.Unlock()
				conn.Close()
				return
			}
			r.current = conn
			conn.HostActivity = m.dispatcher.HostActivity
			conn.HostLock = m.hostLock.State
			// An explicit request queued during this successful handshake is
			// already satisfied. Do not retain it as permission to seize a later owner.
			select {
			case <-r.wake:
			default:
			}
			r.view.Connected = true
			r.view.Error = ""
			r.view.Firmware = conn.Challenge.FwVer
			r.view.FirmwareVersion = conn.Challenge.FwVersion
			r.view.MAC = net.HardwareAddr(conn.Challenge.Mac).String()
			debuglog.Printf("device", "connected id=%s firmware=%s", r.device.ID, conn.Challenge.FwVersion)
			m.mu.Unlock()
			attempt = 0
			e = conn.Run(ctx, func(message *pb.Msg) {
				m.mu.Lock()
				defer m.mu.Unlock()
				if r.current != conn || m.records[r.device.ID] != r {
					return
				}
				r.view.LastValid = time.Now()
				if s := message.GetStatus(); s != nil {
					restarted := r.view.Uptime > 0 && s.UptimeS < r.view.Uptime
					if restarted {
						r.view.KeepAwakeLastMoveAt = time.Time{}
					}
					if s.KeepAwakeMoves > r.view.KeepAwakeMoves || restarted && s.KeepAwakeMoves > 0 {
						r.view.KeepAwakeLastMoveAt = time.Now()
					}
					r.view.KeepAwakeState = s.KeepAwakeState
					r.view.KeepAwakeMoves = s.KeepAwakeMoves
					r.view.Firmware = s.FwVer
					if s.FwVersion != "" {
						r.view.FirmwareVersion = s.FwVersion
					}
					r.view.Uptime = s.UptimeS
					r.view.USB = s.UsbMounted
					r.view.USBSleeping = s.UsbSuspended
					r.view.RSSI = s.Rssi
					r.view.LEDs = s.KbLeds
					if err := m.dispatcher.Attach(r.device.ID, conn, s.UsbMounted); err != nil {
						debuglog.Printf("device", "attach failed id=%s usb=%t error=%v", r.device.ID, s.UsbMounted, err)
						conn.Close()
					}
				}
				if message.GetCfgData() != nil || message.GetCfgResult() != nil {
					select {
					case r.replies <- reply{conn, proto.Clone(message).(*pb.Msg)}:
					default:
						conn.Close()
					}
				}
			})
			m.mu.Lock()
			if r.current == conn {
				r.current = nil
				r.view.Connected = false
				r.view.USB = false
				r.view.USBSleeping = false
			}
			m.mu.Unlock()
			m.dispatcher.Detach(r.device.ID, conn)
			debuglog.Printf("device", "disconnected id=%s error=%v", r.device.ID, e)
		}
		if ctx.Err() != nil {
			return
		}
		m.mu.Lock()
		if e != nil {
			r.view.Error = e.Error()
		}
		m.mu.Unlock()
		if errors.Is(e, session.ErrReplaced) {
			select {
			case <-ctx.Done():
				return
			case <-r.wake:
				takeover = true
			}
			continue
		}
		delays := []time.Duration{time.Second, 2 * time.Second, 5 * time.Second}
		index := attempt
		if index > 2 {
			index = 2
		}
		attempt++
		var jitter [1]byte
		if _, err := rand.Read(jitter[:]); err != nil {
			return
		}
		timer := time.NewTimer(delays[index] + time.Duration(jitter[0])*time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-r.wake:
			timer.Stop()
			takeover = true
		case <-timer.C:
		}
	}
}
func (m *Manager) request(ctx context.Context, id string, message *pb.Msg) (*pb.Msg, error) {
	m.mu.Lock()
	r := m.records[id]
	m.mu.Unlock()
	if r == nil {
		return nil, errors.New("unknown device")
	}
	r.op.Lock()
	defer r.op.Unlock()
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	m.mu.Lock()
	conn := r.current
	m.mu.Unlock()
	if conn == nil {
		return nil, errors.New("device offline")
	}
	// One operation per session; discard only already-completed older replies.
	for {
		select {
		case <-r.replies:
			continue
		default:
		}
		break
	}
	if e := conn.Send(message, 0, false); e != nil {
		return nil, e
	}
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			conn.Close()
			return nil, ctx.Err()
		case <-timer.C:
			conn.Close()
			return nil, errors.New("configuration result unknown; inspect pending before retrying")
		case <-conn.Done():
			// The device restarts right after CFG_RESULT; prefer a reply that arrived first.
			select {
			case reply := <-r.replies:
				if reply.session == conn && message.GetCfgSet() != nil && reply.message.GetCfgResult() != nil {
					return reply.message, nil
				}
			default:
			}
			return nil, session.ErrClosed
		case reply := <-r.replies:
			if reply.session != conn {
				continue
			}
			if (message.GetCfgGet() != nil && reply.message.GetCfgData() != nil) || (message.GetCfgSet() != nil && reply.message.GetCfgResult() != nil) {
				return reply.message, nil
			}
			conn.Close()
			return nil, errors.New("unexpected configuration reply")
		}
	}
}
func (m *Manager) GetConfig(ctx context.Context, id string) (*pb.Config, error) {
	reply, e := m.request(ctx, id, &pb.Msg{Body: &pb.Msg_CfgGet{CfgGet: &pb.Empty{}}})
	if e != nil {
		return nil, e
	}
	return reply.GetCfgData(), nil
}

type requester struct {
	manager *Manager
	ctx     context.Context
	id      string
}

func (r requester) Request(p *pb.SerialPacket) (*pb.SerialPacket, error) {
	if p.GetInfoGet() != nil {
		c, err := r.manager.GetConfig(r.ctx, r.id)
		if err != nil {
			return nil, err
		}
		return &pb.SerialPacket{Body: &pb.SerialPacket_Info{Info: &pb.SerialInfo{Paired: true, Config: c}}}, nil
	}
	if p.GetCfgSet() == nil {
		return nil, errors.New("unsupported TCP provisioning operation")
	}
	reply, e := r.manager.request(r.ctx, r.id, &pb.Msg{Body: &pb.Msg_CfgSet{CfgSet: p.GetCfgSet()}})
	if e != nil {
		return nil, e
	}
	return &pb.SerialPacket{Body: &pb.SerialPacket_CfgResult{CfgResult: reply.GetCfgResult()}}, nil
}
func (m *Manager) Provision(ctx context.Context, id string, c *pb.Config) error {
	return m.store.Provision(requester{m, ctx, id}, id, c)
}

// Pause the owned reconnect loop so verification cannot race it for the single
// device session. The same owner is used, without permission to replace others.
func (m *Manager) Verify(ctx context.Context, id string) (err error) {
	m.mu.Lock()
	if r := m.records[id]; r != nil {
		r.paused = true
		r.cancel()
		if r.current != nil {
			r.current.Close()
		}
	}
	m.mu.Unlock()
	defer func() { err = errors.Join(err, m.Reload()) }()
	return m.store.VerifyWithOptions(ctx, id, session.Options{Owner: m.owner})
}
