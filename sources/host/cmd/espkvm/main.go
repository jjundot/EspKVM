package main

import (
	"context"
	"embed"
	"errors"
	"flag"
	"fmt"
	"github.com/jjundot/EspKVM/internal/capture"
	"github.com/jjundot/EspKVM/internal/cli"
	"github.com/jjundot/EspKVM/internal/config"
	"github.com/jjundot/EspKVM/internal/control"
	"github.com/jjundot/EspKVM/internal/debuglog"
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

//go:embed ui/*
var ui embed.FS
var version = "development"

func run() error {
	if len(os.Args) > 1 && os.Args[1] == "ctl" {
		return cli.Run(os.Args[2:])
	}
	flags := flag.NewFlagSet(os.Args[0], flag.ContinueOnError)
	storePath := flags.String("store", "", "credential store path")
	browser := flags.Bool("browser", true, "open the control UI with a one-time fragment credential")
	bootstrapFile := flags.String("bootstrap-file", "", "create a private launch-URL file; never print the credential in logs")
	captureInput := flags.Bool("capture", true, "capture physical input; false runs the authenticated control API only")
	exitAfter := flags.Duration("exit-after", 0, "optional bounded diagnostic runtime")
	debug := flags.Bool("debug", false, "enable diagnostic logging")
	if e := flags.Parse(os.Args[1:]); e != nil {
		return e
	}
	debuglog.SetEnabled(*debug)
	debuglog.Printf("host", "starting version=%s capture=%t browser=%t", version, *captureInput, *browser)
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
	app := &control.App{Store: store, Devices: manager, Dispatcher: dispatcher, Capture: source, Version: version}
	app.InputStatus("disabled")
	assets, e := fs.Sub(ui, "ui")
	if e != nil {
		return e
	}
	api, token, e := web.NewWithAuth("127.0.0.1:62626", app.State, app.Operation, assets, store)
	if e != nil {
		return e
	}
	listener, e := net.Listen("tcp4", "127.0.0.1:62626")
	if e != nil {
		return e
	}
	defer listener.Close()
	debuglog.Printf("host", "control API listening on %s", listener.Addr())
	server := &http.Server{Handler: api.Handler(), ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192}
	serving := make(chan error, 1)
	go func() { serving <- server.Serve(listener) }()
	defer func() {
		shutdown, stop := context.WithTimeout(context.Background(), 2*time.Second)
		defer stop()
		server.Shutdown(shutdown)
	}()
	baseURL := "http://127.0.0.1:62626/"
	bootstrapURL := baseURL + "#" + token
	if *bootstrapFile != "" {
		if e = config.WritePrivate(*bootstrapFile, []byte(bootstrapURL+"\n")); e != nil {
			return e
		}
		defer os.Remove(*bootstrapFile)
	}
	if *browser {
		if e = openBrowser(baseURL); e != nil {
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
				debuglog.Printf("capture", "input capture stopped: %v", e)
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
