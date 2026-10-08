package cloud

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
)

const linuxUpdateGuardProgram = `#!/bin/sh
set -eu
state=/var/lib/hankagent/update-guard.state
lock=/var/lib/hankagent/update-guard.lock
binary=/usr/bin/hankagent
test -f "$state" || exit 0
exec 9>"$lock"
flock 9
test -f "$state" || exit 0
assignment_id=$(sed -n 's/^assignment_id=\(luasg_[A-Za-z0-9_-]\{8,128\}\)$/\1/p' "$state")
target_version=$(sed -n 's/^target_version=\([0-9][0-9]*\.[0-9][0-9]*\.[0-9][0-9]*\)$/\1/p' "$state")
deadline_epoch=$(sed -n 's/^deadline_epoch=\([0-9][0-9]*\)$/\1/p' "$state")
test -n "$assignment_id" && test -n "$target_version" && test -n "$deadline_epoch" || exit 65
test "$(wc -l < "$state")" -eq 3 || exit 65
now=$(date +%s)
if [ "$deadline_epoch" -gt "$now" ]; then sleep "$((deadline_epoch-now))"; fi
test -f "$state" || exit 0
test -f "$binary.previous" || { printf 'recovery_failed\n' > /var/lib/hankagent/update-guard.result; exit 70; }
failed=$binary.failed-guard
mv "$binary" "$failed"
if ! mv "$binary.previous" "$binary"; then mv "$failed" "$binary"; printf 'recovery_failed\n' > /var/lib/hankagent/update-guard.result; exit 70; fi
chmod 0755 "$binary"
rm -f "$failed" "$state"
printf 'rolled_back\n' > /var/lib/hankagent/update-guard.result
chmod 0600 /var/lib/hankagent/update-guard.result
systemctl restart hankagent.service
`

const linuxUpdateGuardUnit = `[Unit]
Description=HankAgent update recovery guard
After=local-fs.target

[Service]
Type=oneshot
ExecStart=/usr/libexec/hankagent-update-guard

[Install]
WantedBy=multi-user.target
`

func legacyLinuxUpdateCommand(assignment domain.LinuxAgentUpdateAssignment, manifestURL, fromVersion string, deadline time.Time) (string, error) {
	pending, err := json.Marshal(map[string]any{
		"rollout_id": assignment.RolloutID, "assignment_id": assignment.ID, "from_version": fromVersion,
		"to_version": assignment.ToVersion, "manifest_url": manifestURL, "state": "reconnecting", "health_deadline": deadline.UTC(),
	})
	if err != nil {
		return "", err
	}
	guardState := fmt.Sprintf("assignment_id=%s\ntarget_version=%s\ndeadline_epoch=%d\n", assignment.ID, assignment.ToVersion, deadline.Unix())
	encode := func(value []byte) string { return base64.StdEncoding.EncodeToString(value) }
	parts := []string{
		"set -eu",
		"install -d -m 0700 /var/lib/hankagent",
		"install -d -m 0755 /usr/libexec /etc/systemd/system",
		fmt.Sprintf("printf %%s %s | base64 -d > /usr/libexec/.hankagent-update-guard.tmp", encode([]byte(linuxUpdateGuardProgram))),
		"chmod 0755 /usr/libexec/.hankagent-update-guard.tmp",
		"mv -f /usr/libexec/.hankagent-update-guard.tmp /usr/libexec/hankagent-update-guard",
		fmt.Sprintf("printf %%s %s | base64 -d > /etc/systemd/system/.hankagent-update-guard.service.tmp", encode([]byte(linuxUpdateGuardUnit))),
		"chmod 0644 /etc/systemd/system/.hankagent-update-guard.service.tmp",
		"mv -f /etc/systemd/system/.hankagent-update-guard.service.tmp /etc/systemd/system/hankagent-update-guard.service",
		fmt.Sprintf("printf %%s %s | base64 -d > /var/lib/hankagent/.update-assignment.tmp", encode(append(pending, '\n'))),
		"chmod 0600 /var/lib/hankagent/.update-assignment.tmp",
		"mv -f /var/lib/hankagent/.update-assignment.tmp /var/lib/hankagent/update-assignment.json",
		fmt.Sprintf("printf %%s %s | base64 -d > /var/lib/hankagent/.update-guard.tmp", encode([]byte(guardState))),
		"chmod 0600 /var/lib/hankagent/.update-guard.tmp",
		"mv -f /var/lib/hankagent/.update-guard.tmp /var/lib/hankagent/update-guard.state",
		"systemctl daemon-reload",
		"systemctl enable hankagent-update-guard.service",
		"systemctl start --no-block hankagent-update-guard.service",
		fmt.Sprintf("/usr/bin/hankagent --system update apply --manifest %s", shellSingleQuote(manifestURL)),
	}
	return strings.Join(parts, "; "), nil
}

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
