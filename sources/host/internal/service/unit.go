package service

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
)

// systemdUnit renders the Linux unit. The service runs as the owner of the
// executable's directory so config.json beside the binary stays writable;
// everything else on the filesystem is read-only to it.
func systemdUnit(exe, user, group string, groups []string) (string, error) {
	for _, value := range append([]string{exe, user, group}, groups...) {
		// Quotes, escapes and specifiers would change the meaning of the unit.
		if value == "" || strings.ContainsAny(value, "\"\\%$\r\n") {
			return "", fmt.Errorf("unsupported character in %q for a systemd unit", value)
		}
	}
	args := strings.Join(RunArgs, " ")
	return fmt.Sprintf(`[Unit]
Description=EspKVM host
Wants=network-online.target
After=network-online.target

[Service]
User=%s
Group=%s
SupplementaryGroups=%s
ExecStart="%s" %s
Restart=on-failure
RestartSec=3
NoNewPrivileges=yes
PrivateTmp=yes
ProtectSystem=strict
ReadWritePaths="%s"

[Install]
WantedBy=multi-user.target
`, user, group, strings.Join(groups, " "), exe, args, path.Dir(exe)), nil
}

var execStart = regexp.MustCompile(`(?m)^ExecStart="([^"]+)"`)

func unitExecutable(unit string) (string, error) {
	m := execStart.FindStringSubmatch(unit)
	if m == nil {
		return "", errors.New("unit has no ExecStart")
	}
	return m[1], nil
}
