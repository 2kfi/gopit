// Package terminal provides password-validated PTY shells for the agent.
//
// Security contract: the shell is never spawned as root ("root"/uid 0 is
// rejected before anything spawns), and the password is only ever fed to a
// PTY master -- never piped through stdin, because su reads /dev/tty.
package terminal

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
)

// Validation errors surfaced to the browser as-is (distinct messages).
var (
	ErrRootUser    = errors.New("root is not allowed")
	ErrInvalidUser = errors.New("invalid user")
	ErrAuthFailed  = errors.New("authentication failed")
	ErrSuFailed    = errors.New("login validation failed")
	ErrAgentRoot   = errors.New("agent runs as root; password validation is impossible")
)

// suPath and getentPath are absolute so a compromised PATH cannot substitute
// a fake su when validating credentials.
const (
	suPath     = "/usr/bin/su"
	getentPath = "/usr/bin/getent"
)

const (
	validationTimeout = 5 * time.Second // whole su -c validation, worst case
	handshakeTimeout  = 2 * time.Second // session: wait for a password prompt
	postFeedGrace     = 1500 * time.Millisecond
	readBuf           = 4096
)

// Session is one live PTY shell (su -l <user>).
type Session struct {
	ptmx     *os.File
	cmd      *exec.Cmd
	exited   chan struct{}
	waitOnce sync.Once
	rec      *Recorder // optional ttyrec recording; nil = not recording
}

// Recorder writes the pty output stream as a ttyrec file: one record per
// write, each a 12-byte little-endian header (seconds, microseconds, length)
// followed by the raw bytes — the format ttyplay(1) reads. A recorder is
// created once per session and closed with it.
type Recorder struct {
	f  *os.File
	mu sync.Mutex
}

// NewRecorder creates the per-session file
// <dir>/<nodeID>/<UTC timestamp>.ttyrec. Callers pass the agent's node ID so
// files group by node; the agent logs a warning and continues unrecorded
// when creation fails (e.g. a read-only dir on a rootless agent).
func NewRecorder(dir, nodeID string) (*Recorder, error) {
	d := filepath.Join(dir, nodeID)
	if err := os.MkdirAll(d, 0o750); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(d, time.Now().UTC().Format("20060102T150405Z")+".ttyrec"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return nil, err
	}
	return &Recorder{f: f}, nil
}

// Write appends one timed record: header + payload.
func (r *Recorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	var hdr [12]byte
	binary.LittleEndian.PutUint32(hdr[0:4], uint32(now.Unix()))
	binary.LittleEndian.PutUint32(hdr[4:8], uint32(now.Nanosecond()/1000))
	binary.LittleEndian.PutUint32(hdr[8:12], uint32(len(p)))
	if _, err := r.f.Write(hdr[:]); err != nil {
		return 0, err
	}
	return r.f.Write(p)
}

// Close flushes and closes the recording file.
func (r *Recorder) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.f.Close()
}

// Open validates the password via su in a PTY, then spawns the real login
// shell in a fresh PTY. cols/rows are clamped to sane bounds.
//
// ponytail: the validation pty exists because su reads its prompt from the
// controlling tty; there is no way to feed a password through stdin, so we
// spawn su inside a pty and answer the prompt there.
func Open(user, password string, cols, rows int, rec *Recorder) (*Session, error) {
	if os.Geteuid() == 0 {
		// su never prompts for a password when run as root, so the
		// validation below would accept anything — refuse instead.
		return nil, ErrAgentRoot
	}
	uid, shell, err := lookupUser(user)
	if err != nil {
		return nil, ErrInvalidUser
	}
	if user == "root" || uid == "0" {
		return nil, ErrRootUser
	}
	if err := validate(user, password, shell); err != nil {
		return nil, err
	}
	s, err := spawn(user, shell, cols, rows)
	if err != nil {
		return nil, err
	}
	s.rec = rec
	if err := s.handshake(password); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

// lookupUser resolves a user via getent (one source of truth for existence,
// uid and login shell; getent predates and outlives any libc NSS quirks).
func lookupUser(user string) (uid, shell string, err error) {
	out, err := exec.Command(getentPath, "passwd", user).Output()
	if err != nil {
		return "", "", err
	}
	f := strings.Split(strings.TrimSpace(string(out)), ":")
	if len(f) < 7 {
		return "", "", errors.New("malformed passwd entry")
	}
	shell = f[6]
	if shell == "" {
		shell = "/bin/sh"
	}
	return f[2], shell, nil
}

// validate answers su's password prompt and checks the exit status.
func validate(user, password, shell string) error {
	cmd := exec.Command(suPath, "-l", user, "-c", "echo ok")
	cmd.Env = []string{"LC_ALL=C", "TERM=xterm-256color", "SHELL=" + shell}
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 80, Rows: 24})
	if err != nil {
		return errors.New("failed to start su: " + err.Error())
	}
	defer ptmx.Close()

	var out bytes.Buffer
	buf := make([]byte, readBuf)
	fed, authFail := false, false
	deadline := time.Now().Add(validationTimeout)
	for {
		ptmx.SetReadDeadline(deadline)
		n, err := ptmx.Read(buf)
		if n > 0 {
			out.Write(buf[:n])
		}
		text := out.String()
		// Prompt first, answer it. If su reports a failure *after* the feed
		// (bad password, 3-retry loop), kill it and bail early.
		if !fed && strings.Contains(text, "Password:") {
			fed = true
			ptmx.Write([]byte(password + "\n"))
			deadline = time.Now().Add(postFeedGrace)
		} else if isAuthFailure(text) {
			authFail = true
			cmd.Process.Kill()
			break
		}
		if err != nil {
			break // EOF (su exited), read timeout, or the kill above
		}
	}
	if cmd.ProcessState == nil {
		// We gave up (no prompt in time, su blocked on /dev/tty): if the
		// process is still alive, Wait would hang on it forever — kill it.
		cmd.Process.Kill()
	}
	cmd.Wait() // su's pty is closed; the process is dead or about to be
	return classifySu(fed, authFail, cmd.ProcessState.ExitCode(), out.String())
}

