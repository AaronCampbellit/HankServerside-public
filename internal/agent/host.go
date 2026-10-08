package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/dropfile/HankServerside/internal/protocol"
)

type hostService struct{ shellEnabled func() bool }

func (h *hostService) status() map[string]any {
	host, _ := os.Hostname()
	return map[string]any{"hostname": host, "platform": runtime.GOOS, "os_version": runtime.GOARCH, "metrics": collectHostMetrics()}
}

func collectHostMetrics() protocol.HostMetrics {
	metrics := protocol.HostMetrics{}
	if raw, err := os.ReadFile("/proc/loadavg"); err == nil {
		fields := strings.Fields(string(raw))
		if len(fields) > 0 {
			metrics.CPULoad1m, _ = strconv.ParseFloat(fields[0], 64)
		}
	}
	if raw, err := os.ReadFile("/proc/meminfo"); err == nil {
		if used, total, ok := parseProcMemory(string(raw)); ok {
			metrics.MemoryUsedBytes = used
			metrics.MemoryTotalBytes = total
		}
	}
	if raw, err := os.ReadFile("/proc/uptime"); err == nil {
		metrics.UptimeSeconds = parseProcUptime(string(raw))
	}
	var stat syscall.Statfs_t
	if syscall.Statfs("/", &stat) == nil {
		metrics.DiskTotalBytes = int64(stat.Blocks) * int64(stat.Bsize)
		metrics.DiskUsedBytes = int64(stat.Blocks-stat.Bavail) * int64(stat.Bsize)
	}
	return metrics
}

func parseProcMemory(raw string) (used, total int64, ok bool) {
	values := make(map[string]int64)
	for _, line := range strings.Split(raw, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		value, err := strconv.ParseInt(fields[1], 10, 64)
		if err == nil {
			values[strings.TrimSuffix(fields[0], ":")] = value * 1024
		}
	}
	total = values["MemTotal"]
	available, hasAvailable := values["MemAvailable"]
	if total <= 0 || !hasAvailable || available < 0 || available > total {
		return 0, 0, false
	}
	return total - available, total, true
}

func parseProcUptime(raw string) int64 {
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return 0
	}
	seconds, _ := strconv.ParseFloat(fields[0], 64)
	return int64(seconds)
}

func (h *hostService) lock(ctx context.Context) error {
	var commands [][]string
	if runtime.GOOS == "darwin" {
		commands = [][]string{{"/System/Library/CoreServices/Menu Extras/User.menu/Contents/Resources/CGSession", "-suspend"}, {"/usr/bin/pmset", "displaysleepnow"}}
	} else {
		commands = [][]string{{"loginctl", "lock-session"}, {"xdg-screensaver", "lock"}}
	}
	var last error
	for _, args := range commands {
		if err := exec.CommandContext(ctx, args[0], args[1:]...).Run(); err == nil {
			return nil
		} else {
			last = err
		}
	}
	return fmt.Errorf("screen lock failed: %w", last)
}

func (h *hostService) wake(mac, broadcast string) error {
	hardware, err := net.ParseMAC(mac)
	if err != nil || len(hardware) != 6 {
		return errors.New("invalid wake-on-LAN MAC address")
	}
	if strings.TrimSpace(broadcast) == "" {
		broadcast = "255.255.255.255"
	}
	address, err := net.ResolveUDPAddr("udp4", net.JoinHostPort(broadcast, "9"))
	if err != nil {
		return err
	}
	packet := append(bytes.Repeat([]byte{0xff}, 6), bytes.Repeat(hardware, 16)...)
	conn, err := net.DialUDP("udp4", nil, address)
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = conn.Write(packet)
	return err
}

func (h *hostService) exec(ctx context.Context, command string, timeout time.Duration) (ShellResult, error) {
	if h.shellEnabled == nil || !h.shellEnabled() {
		return ShellResult{}, errors.New("remote shell is disabled")
	}
	if timeout <= 0 || timeout > 10*time.Minute {
		timeout = 60 * time.Second
	}
	commandCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	cmd := exec.CommandContext(commandCtx, shell, "-lc", command)
	var stdout, stderr limitedBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if commandCtx.Err() == context.DeadlineExceeded {
		return ShellResult{}, errors.New("command timed out")
	}
	exitCode := 0
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			exitCode = exit.ExitCode()
		} else {
			return ShellResult{}, err
		}
	}
	return ShellResult{ExitCode: exitCode, Stdout: stdout.String(), Stderr: stderr.String(), Truncated: stdout.truncated || stderr.truncated}, nil
}

type ShellResult struct {
	ExitCode  int    `json:"exit_code"`
	Stdout    string `json:"stdout"`
	Stderr    string `json:"stderr"`
	Truncated bool   `json:"truncated"`
}
type limitedBuffer struct {
	bytes.Buffer
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	original := len(p)
	remaining := 256*1024 - b.Len()
	if remaining <= 0 {
		b.truncated = true
		return original, nil
	}
	if len(p) > remaining {
		p = p[:remaining]
		b.truncated = true
	}
	_, _ = b.Buffer.Write(p)
	return original, nil
}
