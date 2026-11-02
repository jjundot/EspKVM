package control

import (
	"context"
	"encoding/json"
	"github.com/jjundot/EspKVM/internal/config"
	"github.com/jjundot/EspKVM/internal/devices"
	"github.com/jjundot/EspKVM/internal/input"
	"github.com/jjundot/EspKVM/internal/protocol/pb"
	"github.com/jjundot/EspKVM/internal/switcher"
	"google.golang.org/protobuf/proto"
	"path/filepath"
	"strings"
	"testing"
)

type source struct{}

func (source) Local()                          {}
func (source) Remote() (bool, error)           { return true, nil }
func (source) ReleaseLocal(*input.State) error { return nil }
func (source) Run(ctx context.Context, _ func(input.Event) bool) error {
	<-ctx.Done()
	return ctx.Err()
}
func (source) SetHotkey(keys []uint16) error {
	_, ok := input.NewHotkey(keys)
	if !ok {
		return ErrTestHotkey{}
	}
	return nil
}

type ErrTestHotkey struct{}

func (ErrTestHotkey) Error() string { return "bad hotkey" }
func app(t *testing.T) *App {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	store := config.Open(filepath.Join(t.TempDir(), "config.json"))
	d := switcher.New(source{})
	go d.Run(ctx)
	manager, e := devices.New(ctx, store, d)
	if e != nil {
		cancel()
		t.Fatal(e)
	}
	t.Cleanup(func() { cancel(); manager.Close() })
	return &App{Store: store, Devices: manager, Dispatcher: d, Capture: source{}}
}
func TestDeviceOperationsDoNotExposeCredentials(t *testing.T) {
	a := app(t)
	ctx := context.Background()
	secret := "1234567890abcdef1234567890abcdef"
	for _, id := range []string{"a", "b"} {
		body := `{"id":"` + id + `","name":"device","ip":"192.168.1.200","port":24800,"psk":"` + secret + `","enabled":false}`
		if _, e := a.Operation(ctx, "device-save", json.RawMessage(body)); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := a.Operation(ctx, "device-order", json.RawMessage(`{"ids":["b","b"]}`)); e == nil {
		t.Fatal("duplicate order accepted")
	}
	if _, e := a.Operation(ctx, "device-order", json.RawMessage(`{"ids":["b","a"]}`)); e != nil {
		t.Fatal(e)
	}
	if e := a.Store.Update(func(data *config.Data) error {
		data.Pending = append(data.Pending, config.Pending{New: config.Device{ID: "pending", PSK: secret}, Expected: &pb.Config{Password: proto.String("never expose password"), Psk: []byte(secret)}})
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	b, e := json.Marshal(a.State())
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(b), secret) || strings.Contains(string(b), "never expose password") || strings.Contains(string(b), "\"psk\"") {
		t.Fatal("state contains credentials")
	}
	if a.State().Devices[0].ID != "b" {
		t.Fatal("order lost")
	}
	if _, e := a.Operation(ctx, "hotkey", json.RawMessage(`{"keys":[224]}`)); e == nil {
		t.Fatal("single key accepted")
	}
	data, e := a.Store.Load()
	if e != nil || len(data.Hotkey) != 2 || data.Hotkey[0] != 0xe0 {
		t.Fatal("invalid hotkey persisted")
	}
}
