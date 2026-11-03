package service

import (
	"errors"
	"fmt"
	"html"
	"regexp"
	"strings"
	"unicode/utf16"
)

const taskName = "EspKVM"

// taskXML renders the Windows logon task. Input hooks only work inside the
// user's interactive session, so this is a logon task rather than a Windows
// service (session 0). HighestAvailable keeps hooks and SendInput working while
// an elevated window has focus; priority 4 (normal) avoids the scheduler's
// below-normal default delaying low-level hook callbacks. conhost --headless
// runs the console executable without a window.
func taskXML(exe, sid string) (string, error) {
	if strings.LastIndexByte(exe, '\\') < 1 || strings.ContainsAny(exe, "\"\r\n") || sid == "" {
		return "", fmt.Errorf("unsupported executable path %q", exe)
	}
	escape := html.EscapeString
	args := fmt.Sprintf(`--headless "%s" %s`, exe, strings.Join(RunArgs, " "))
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>EspKVM host</Description>
  </RegistrationInfo>
  <Triggers>
    <LogonTrigger>
      <Enabled>true</Enabled>
      <UserId>%[1]s</UserId>
    </LogonTrigger>
  </Triggers>
  <Principals>
    <Principal id="Author">
      <UserId>%[1]s</UserId>
      <LogonType>InteractiveToken</LogonType>
      <RunLevel>HighestAvailable</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <AllowHardTerminate>true</AllowHardTerminate>
    <AllowStartOnDemand>true</AllowStartOnDemand>
    <Enabled>true</Enabled>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <Priority>4</Priority>
    <RestartOnFailure>
      <Interval>PT1M</Interval>
      <Count>999</Count>
    </RestartOnFailure>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>%%SystemRoot%%\System32\conhost.exe</Command>
      <Arguments>%[2]s</Arguments>
      <WorkingDirectory>%[3]s</WorkingDirectory>
    </Exec>
  </Actions>
</Task>
`, escape(sid), escape(args), escape(exe[:strings.LastIndexByte(exe, '\\')])), nil
}

// utf16File is the encoding schtasks /XML expects for the declared UTF-16.
func utf16File(text string) []byte {
	units := utf16.Encode([]rune(text))
	out := make([]byte, 2, 2+2*len(units))
	out[0], out[1] = 0xff, 0xfe
	for _, u := range units {
		out = append(out, byte(u), byte(u>>8))
	}
	return out
}

// decodeOutput accepts schtasks output as either UTF-16LE or single-byte text.
func decodeOutput(raw []byte) string {
	if len(raw) >= 2 && (raw[0] == 0xff && raw[1] == 0xfe || raw[1] == 0) {
		if raw[0] == 0xff {
			raw = raw[2:]
		}
		units := make([]uint16, len(raw)/2)
		for i := range units {
			units[i] = uint16(raw[2*i]) | uint16(raw[2*i+1])<<8
		}
		return string(utf16.Decode(units))
	}
	return string(raw)
}

var taskArguments = regexp.MustCompile(`<Arguments>([^<]*)</Arguments>`)
var headlessExecutable = regexp.MustCompile(`^--headless "([^"]+)"`)

func taskExecutable(xml string) (string, error) {
	if m := taskArguments.FindStringSubmatch(xml); m != nil {
		if m = headlessExecutable.FindStringSubmatch(html.UnescapeString(m[1])); m != nil {
			return m[1], nil
		}
	}
	return "", errors.New("task does not start EspKVM")
}
