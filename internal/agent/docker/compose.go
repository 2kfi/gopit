package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// Compose project/service payloads.
type ComposeProject struct {
	Name        string `json:"name"`
	Status      string `json:"status"`
	ConfigFiles string `json:"config_files"`
}

type ComposeService struct {
	Name     string `json:"name"`
	Service  string `json:"service"`
	Image    string `json:"image"`
	State    string `json:"state"`
	Health   string `json:"health"`
	Status   string `json:"status"`
	Ports    string `json:"ports"`
	ExitCode int    `json:"exit_code"`
}

type ComposeReq struct {
	Name string `json:"name"`
	Yaml string `json:"yaml,omitempty"`
}

var projectNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// composeTimeout bounds each compose invocation (deploys can legitimately take
// a while, so callers pass their own deadline via the composeCommand arg).
const composeTimeout = 120 * time.Second

// Call handles the docker.compose.* methods.
func (a *API) ComposeCall(method string, payload json.RawMessage) (any, error) {
	switch method {
	case "docker.compose.list":
		out, err := runCompose(composeTimeout, "ls")
		if err != nil {
			return nil, err
		}
		return parseComposeLS(out), nil
	case "docker.compose.deploy":
		var req ComposeReq
		if err := json.Unmarshal(payload, &req); err != nil || req.Name == "" || req.Yaml == "" {
			return nil, errors.New("name and yaml required")
		}
		if !projectNameRe.MatchString(req.Name) {
			return nil, errors.New("invalid project name: use lowercase letters, digits, - and _")
		}
		return a.deploy(req)
	case "docker.compose.down":
		var req ComposeReq
		if err := json.Unmarshal(payload, &req); err != nil || req.Name == "" {
			return nil, errors.New("name required")
		}
		out, err := runCompose(composeTimeout, "-p", req.Name, "down")
		if err != nil {
			return nil, err
		}
		return map[string]string{"output": out}, nil
	case "docker.compose.ps":
		var req ComposeReq
		if err := json.Unmarshal(payload, &req); err != nil || req.Name == "" {
			return nil, errors.New("name required")
		}
		out, err := runCompose(composeTimeout, "-p", req.Name, "ps", "--format", "json")
		if err != nil {
			return nil, err
		}
		return parseComposePS(out)
	default:
		return nil, fmt.Errorf("unknown docker method: %s", method)
	}
}

// deploy writes the YAML to a temp dir and brings the stack up.
func (a *API) deploy(req ComposeReq) (any, error) {
	dir, err := os.MkdirTemp("", "gopit-compose-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	file := dir + "/compose.yaml"
	if err := os.WriteFile(file, []byte(req.Yaml), 0o600); err != nil {
		return nil, err
	}
	out, err := runCompose(composeTimeout, "-p", req.Name, "-f", file, "up", "-d")
	if err != nil {
		return nil, err
	}
	return map[string]string{"output": out}, nil
}

// runCompose shells out to `docker compose`. Errors carry the CLI's output so
// the server can surface them; a missing docker/compose gets a clear message.
func runCompose(timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", append([]string{"compose"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return "", errors.New("docker not found: install docker with the compose plugin")
		}
		msg := strings.TrimSpace(string(out))
		if strings.Contains(msg, "'compose' is not a docker command") {
			return "", errors.New("docker compose plugin not found: install docker-compose-plugin")
		}
		if msg == "" {
			return "", err
		}
		return "", fmt.Errorf("%s: %s", err, msg)
	}
	return string(out), nil
}

// parseComposeLS parses `docker compose ls` table output (NAME STATUS CONFIG FILES).
// Pure on the raw string so it is unit-testable.
func parseComposeLS(raw string) []ComposeProject {
	var out []ComposeProject
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "NAME") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		out = append(out, ComposeProject{Name: f[0], Status: f[1], ConfigFiles: strings.Join(f[2:], " ")})
	}
	if out == nil {
		out = []ComposeProject{}
	}
	return out
}

// parseComposePS parses `docker compose -p <name> ps --format json`.
// ponytail: the table format's COMMAND/CREATED/PORTS columns contain spaces and
// cannot be split reliably; JSON is the compose-native structured output.
func parseComposePS(raw string) ([]ComposeService, error) {
	type rawService struct {
		Name     string `json:"Name"`
		Service  string `json:"Service"`
		Image    string `json:"Image"`
		State    string `json:"State"`
		Health   string `json:"Health"`
		Status   string `json:"Status"`
		Ports    string `json:"Ports"`
		ExitCode int    `json:"ExitCode"`
	}
	var rawList []rawService
	if err := json.Unmarshal([]byte(raw), &rawList); err != nil {
		return nil, fmt.Errorf("parse docker compose ps output: %w", err)
	}
	out := make([]ComposeService, 0, len(rawList))
	for _, s := range rawList {
		out = append(out, ComposeService{Name: s.Name, Service: s.Service, Image: s.Image, State: s.State, Health: s.Health, Status: s.Status, Ports: s.Ports, ExitCode: s.ExitCode})
	}
	return out, nil
}
