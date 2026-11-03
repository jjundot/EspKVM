package service

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// stopEvent lets "espkvm stop" end the host gracefully (releasing the cursor
// clip and remote keys) instead of schtasks /End terminating it. It lives in
// the session namespace and doubles as the single-instance guard.
const stopEvent = `Local\EspKVM-stop`

// WatchStop creates the stop event and calls stop once it is signalled.
func WatchStop(stop func()) error {
	u, e := user.Current()
	if e != nil {
		return e
	}
	// The task runs elevated; grant the same user at medium integrity so an
	// ordinary terminal can still ask it to stop.
	sd, e := windows.SecurityDescriptorFromString("D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GA;;;" + u.Uid + ")S:(ML;;NW;;;ME)")
	if e != nil {
		return e
	}
	name, e := windows.UTF16PtrFromString(stopEvent)
	if e != nil {
		return e
	}
	sa := &windows.SecurityAttributes{SecurityDescriptor: sd}
	sa.Length = uint32(unsafe.Sizeof(*sa))
	event, e := windows.CreateEvent(sa, 1, 0, name)
	if errors.Is(e, windows.ERROR_ALREADY_EXISTS) {
		windows.CloseHandle(event)
		return errors.New("EspKVM is already running in this session (see espkvm status)")
	} else if e != nil {
		return e
	}
	go func() {
		windows.WaitForSingleObject(event, windows.INFINITE)
		stop()
	}()
	return nil
}

func running() bool {
	name, _ := windows.UTF16PtrFromString(stopEvent)
	event, e := windows.OpenEvent(windows.SYNCHRONIZE, false, name)
	if e != nil {
		return false
	}
	windows.CloseHandle(event)
	return true
}

func schtasks(args ...string) (string, error) {
	out, e := exec.Command("schtasks.exe", args...).CombinedOutput()
	text := strings.TrimSpace(decodeOutput(out))
	if e != nil {
		return text, fmt.Errorf("schtasks %s: %s", args[0], text)
	}
	return text, nil
}

func installed() bool {
	_, e := schtasks("/Query", "/TN", taskName)
	return e == nil
}

func requireAdmin() error {
	if !windows.GetCurrentProcessToken().IsElevated() {
		return errors.New("run from an administrator terminal")
	}
	return nil
}

func install() error {
	if e := requireAdmin(); e != nil {
		return e
	}
	exe, e := executable()
	if e != nil {
		return e
	}
	u, e := user.Current()
	if e != nil {
		return e
	}
	xml, e := taskXML(exe, u.Uid)
	if e != nil {
		return e
	}
	file, e := os.CreateTemp("", "espkvm-task-*.xml")
	if e != nil {
		return e
	}
	defer os.Remove(file.Name())
	_, e = file.Write(utf16File(xml))
	if c := file.Close(); e == nil {
		e = c
	}
	if e != nil {
		return e
	}
	// A previously registered build may be running; replace it cleanly.
	if e = stop(); e != nil && running() {
		return e
	}
	if _, e = schtasks("/Create", "/TN", taskName, "/XML", file.Name(), "/F"); e != nil {
		return e
	}
	if e = start(); e != nil {
		return e
	}
	fmt.Printf("Installed scheduled task %s: %s runs at logon of %s.\n", taskName, exe, u.Username)
	fmt.Println("Control UI: http://127.0.0.1:62626")
	return nil
}

func uninstall() error {
	if !installed() {
		return errors.New("not installed")
	}
	if e := stop(); e != nil && running() {
		return e
	}
	if _, e := schtasks("/Delete", "/TN", taskName, "/F"); e != nil {
		return e
	}
	fmt.Printf("Removed scheduled task %s.\n", taskName)
	return nil
}

func start() error {
	if !installed() {
		return errors.New("not installed; run espkvm install from an administrator terminal")
	}
	if running() {
		fmt.Println("already running")
		return nil
	}
	if _, e := schtasks("/Run", "/TN", taskName); e != nil {
		return e
	}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if running() {
			fmt.Println("started")
			return nil
		}
	}
	return errors.New("task started but EspKVM did not come up; run the executable in a terminal to see the error")
}

func stop() error {
	name, _ := windows.UTF16PtrFromString(stopEvent)
	event, e := windows.OpenEvent(windows.EVENT_MODIFY_STATE, false, name)
	if e != nil {
		if running() {
			return errors.New("EspKVM is running but cannot be signalled; retry from an administrator terminal")
		}
		fmt.Println("not running")
		return nil
	}
	e = windows.SetEvent(event)
	windows.CloseHandle(event)
	if e != nil {
		return e
	}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if !running() {
			fmt.Println("stopped")
			return nil
		}
	}
	if installed() {
		if _, e = schtasks("/End", "/TN", taskName); e == nil {
			fmt.Println("stopped (forced)")
			return nil
		}
	}
	return errors.New("EspKVM did not stop within 5 seconds")
}

func restart() error {
	if e := stop(); e != nil {
		return e
	}
	return start()
}

func status() error {
	state := "stopped"
	if running() {
		state = "running"
	}
	if !installed() {
		fmt.Printf("service: not installed (%s)\n", state)
		return nil
	}
	fmt.Printf("service: %s\n", state)
	xml, e := schtasks("/Query", "/TN", taskName, "/XML")
	if e != nil {
		return e
	}
	exe, e := taskExecutable(xml)
	if e != nil {
		return e
	}
	fmt.Printf("executable: %s\n", exe)
	if self, e := executable(); e == nil && !sameFile(self, exe) {
		fmt.Println("note: registered for a different executable; run espkvm install from this one to switch")
	}
	return nil
}
