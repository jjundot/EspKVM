package service

import (
	"strings"
	"testing"
)

func TestSystemdUnit(t *testing.T) {
	unit, e := systemdUnit("/home/a b/espkvm", "alice", "alice", []string{"espkvm", "dialout"})
	if e != nil {
		t.Fatal(e)
	}
	for _, want := range []string{
		"User=alice\n",
		"SupplementaryGroups=espkvm dialout\n",
		`ExecStart="/home/a b/espkvm" -browser=false` + "\n",
		`ReadWritePaths="/home/a b"` + "\n",
		"ProtectSystem=strict\n",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("unit lacks %q:\n%s", want, unit)
		}
	}
	if exe, e := unitExecutable(unit); e != nil || exe != "/home/a b/espkvm" {
		t.Fatalf("unitExecutable = %q, %v", exe, e)
	}
	for _, bad := range []string{`/x"y`, `/x%i`, "/x\ny", `/x\y`, `/x$y`} {
		if _, e := systemdUnit(bad, "alice", "alice", []string{"espkvm"}); e == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestTaskXML(t *testing.T) {
	exe := `C:\Tools\R&D\espkvm.exe`
	xml, e := taskXML(exe, "S-1-5-21-1-2-3-1001")
	if e != nil {
		t.Fatal(e)
	}
	for _, want := range []string{
		"<UserId>S-1-5-21-1-2-3-1001</UserId>",
		"<LogonType>InteractiveToken</LogonType>",
		"<RunLevel>HighestAvailable</RunLevel>",
		`<Command>%SystemRoot%\System32\conhost.exe</Command>`,
		`<WorkingDirectory>C:\Tools\R&amp;D</WorkingDirectory>`,
	} {
		if !strings.Contains(xml, want) {
			t.Errorf("task lacks %q:\n%s", want, xml)
		}
	}
	if got, e := taskExecutable(xml); e != nil || got != exe {
		t.Fatalf("taskExecutable = %q, %v", got, e)
	}
	// schtasks /Query /XML writes quotes literally.
	queried := `<Arguments>--headless "C:\Tools\R&amp;D\espkvm.exe" -browser=false</Arguments>`
	if got, e := taskExecutable(queried); e != nil || got != exe {
		t.Fatalf("taskExecutable(queried) = %q, %v", got, e)
	}
	if got := decodeOutput(utf16File(xml)); got != xml {
		t.Fatal("UTF-16 round trip changed the task")
	}
	if _, e = taskXML(`C:\a"b.exe`, "S-1"); e == nil {
		t.Fatal("accepted quote in path")
	}
}
