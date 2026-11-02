package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"github.com/jjundot/EspKVM/internal/capture"
	"github.com/jjundot/EspKVM/internal/config"
	"github.com/jjundot/EspKVM/internal/control"
	"github.com/jjundot/EspKVM/internal/devices"
	"github.com/jjundot/EspKVM/internal/input"
	"github.com/jjundot/EspKVM/internal/switcher"
	"github.com/jjundot/EspKVM/internal/web"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// Populated with embedded static assets once the UI design is approved.
var assets fs.FS

func run() error {
	storePath := flag.String("store", "", "credential store path")
	browser := flag.Bool("browser", true, "open the control UI with a one-time fragment credential")
	bootstrapFile := flag.String("bootstrap-file", "", "create a private launch-URL file; never print the credential in logs")
	captureInput := flag.Bool("capture", true, "capture physical input; false runs the authenticated control API only")
	exitAfter := flag.Duration("exit-after", 0, "optional bounded diagnostic runtime")
	flag.Parse()
	if *browser && assets == nil {
		return errors.New("Web UI is not yet available; use -browser=false -bootstrap-file PATH for authenticated API mode")
	}
	if !*browser && *bootstrapFile == "" {
		return errors.New("-browser=false requires -bootstrap-file for private authentication")
	}
	if *storePath == "" {
		p, e := config.DefaultPath()
		if e != nil {
			return e
		}
		*storePath = p
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if *exitAfter > 0 {
		var stop context.CancelFunc
		ctx, stop = context.WithTimeout(ctx, *exitAfter)
		defer stop()
	}
	source := capture.New()
	store := config.Open(*storePath)
	data, e := store.Load()
	if e != nil {
		return e
	}
	if e = source.SetHotkey(data.Hotkey); e != nil {
		return e
	}
	dispatcher := switcher.New(source)
	go dispatcher.Run(ctx)
	manager, e := devices.New(ctx, store, dispatcher)
	if e != nil {
		return e
	}
	defer manager.Close()
	go manager.Watch()
	app := &control.App{Store: store, Devices: manager, Dispatcher: dispatcher, Capture: source}
	app.InputStatus("disabled")
	api, token, e := web.New("127.0.0.1:62626", app.State, app.Operation, assets)
	if e != nil {
		return e
	}
	listener, e := net.Listen("tcp4", "127.0.0.1:62626")
	if e != nil {
		return e
	}
	defer listener.Close()
	server := &http.Server{Handler: api.Handler(), ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192}
	serving := make(chan error, 1)
	go func() { serving <- server.Serve(listener) }()
	defer func() {
		shutdown, stop := context.WithTimeout(context.Background(), 2*time.Second)
		defer stop()
		server.Shutdown(shutdown)
	}()
	url := "http://127.0.0.1:62626/#" + token
	if *bootstrapFile != "" {
		if e = config.WritePrivate(*bootstrapFile, []byte(url+"\n")); e != nil {
			return e
		}
		defer os.Remove(*bootstrapFile)
	}
	if *browser {
		if e = openBrowser(url); e != nil {
			return e
		}
	}
	fmt.Println("EspKVM control API: http://127.0.0.1:62626 (LOCAL until explicitly switched)")
	if *captureInput {
		captureDone := make(chan struct{})
		defer func() {
			cancel()
			source.Local()
			select {
			case <-captureDone:
			case <-time.After(time.Second):
			}
		}()
		go func() {
			defer close(captureDone)
			for ctx.Err() == nil {
				app.InputStatus("running")
				e := source.Run(ctx, dispatcher.Input)
				source.Local()
				dispatcher.Input(input.Event{Kind: input.Fault, Reset: true})
				if ctx.Err() != nil {
					return
				}
				if e != nil {
					app.InputStatus(e.Error())
				} else {
					app.InputStatus("capture stopped")
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(5 * time.Second):
				}
			}
		}()
	}
	select {
	case <-ctx.Done():
		source.Local()
		return nil
	case e := <-serving:
		source.Local()
		if errors.Is(e, http.ErrServerClosed) {
			return nil
		}
		return e
	}
}
func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
