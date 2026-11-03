package cli

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/jjundot/EspKVM/internal/config"
	"os"
	"time"

	"github.com/jjundot/EspKVM/internal/protocol/pb"
	"github.com/jjundot/EspKVM/internal/serialcfg"
	"github.com/jjundot/EspKVM/internal/session"
	"google.golang.org/protobuf/encoding/protojson"
)

func Run(args []string) error {
	flags := flag.NewFlagSet("espkvm ctl", flag.ContinueOnError)
	port := flags.String("serial", "", "serial port")
	addr := flags.String("address", "", "explicit device IP:port")
	pskFile := flags.String("psk-file", "", "file containing hex PSK (never pass secrets on command line)")
	cfg := flags.String("config", "", "full device config JSON for serial-set")
	id := flags.String("id", "", "stable device id")
	storePath := flags.String("store", "", "client credential path")
	confirm := flags.Bool("confirm-reset", false, "explicitly authorize factory reset")
	if e := flags.Parse(args); e != nil {
		return e
	}
	if flags.NArg() != 1 {
		return errors.New("usage: espkvm ctl [flags] ports|info|serial-set|reboot|factory-reset|check|verify|pending-list|pending-discard|new-psk")
	}
	if *storePath == "" {
		p, e := config.DefaultPath()
		if e != nil {
			return e
		}
		*storePath = p
	}
	store := config.Open(*storePath)
	action := flags.Arg(0)
	switch action {
	case "ports":
		p, e := serialcfg.Ports()
		if e != nil {
			return e
		}
		return json.NewEncoder(os.Stdout).Encode(p)
	case "new-psk":
		p, e := config.NewPSK()
		if e == nil {
			fmt.Println(p)
		}
		return e
	case "pending-list":
		d, e := store.Load()
		if e != nil {
			return e
		}
		for _, p := range d.Pending {
			fmt.Printf("%s\t%s\t%s\t%s:%d\n", p.New.ID, p.State, p.New.Name, p.New.IP, p.New.Port)
		}
		return nil
	case "pending-discard":
		return store.DiscardPending(*id)
	case "verify":
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		return store.Verify(ctx, *id)
	case "check":
		raw, e := os.ReadFile(*pskFile)
		if e != nil {
			return e
		}
		var text string
		for _, c := range raw {
			if c != '\r' && c != '\n' {
				text += string(c)
			}
		}
		key, e := hex.DecodeString(text)
		if e != nil {
			return e
		}
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		s, e := session.Dial(ctx, *addr, key)
		if e != nil {
			return e
		}
		defer s.Close()
		fmt.Println("mutual authentication successful")
		return nil
	}
	var req *pb.SerialPacket
	switch action {
	case "info":
		req = &pb.SerialPacket{Body: &pb.SerialPacket_InfoGet{InfoGet: &pb.Empty{}}}
	case "reboot":
		req = &pb.SerialPacket{Body: &pb.SerialPacket_Reboot{Reboot: &pb.Empty{}}}
	case "factory-reset":
		if !*confirm {
			return errors.New("factory reset requires -confirm-reset")
		}
		req = &pb.SerialPacket{Body: &pb.SerialPacket_FactoryReset{FactoryReset: &pb.Empty{}}}
	case "serial-set":
		raw, e := os.ReadFile(*cfg)
		if e != nil {
			return e
		}
		candidate := new(pb.Config)
		if e = protojson.Unmarshal(raw, candidate); e != nil {
			return e
		}
		c, e := serialcfg.Open(*port)
		if e != nil {
			return e
		}
		defer c.Close()
		return store.Provision(c, *id, candidate)
	default:
		return errors.New("unknown command")
	}
	c, e := serialcfg.Open(*port)
	if e != nil {
		return e
	}
	defer c.Close()
	reply, e := c.Request(req)
	if e != nil {
		return e
	}
	raw, e := protojson.MarshalOptions{Indent: "  "}.Marshal(reply)
	if e == nil {
		fmt.Println(string(raw))
	}
	return e
}
