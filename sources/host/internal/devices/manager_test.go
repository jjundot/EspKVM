package devices

import (
	"bufio"
	"context"
	"fmt"
	"github.com/jjundot/EspKVM/internal/config"
	"github.com/jjundot/EspKVM/internal/input"
	"github.com/jjundot/EspKVM/internal/session"
	"github.com/jjundot/EspKVM/internal/switcher"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

type localCapture struct{}

func (localCapture) Local()                          {}
func (localCapture) Remote() (bool, error)           { return true, nil }
func (localCapture) ReleaseLocal(*input.State) error { return nil }
func until(t *testing.T, p func() bool) {
	t.Helper()
	end := time.Now().Add(3 * time.Second)
	for !p() {
		if time.Now().After(end) {
			t.Fatal("state stalled")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
func TestFirmwareSatisfiedTakeoverDoesNotAuthorizeLaterSeizure(t *testing.T) {
	server := os.Getenv("ESPKVM_NETWORK_TEST_SERVER")
	if server == "" {
		t.Skip("native firmware server not configured")
	}
	cmd := exec.Command(server)
	cmd.Env = append(os.Environ(), "ESPKVM_TEST_AUTH_DELAY_MS=200")
	out, e := cmd.StdoutPipe()
	if e != nil {
		t.Fatal(e)
	}
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	scanner := bufio.NewScanner(out)
	if !scanner.Scan() {
		t.Fatal("missing READY")
	}
	var port uint16
	if _, e = fmt.Sscanf(scanner.Text(), "READY %d", &port); e != nil {
		t.Fatal(e)
	}
	challenge := make(chan struct{}, 4)
	go func() {
		for scanner.Scan() {
			if scanner.Text() == "CHALLENGE" {
				select {
				case challenge <- struct{}{}:
				default:
				}
			}
		}
	}()
	store := config.Open(filepath.Join(t.TempDir(), "config.json"))
	if e = store.Update(func(data *config.Data) error {
		data.Devices = []config.Device{{ID: "test", Name: "test", IP: "127.0.0.1", Port: port, PSK: "00000000000000000000000000000000", Enabled: true}}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := switcher.New(localCapture{})
	go d.Run(ctx)
	m, e := New(ctx, store, d)
	if e != nil {
		t.Fatal(e)
	}
	defer m.Close()
	select {
	case <-challenge:
	case <-time.After(time.Second):
		t.Fatal("handshake did not begin")
	}
	if e = m.Takeover("test"); e != nil {
		t.Fatal(e)
	} // Enqueued while an ordinary handshake is in progress.
	until(t, func() bool { return m.Views()[0].Connected })
	foreign, e := session.DialWithOptions(ctx, fmt.Sprintf("127.0.0.1:%d", port), make([]byte, 16), session.Options{Owner: [16]byte{42}, Takeover: true})
	if e != nil {
		t.Fatal(e)
	}
	defer foreign.Close()
	ended := make(chan error, 1)
	go func() { ended <- foreign.Run(ctx, nil) }()
	until(t, func() bool { return !m.Views()[0].Connected })
	select {
	case e := <-ended:
		t.Fatal("stale user intent seized the later owner", e)
	case <-time.After(800 * time.Millisecond):
	}
}
func TestFirmwareDeviceLifecycle(t *testing.T) {
	server := os.Getenv("ESPKVM_NETWORK_TEST_SERVER")
	if server == "" {
		t.Skip("native firmware server not configured")
	}
	cmd := exec.Command(server)
	out, e := cmd.StdoutPipe()
	if e != nil {
		t.Fatal(e)
	}
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	scanner := bufio.NewScanner(out)
	if !scanner.Scan() {
		t.Fatal("server missing READY")
	}
	var port uint16
	if _, e = fmt.Sscanf(scanner.Text(), "READY %d", &port); e != nil {
		t.Fatal(e)
	}
	store := config.Open(filepath.Join(t.TempDir(), "config.json"))
	if e = store.Update(func(data *config.Data) error {
		data.Devices = []config.Device{{ID: "test", Name: "test", IP: "127.0.0.1", Port: port, PSK: "00000000000000000000000000000000", Enabled: true}}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := switcher.New(localCapture{})
	go d.Run(ctx)
	m, e := New(ctx, store, d)
	if e != nil {
		t.Fatal(e)
	}
	defer m.Close()
	until(t, func() bool { views := m.Views(); return len(views) == 1 && views[0].Connected && views[0].USB })
	if d.View().Target != switcher.Local {
		t.Fatal("connect selected remote automatically")
	}
	c, e := m.GetConfig(ctx, "test")
	if e != nil {
		t.Fatal(e)
	}
	if c.Password != nil || c.Psk != nil {
		t.Fatal("credentials leaked")
	}
	// The native adapter deliberately rejects CFG_SET with an unpersisted result.
	if e = m.Provision(ctx, "test", c); e == nil {
		t.Fatal("missing rejection")
	}
	data, e := store.Load()
	if e != nil || len(data.Pending) != 0 {
		t.Fatal("rejected pending retained", data, e)
	}
	if e = store.Update(func(data *config.Data) error {
		data.Pending = append(data.Pending, config.Pending{Token: "verify", New: data.Devices[0], Expected: c, State: "unknown"})
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	if e = m.Verify(ctx, "test"); e != nil {
		t.Fatal("verification raced owned reconnect", e)
	}
	until(t, func() bool { return m.Views()[0].Connected })
	if e = store.Update(func(data *config.Data) error { data.Devices[0].Enabled = false; return nil }); e != nil {
		t.Fatal(e)
	}
	go m.Watch() // An external CLI/file update is observed without a UI reload.
	until(t, func() bool { return !m.Views()[0].Connected && !m.Views()[0].Enabled })
	if d.View().Target != switcher.Local {
		t.Fatal("disable failed to restore LOCAL")
	}
}
