package service

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

const (
	unitName  = "espkvm.service"
	unitPath  = "/etc/systemd/system/" + unitName
	rulesPath = "/etc/udev/rules.d/70-espkvm-input.rules"
	// uinput is often a module; the optional mouse guard needs it at boot.
	modulesPath = "/etc/modules-load.d/espkvm.conf"
	group       = "espkvm"
)

//go:embed 70-espkvm-input.rules
var rules []byte

// WatchStop is a no-op on Linux: systemd stops the host with SIGTERM.
func WatchStop(func()) error { return nil }

func requireRoot() error {
	if os.Geteuid() != 0 {
		return errors.New("run with sudo")
	}
	return nil
}

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if e := cmd.Run(); e != nil {
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), e)
	}
	return nil
}

func reloadRules() error {
	if e := run("udevadm", "control", "--reload-rules"); e != nil {
		return e
	}
	if e := run("udevadm", "trigger", "--subsystem-match=input", "--action=change"); e != nil {
		return e
	}
	return run("udevadm", "trigger", "--subsystem-match=misc", "--sysname-match=uinput", "--action=change")
}

func install() error {
	if e := requireRoot(); e != nil {
		return e
	}
	exe, e := executable()
	if e != nil {
		return e
	}
	// The directory owner is the account that can keep config.json beside exe.
	info, e := os.Stat(filepath.Dir(exe))
	if e != nil {
		return e
	}
	owner, e := user.LookupId(strconv.Itoa(int(info.Sys().(*syscall.Stat_t).Uid)))
	if e != nil {
		return e
	}
	primary, e := user.LookupGroupId(owner.Gid)
	if e != nil {
		return e
	}
	if _, e = user.LookupGroup(group); e != nil {
		if e = run("groupadd", "--system", group); e != nil {
			return e
		}
	}
	groups := []string{group}
	// Serial provisioning from the UI needs the distribution's tty group.
	for _, tty := range []string{"dialout", "uucp"} {
		if _, e = user.LookupGroup(tty); e == nil {
			groups = append(groups, tty)
			break
		}
	}
	unit, e := systemdUnit(exe, owner.Username, primary.Name, groups)
	if e != nil {
		return e
	}
	if owner.Uid != "0" {
		// Lets foreground runs read input too, after the next login.
		if e = run("usermod", "-a", "-G", group, owner.Username); e != nil {
			return e
		}
	}
	if e = os.WriteFile(rulesPath, rules, 0644); e != nil {
		return e
	}
	if e = os.WriteFile(modulesPath, []byte("uinput\n"), 0644); e != nil {
		return e
	}
	if e = run("modprobe", "uinput"); e != nil {
		fmt.Println("warning:", e, "(the optional mouse guard needs uinput)")
	}
	if e = reloadRules(); e != nil {
		return e
	}
	if e = os.WriteFile(unitPath, []byte(unit), 0644); e != nil {
		return e
	}
	for _, args := range [][]string{{"daemon-reload"}, {"enable", unitName}, {"restart", unitName}} {
		if e = run("systemctl", args...); e != nil {
			return e
		}
	}
	fmt.Printf("Installed %s: %s runs as %s at boot.\n", unitName, exe, owner.Username)
	if owner.Uid == "0" {
		fmt.Println("The service runs as root because the executable's directory is owned by root.")
	}
	fmt.Println("Control UI: http://127.0.0.1:62626   Logs: journalctl -u espkvm -f")
	return nil
}

func uninstall() error {
	if e := requireRoot(); e != nil {
		return e
	}
	if _, e := os.Stat(unitPath); e != nil {
		return errors.New("not installed")
	}
	if e := run("systemctl", "disable", "--now", unitName); e != nil {
		return e
	}
	for _, p := range []string{unitPath, rulesPath, modulesPath} {
		if e := os.Remove(p); e != nil && !errors.Is(e, os.ErrNotExist) {
			return e
		}
	}
	if e := run("systemctl", "daemon-reload"); e != nil {
		return e
	}
	if e := reloadRules(); e != nil {
		return e
	}
	fmt.Printf("Removed %s, the input udev rule and the uinput module entry; the %s group is kept.\n", unitName, group)
	return nil
}

func control(verb string) error {
	if _, e := os.Stat(unitPath); e != nil {
		return errors.New("not installed; run sudo espkvm install")
	}
	return run("systemctl", verb, unitName)
}

func start() error   { return control("start") }
func stop() error    { return control("stop") }
func restart() error { return control("restart") }

func status() error {
	unit, e := os.ReadFile(unitPath)
	if errors.Is(e, os.ErrNotExist) {
		fmt.Println("service: not installed")
		return nil
	} else if e != nil {
		return e
	}
	out, _ := exec.Command("systemctl", "is-active", unitName).Output()
	fmt.Printf("service: %s\n", strings.TrimSpace(string(out)))
	exe, e := unitExecutable(string(unit))
	if e != nil {
		return e
	}
	fmt.Printf("executable: %s\n", exe)
	if self, e := executable(); e == nil && !sameFile(self, exe) {
		fmt.Println("note: registered for a different executable; run sudo espkvm install from this one to switch")
	}
	return nil
}
