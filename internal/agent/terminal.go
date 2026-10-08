package agent

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/dropfile/HankServerside/internal/protocol"
)

const terminalReplayLimit = 512 * 1024
const terminalExitRetention = 5 * time.Minute

type terminalEventSink func(context.Context, string, string, any) error

type terminalSession struct {
	mu       sync.Mutex
	id       string
	cmd      *exec.Cmd
	pty      *os.File
	replay   []byte
	base     uint64
	cursor   uint64
	exited   bool
	exitCode *int
}

type terminalManager struct {
	mu       sync.Mutex
	enabled  bool
	sessions map[string]*terminalSession
	sink     terminalEventSink
}

func (m *terminalManager) setEventSink(sink terminalEventSink) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sink = sink
}

func (m *terminalManager) emit(event, topic string, payload any) {
	m.mu.Lock()
	sink := m.sink
	m.mu.Unlock()
	if sink != nil {
		_ = sink(context.Background(), event, topic, payload)
	}
}

func newTerminalManager(enabled bool, sink terminalEventSink) *terminalManager {
	return &terminalManager{enabled: enabled, sessions: make(map[string]*terminalSession), sink: sink}
}

func (m *terminalManager) setEnabled(enabled bool) {
	m.mu.Lock()
	m.enabled = enabled
	m.mu.Unlock()
	if !enabled {
		m.closeAll()
	}
}

func (m *terminalManager) open(ctx context.Context, request protocol.ShellSessionOpenRequest) (protocol.ShellSessionOpenResponse, error) {
	if err := request.Validate(); err != nil {
		return protocol.ShellSessionOpenResponse{}, err
	}
	m.mu.Lock()
	if !m.enabled {
		m.mu.Unlock()
		return protocol.ShellSessionOpenResponse{}, errors.New("remote shell is disabled")
	}
	if _, exists := m.sessions[request.SessionID]; exists {
		m.mu.Unlock()
		return protocol.ShellSessionOpenResponse{}, errors.New("terminal session already exists")
	}
	if len(m.sessions) >= 8 {
		m.mu.Unlock()
		return protocol.ShellSessionOpenResponse{}, errors.New("terminal session limit reached")
	}

	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	cmd := exec.Command(shell, "-l")
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	file, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: request.Columns, Rows: request.Rows})
	if err != nil {
		m.mu.Unlock()
		return protocol.ShellSessionOpenResponse{}, err
	}
	session := &terminalSession{id: request.SessionID, cmd: cmd, pty: file}
	m.sessions[request.SessionID] = session
	m.mu.Unlock()
	go m.readLoop(session)
	return protocol.ShellSessionOpenResponse{SessionID: request.SessionID, Shell: shell}, nil
}

func (m *terminalManager) readLoop(session *terminalSession) {
	buffer := make([]byte, 32*1024)
	for {
		n, err := session.pty.Read(buffer)
		if n > 0 {
			chunk := append([]byte(nil), buffer[:n]...)
			session.mu.Lock()
			session.replay = append(session.replay, chunk...)
			session.cursor += uint64(n)
			cursor := session.cursor
			if len(session.replay) > terminalReplayLimit {
				drop := len(session.replay) - terminalReplayLimit
				session.replay = append([]byte(nil), session.replay[drop:]...)
				session.base += uint64(drop)
			}
			session.mu.Unlock()
			m.emit("shell.session.output", protocol.ShellSessionTopic(session.id), protocol.ShellSessionOutput{SessionID: session.id, Cursor: cursor, Data: string(chunk)})
		}
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, os.ErrClosed) { /* PTYs commonly return EIO at exit. */
			}
			break
		}
	}
	err := session.cmd.Wait()
	code := 0
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			code = exit.ExitCode()
		} else {
			code = -1
		}
	}
	session.mu.Lock()
	session.exited = true
	session.exitCode = &code
	cursor := session.cursor
	session.mu.Unlock()
	m.emit("shell.session.exited", protocol.ShellSessionTopic(session.id), protocol.ShellSessionExited{SessionID: session.id, Cursor: cursor, ExitCode: &code, Reason: "process_exited"})
	time.AfterFunc(terminalExitRetention, func() { m.removeExited(session) })
}

func (m *terminalManager) removeExited(session *terminalSession) {
	m.mu.Lock()
	current, ok := m.sessions[session.id]
	if ok && current == session {
		delete(m.sessions, session.id)
	}
	m.mu.Unlock()
	if ok && current == session {
		_ = session.pty.Close()
	}
}

func (m *terminalManager) input(request protocol.ShellSessionInputRequest) error {
	if err := request.Validate(); err != nil {
		return err
	}
	session, err := m.session(request.SessionID)
	if err != nil {
		return err
	}
	_, err = io.WriteString(session.pty, request.Data)
	return err
}

func (m *terminalManager) resize(request protocol.ShellSessionResizeRequest) error {
	if err := request.Validate(); err != nil {
		return err
	}
	session, err := m.session(request.SessionID)
	if err != nil {
		return err
	}
	return pty.Setsize(session.pty, &pty.Winsize{Cols: request.Columns, Rows: request.Rows})
}

func (m *terminalManager) attach(request protocol.ShellSessionAttachRequest) (protocol.ShellSessionAttachResponse, error) {
	if err := request.Validate(); err != nil {
		return protocol.ShellSessionAttachResponse{}, err
	}
	session, err := m.session(request.SessionID)
	if err != nil {
		return protocol.ShellSessionAttachResponse{}, err
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	start := request.AfterCursor
	if start < session.base {
		start = session.base
	}
	if start > session.cursor {
		start = session.cursor
	}
	return protocol.ShellSessionAttachResponse{SessionID: session.id, Cursor: session.cursor, Output: string(session.replay[start-session.base:]), Exited: session.exited, ExitCode: session.exitCode}, nil
}

func (m *terminalManager) close(request protocol.ShellSessionCloseRequest) error {
	if err := request.Validate(); err != nil {
		return err
	}
	m.mu.Lock()
	session, ok := m.sessions[request.SessionID]
	if ok {
		delete(m.sessions, request.SessionID)
	}
	m.mu.Unlock()
	if !ok {
		return errors.New("terminal session not found")
	}
	_ = session.pty.Close()
	if session.cmd.Process != nil {
		_ = syscall.Kill(-session.cmd.Process.Pid, syscall.SIGTERM)
		_ = session.cmd.Process.Kill()
	}
	return nil
}

func (m *terminalManager) session(id string) (*terminalSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	session, ok := m.sessions[id]
	if !ok {
		return nil, errors.New("terminal session not found")
	}
	return session, nil
}

func (m *terminalManager) closeAll() {
	m.mu.Lock()
	ids := make([]string, 0, len(m.sessions))
	for id := range m.sessions {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		_ = m.close(protocol.ShellSessionCloseRequest{SessionID: id})
	}
}
