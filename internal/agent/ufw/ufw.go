package ufw

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

const cmdTimeout = 15 * time.Second

// API executes ufw commands via sudo for the agent's ufw.* WS methods.
// A single mutex serializes all calls: ufw has no transactional interface, so
// concurrent mutations would race rule numbering (delete-by-number TOCTOU).
// ufw ops are rare; contention is a non-issue.
// ponytail: global lock, per-call locks if throughput matters.
type API struct {
	bin         string // absolute ufw binary path, e.g. /usr/sbin/ufw
	allowToggle bool   // permit ufw enable/disable (off by default)
	mu          sync.Mutex
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
	a.mu.Lock()
	defer a.mu.Unlock()
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
		// --force: modern ufw prompts "Proceed with operation?" and aborts
		// when stdin is closed, so the toggle would silently never apply.
		_, err := a.run("--force", arg)
		return map[string]string{"status": "ok"}, err
	case "ufw.rule.preview":
		var req RuleAddReq
		if err := json.Unmarshal(payload, &req); err != nil ||
			req.Port < 1 || req.Port > 65535 || !oneOf(req.Protocol, "tcp", "udp", "both") || !oneOf(req.Action, "allow", "deny", "reject") {
			return nil, errors.New("protocol (tcp|udp|both), port (1-65535) and action (allow|deny|reject) required")
		}
		return a.preview(req)
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
	_, err := a.run("--force", "delete", strconv.Itoa(number))
	return err
}

// preview returns the current status with the new rule simulated. ufw has no
// dry-run, so the simulated entry is appended as the next number; the real
// numbering may differ slightly (ufw expands tcp+udp "both" into two rows),
// which is fine for a preview.
func (a *API) preview(req RuleAddReq) (*Status, error) {
	st, err := a.Status()
	if err != nil {
		return nil, err
	}
	if req.From != "" && !oneOf(req.From, "any", "Anywhere") {
		if _, _, err := net.ParseCIDR(req.From); err != nil {
			if net.ParseIP(req.From) == nil {
				return nil, errors.New("invalid 'from' address")
			}
		}
	}
	to := strconv.Itoa(req.Port)
	if req.Protocol != "both" {
		to += "/" + req.Protocol
	}
	from := req.From
	if from == "" || oneOf(from, "any", "Anywhere") {
		from = "Anywhere"
	}
	st.Rules = append(st.Rules, Rule{
		Number:    len(st.Rules) + 1,
		To:        to,
		Action:    strings.ToUpper(req.Action),
		From:      from,
		Direction: "IN",
		Interface: req.Interface,
	})
	return st, nil
}

func (a *API) addRule(req RuleAddReq) error {
	args := []string{req.Action}
	if req.Interface != "" {
		if len(req.Interface) > 15 || strings.ContainsAny(req.Interface, " \t") || strings.HasPrefix(req.Interface, "-") {
			return errors.New("invalid 'interface' name")
		}
		args = append(args, "in", "on", req.Interface)
	}
	if req.From != "" && !oneOf(req.From, "any", "Anywhere") {
		// validated so a leading '-' can't be parsed as a ufw option
		if _, _, err := net.ParseCIDR(req.From); err != nil {
			if net.ParseIP(req.From) == nil {
				return errors.New("invalid 'from' address")
			}
		}
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
	cmd.Env = append(envWithoutAskpass(), "LC_ALL=C") // localized ufw output must not break the parser
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