// spawn starts su -l <user> in a fresh pty. This su must not prompt again
// after Open validated the password; see handshake.
func spawn(user, shell string, cols, rows int) (*Session, error) {
	if user == "root" { // belt and braces: never hand a root shell to a caller
		return nil, ErrRootUser
	}
	cmd := exec.Command(suPath, "-l", user)
	cmd.Env = []string{"LC_ALL=C", "TERM=xterm-256color", "SHELL=" + shell}
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: clampSize(cols, 80), Rows: clampSize(rows, 24)})
	if err != nil {
		return nil, errors.New("failed to start shell: " + err.Error())
	}
	s := &Session{ptmx: ptmx, cmd: cmd, exited: make(chan struct{})}
	go func() { // reaps the child exactly once; no zombies
		s.cmd.Wait()
		s.waitOnce.Do(func() { close(s.exited) })
	}()
	return s, nil
}

// handshake gives su a short window to re-prompt. The password was already
// validated, so a second prompt means the environment re-authenticates
// anyway -- answer it once (non-root agents, the dev case), and fail if su
// still refuses.
func (s *Session) handshake(password string) error {
	deadline := time.Now().Add(handshakeTimeout)
	buf := make([]byte, readBuf)
	var out bytes.Buffer
	fed := false
	for {
		s.ptmx.SetReadDeadline(deadline)
		n, err := s.ptmx.Read(buf)
		if n > 0 {
			out.Write(buf[:n])
		}
		text := out.String()
		if !fed && strings.Contains(text, "Password:") {
			fed = true
			s.ptmx.Write([]byte(password + "\n"))
			deadline = time.Now().Add(postFeedGrace)
		} else if isAuthFailure(text) {
			return ErrAuthFailed
		}
		if err != nil {
			break // EOF or the deadline; no prompt in time = session ready
		}
	}
	s.ptmx.SetReadDeadline(time.Time{})
	return nil
}

// classifySu maps su's failure output to a typed error.
func classifySu(prompted, authFail bool, code int, out string) error {
	if code == 0 {
		return nil
	}
	switch {
	case authFail, isAuthFailure(out):
		return ErrAuthFailed
	case strings.Contains(out, "does not exist"),
		strings.Contains(out, "unknown user"),
		strings.Contains(out, "Unknown id"),
		strings.Contains(out, "no passwd entry"):
		return ErrInvalidUser
	default:
		msg := strings.TrimSpace(out)
		if i := strings.IndexByte(msg, '\n'); i >= 0 {
			msg = msg[:i]
		}
		if msg == "" {
			msg = "su exited " + strconv.Itoa(code)
		}
		return fmt.Errorf("%w: %s", ErrSuFailed, msg)
	}
}

// isAuthFailure reports whether su text indicates a rejected credential.
func isAuthFailure(text string) bool {
	t := strings.ToLower(text)
	for _, s := range []string{"authentication failure", "incorrect password", "permission denied", "too many authentication failures"} {
		if strings.Contains(t, s) {
			return true
		}
	}
	return false
}

// Resize updates the pty window size.
func (s *Session) Resize(cols, rows int) error {
	if cols < 1 {
		cols = 80
	}
	if rows < 1 {
		rows = 24
	}
	return pty.Setsize(s.ptmx, &pty.Winsize{Cols: clampSize(cols, 80), Rows: clampSize(rows, 24)})
}

// Read/Write forward to the pty master. Read returns after Close with an
// error, which ends the output pump. Read tees output into the recorder
// (with timestamps) when recording is enabled.
func (s *Session) Read(p []byte) (int, error) {
	n, err := s.ptmx.Read(p)
	if n > 0 && s.rec != nil {
		s.rec.Write(p[:n])
	}
	return n, err
}
func (s *Session) Write(p []byte) (int, error) { return s.ptmx.Write(p) }

// Exited closes when the spawned process has been reaped.
func (s *Session) Exited() <-chan struct{} { return s.exited }

// Close tears the session down: closing the master sends SIGHUP to the
// foreground process group (the shell and its children), SIGKILL finishes
// su itself, and the reaper goroutine collects it.
func (s *Session) Close() error {
	s.ptmx.Close()
	if s.rec != nil {
		s.rec.Close()
	}
	if s.cmd.Process != nil {
		s.cmd.Process.Kill()
	}
	return nil
}

func clampSize(v, def int) uint16 {
	if v < 1 {
		v = def
	}
	if v > 500 {
		v = 500
	}
	return uint16(v)
}
