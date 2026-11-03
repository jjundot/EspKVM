// Package control connects the authenticated API to persistence and device lifetimes.
package control

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/jjundot/EspKVM/internal/capture"
	"github.com/jjundot/EspKVM/internal/config"
	"github.com/jjundot/EspKVM/internal/debuglog"
	"github.com/jjundot/EspKVM/internal/devices"
	"github.com/jjundot/EspKVM/internal/input"
	"github.com/jjundot/EspKVM/internal/protocol/pb"
	"github.com/jjundot/EspKVM/internal/serialcfg"
	"github.com/jjundot/EspKVM/internal/switcher"
	"github.com/jjundot/EspKVM/internal/web"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"net"
	"strings"
	"sync/atomic"
	"unicode/utf8"
)

type App struct {
	Store       *config.Store
	Devices     *devices.Manager
	Dispatcher  *switcher.Dispatcher
	Capture     capture.Source
	Version     string
	inputStatus atomic.Value
}

// MouseGuard is the Linux capture option that keeps Ctrl+side button off the
// local desktop by passing mice through uinput.
type MouseGuard interface {
	SetMouseGuard(bool)
	MouseGuardWarning() string
}

func (a *App) InputStatus(value string) { a.inputStatus.Store(value) }
func (a *App) State() web.State {
	state := web.State{Devices: a.Devices.Views(), Switch: a.Dispatcher.View(), Version: a.Version}
	if v := a.inputStatus.Load(); v != nil {
		state.Input = v.(string)
	}
	data, e := a.Store.Load()
	if e != nil {
		state.Error = "cannot load local configuration"
		return state
	}
	state.Hotkey = data.Hotkey
	if guard, ok := a.Capture.(MouseGuard); ok {
		state.MouseGuard = &web.MouseGuardView{Enabled: data.MouseGuard, Warning: guard.MouseGuardWarning()}
	}
	for _, p := range data.Pending {
		state.Pending = append(state.Pending, web.PendingView{ID: p.New.ID, Name: p.New.Name, IP: p.New.IP, Port: p.New.Port, State: p.State})
	}
	return state
}
func protobuf(message proto.Message) (json.RawMessage, error) {
	b, e := protojson.MarshalOptions{UseProtoNames: true, EmitDefaultValues: true}.Marshal(message)
	return json.RawMessage(b), e
}
func (a *App) Operation(ctx context.Context, name string, body json.RawMessage) (result any, err error) {
	debuglog.Printf("control", "operation=%s started", name)
	defer func() {
		if err != nil {
			debuglog.Printf("control", "operation=%s failed: %v", name, err)
		} else {
			debuglog.Printf("control", "operation=%s completed", name)
		}
	}()
	switch name {
	case "recording":
		var req struct {
			Active bool `json:"active"`
		}
		if e := web.Decode(body, &req); e != nil {
			return nil, e
		}
		return nil, a.Dispatcher.Recording(req.Active)
	case "device-save":
		var device config.Device
		if e := web.Decode(body, &device); e != nil {
			return nil, e
		}
		if device.ID == "" || len(device.ID) > 64 || !utf8.ValidString(device.ID) || strings.ContainsAny(device.ID, "\r\n\t\x00") || len(device.Name) > 31 || !utf8.ValidString(device.Name) {
			return nil, errors.New("invalid device identity")
		}
		ip := net.ParseIP(device.IP)
		if ip == nil || ip.To4() == nil || ip.IsUnspecified() || ip.IsMulticast() || device.Port == 0 {
			return nil, errors.New("explicit IPv4 address and nonzero port required")
		}
		e := a.Store.Update(func(data *config.Data) error {
			index := -1
			for i, d := range data.Devices {
				if d.ID == device.ID {
					index = i
					if device.PSK == "" {
						device.PSK = d.PSK
					}
					break
				}
			}
			key, e := hex.DecodeString(device.PSK)
			if e != nil || len(key) != 16 {
				return errors.New("PSK must be exactly 16 bytes in hexadecimal")
			}
			clear(key)
			for _, p := range data.Pending {
				if p.New.ID == device.ID && (index < 0 || data.Devices[index].IP != device.IP || data.Devices[index].Port != device.Port || data.Devices[index].PSK != device.PSK) {
					return errors.New("resolve pending transaction before changing connection credentials")
				}
			}
			if index < 0 {
				if len(data.Devices) >= 64 {
					return errors.New("at most 64 devices")
				}
				data.Devices = append(data.Devices, device)
			} else {
				data.Devices[index] = device
			}
			return nil
		})
		if e != nil {
			return nil, e
		}
		return nil, a.Devices.Reload()
	case "device-order":
		var req struct {
			IDs []string `json:"ids"`
		}
		if e := web.Decode(body, &req); e != nil {
			return nil, e
		}
		e := a.Store.Update(func(data *config.Data) error {
			if len(req.IDs) != len(data.Devices) {
				return errors.New("order must contain every device exactly once")
			}
			byID := map[string]config.Device{}
			for _, d := range data.Devices {
				byID[d.ID] = d
			}
			ordered := make([]config.Device, 0, len(req.IDs))
			for _, id := range req.IDs {
				d, ok := byID[id]
				if !ok {
					return errors.New("unknown or duplicate device")
				}
				delete(byID, id)
				ordered = append(ordered, d)
			}
			data.Devices = ordered
			return nil
		})
		if e != nil {
			return nil, e
		}
		return nil, a.Devices.Reload()
	case "hotkey":
		var req struct {
			Keys []uint16 `json:"keys"`
		}
		if e := web.Decode(body, &req); e != nil {
			return nil, e
		}
		if _, ok := input.NewHotkey(req.Keys); !ok {
			return nil, capture.ErrHotkey
		}
		if e := a.Store.Update(func(data *config.Data) error { data.Hotkey = req.Keys; return nil }); e != nil {
			return nil, e
		}
		if e := a.Devices.Reload(); e != nil {
			return nil, e
		}
		return nil, a.Dispatcher.Recording(false)
	case "mouse-guard":
		var req struct {
			Enabled bool `json:"enabled"`
		}
		if e := web.Decode(body, &req); e != nil {
			return nil, e
		}
		guard, ok := a.Capture.(MouseGuard)
		if !ok {
			return nil, errors.New("mouse guard is only available on Linux")
		}
		if e := a.Store.Update(func(data *config.Data) error { data.MouseGuard = req.Enabled; return nil }); e != nil {
			return nil, e
		}
		guard.SetMouseGuard(req.Enabled)
		return nil, nil
	case "serial-ports":
		return serialcfg.Ports()
	case "serial-info", "serial-set":
		var req struct {
			Port   string          `json:"port"`
			ID     string          `json:"id,omitempty"`
			Config json.RawMessage `json:"config,omitempty"`
		}
		if e := web.Decode(body, &req); e != nil {
			return nil, e
		}
		if req.Port == "" {
			return nil, errors.New("serial port required")
		}
		c, e := serialcfg.Open(req.Port)
		if e != nil {
			return nil, e
		}
		defer c.Close()
		if name == "serial-info" {
			reply, e := c.Request(&pb.SerialPacket{Body: &pb.SerialPacket_InfoGet{InfoGet: &pb.Empty{}}})
			if e != nil {
				return nil, e
			}
			info := reply.GetInfo()
			if info == nil || info.Config == nil || info.Config.Password != nil || info.Config.Psk != nil {
				return nil, errors.New("invalid or unredacted device info")
			}
			return protobuf(info)
		}
		candidate := new(pb.Config)
		if protojson.Unmarshal(req.Config, candidate) != nil {
			return nil, errors.New("invalid device configuration JSON")
		}
		defer func() { clear(candidate.Psk) }()
		if candidate.Psk == nil {
			data, e := a.Store.Load()
			if e != nil {
				return nil, e
			}
			for _, device := range data.Devices {
				if device.ID == req.ID {
					candidate.Psk, e = hex.DecodeString(device.PSK)
					if e != nil || len(candidate.Psk) != 16 {
						return nil, errors.New("saved device PSK must be exactly 16 bytes")
					}
					break
				}
			}
			if candidate.Psk == nil {
				return nil, errors.New("save the device and its PSK before serial provisioning")
			}
		}
		return nil, a.Store.Provision(c, req.ID, candidate)
	case "device-config-set":
		var req struct {
			ID     string          `json:"id"`
			Config json.RawMessage `json:"config"`
		}
		if e := web.Decode(body, &req); e != nil {
			return nil, e
		}
		candidate := new(pb.Config)
		if protojson.Unmarshal(req.Config, candidate) != nil {
			return nil, errors.New("invalid device configuration JSON")
		}
		return nil, a.Devices.Provision(ctx, req.ID, candidate)
	default:
		var req struct {
			ID string `json:"id"`
		}
		if e := web.Decode(body, &req); e != nil {
			return nil, e
		}
		switch name {
		case "target":
			if a.Dispatcher.View().Recording && req.ID != switcher.Local {
				return nil, errors.New("finish hotkey recording before switching")
			}
			if req.ID != switcher.Local {
				ready := false
				for _, d := range a.Devices.Views() {
					if d.ID == req.ID && d.Enabled && d.Connected && d.USB {
						ready = true
					}
				}
				if !ready {
					return nil, errors.New("target not ready")
				}
			}
			return nil, a.Dispatcher.Switch(req.ID)
		case "takeover":
			return nil, a.Devices.Takeover(req.ID)
		case "pending-discard":
			return nil, a.Store.DiscardPending(req.ID)
		case "pending-verify":
			return nil, a.Devices.Verify(ctx, req.ID)
		case "device-config-get":
			c, e := a.Devices.GetConfig(ctx, req.ID)
			if e != nil {
				return nil, e
			}
			return protobuf(c)
		case "device-delete":
			e := a.Store.Update(func(data *config.Data) error {
				for i, d := range data.Devices {
					if d.ID == req.ID {
						data.Devices = append(data.Devices[:i], data.Devices[i+1:]...)
						return nil
					}
				}
				return errors.New("unknown device")
			})
			if e != nil {
				return nil, e
			}
			return nil, a.Devices.Reload()
		}
	}
	return nil, errors.New("unsupported operation")
}
