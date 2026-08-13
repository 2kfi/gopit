package ufw

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const cmdTimeout = 15 * time.Second

// API executes ufw commands via sudo for the agent's ufw.* WS methods.
type API struct {
	bin         string // absolute ufw binary path, e.g. /usr/sbin/ufw
	allowToggle bool   // permit ufw enable/disable (off by default)
}

// New builds the API. Construction never fails: availability is probed on the
// first call so a missing sudo/ufw degrades to a clean error, never a crash.
func New(bin string, allowToggle bool) *API {
	if bin == "" {
		bin = "/usr/sbin/ufw"
	}
	return &API{bin: bin, allowToggle: allowToggle}
}

// Request payloads (keys are the wire contract with the server).
type RuleAddReq struct {
	Protocol  string `json:"protocol"` // tcp | udp | both
	Port      int    `json:"port"`
	Action    string `json:"action"` // allow | deny | reject
	From      string `json:"from"`   // default "any"
	Interface string `json:"interface"`
}

type RuleDeleteReq struct {
	Number int `json:"number"` // ufw rule number from status numbered
}

type ToggleReq struct {
	Enabled bool `json:"enabled"`
}

// Call executes one ufw.* method with the raw request payload and returns the
// response payload (already JSON-marshalable).
func (a *API) Call(method string, payload json.RawMessage) (any, error) {
	switch method {
	case "ufw.status":
		return a.Status()
	case "ufw.rule.add":
		var req RuleAddReq
		if err := json.Unmarshal(payload, &req); err != nil ||
			req.Port < 1 || req.Port > 65535 || !oneOf(req.Protocol, "tcp", "udp", "both") || !oneOf(req.Action, "allow", "deny", "reject") {
			return nil, errors.New("protocol (tcp|udp|both), port (1-65535) and action (allow|deny|reject) required")
		}
		return map[string]string{"status": "ok"}, a.addRule(req)
	case "ufw.rule.delete":
		var req RuleDeleteReq
		if err := json.Unmarshal(payload, &req); err != nil || req.Number < 1 {
			return nil, errors.New("number required")
		}
		return map[string]string{"status": "ok"}, a.delete(req.Number)
	case "ufw.toggle":
		if !a.allowToggle {
			return nil, errors.New("toggle disabled by config")
		}
		var req ToggleReq
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, errors.New("enabled required")
		}
		arg := "disable"
		if req.Enabled {
			arg = "enable"
		}
		_, err := a.run(arg)
		return map[string]string{"status": "ok"}, err
	default:
		return nil, fmt.Errorf("unknown ufw method: %s", method)
	}
}

// Status returns the firewall state, default policies and numbered rule table.
// Any failure of `ufw status` reads as unavailability so a broken install
// degrades cleanly.
func (a *API) Status() (*Status, error) {
	verbose, err := a.run("status", "verbose")
	if err != nil {
		return nil, notAvail(err)
	}
	numbered, err := a.run("status", "numbered")
	if err != nil {
		return nil, notAvail(err)
	}
	st := &Status{}
	st.Enabled, st.DefaultIn, st.DefaultOut = parseVerbose(verbose)
	st.Rules = parseNumbered(numbered)
	return st, nil
}

func (a *API) delete(number int) error {
	_, err := a.run("delete", strconv.Itoa(number))
	return err
}

func (a *API) addRule(req RuleAddReq) error {
	args := []string{req.Action}
	if req.Interface != "" {
		args = append(args, "in", "on", req.Interface)
	}
	if req.From != "" && !oneOf(req.From, "any", "Anywhere") {
		args = append(args, "from", req.From)
	}
	args = append(args, "to", "any", "port", strconv.Itoa(req.Port))
	if req.Protocol != "both" {
		args = append(args, "proto", req.Protocol)
	}
	_, err := a.run(args...)
	return err
}

// run executes one ufw command. sudo is run with -n (NOPASSWD or fail), stdin
// closed and SUDO_ASKPASS stripped so the agent never hangs on a prompt;
// failures return sudo/ufw's stderr. A missing sudo or ufw reads as
// unavailability.
func (a *API) run(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sudo", append([]string{"-n", a.bin}, args...)...)
	cmd.Env = envWithoutAskpass()
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		if errors.Is(err, exec.ErrNotFound) || strings.Contains(msg, "command not found") {
			return "", fmt.Errorf("ufw not available: %s", msg)
		}
		return "", errors.New(msg)
	}
	return strings.TrimSpace(string(out)), nil
}

func notAvail(err error) error {
	if strings.HasPrefix(err.Error(), "ufw not available:") {
		return err
	}
	return fmt.Errorf("ufw not available: %s", err)
}

func envWithoutAskpass() []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "SUDO_ASKPASS=") {
			env = append(env, kv)
		}
	}
	return env
}

func oneOf(s string, opts ...string) bool {
	for _, o := range opts {
		if s == o {
			return true
		}
	}
	return false
}
