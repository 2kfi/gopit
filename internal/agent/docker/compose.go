package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
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
	case "docker.compose.validate":
		var req ComposeReq
		if err := json.Unmarshal(payload, &req); err != nil || req.Name == "" || req.Yaml == "" {
			return nil, errors.New("name and yaml required")
		}
		if !projectNameRe.MatchString(req.Name) {
			return nil, errors.New("invalid project name: use lowercase letters, digits, - and _")
		}
		return a.validate(req)
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
		composeMutex.Lock()
		defer composeMutex.Unlock()
		args := []string{"-p", req.Name}
		if projectNameRe.MatchString(req.Name) {
			if f, err := composeFilePath(req.Name); err == nil && f != "" {
				args = append(args, "-f", f)
			}
		}
		args = append(args, "down")
		out, err := runCompose(composeTimeout, args...)
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

// composeMutex serializes deploy/down so a concurrent deploy can't race the
// shared project file (A's up executing B's YAML). Compose ops are rare;
// per-project locks only if contention ever matters.
// ponytail: global lock, per-project locks if throughput matters.
var composeMutex sync.Mutex

// validate runs `docker compose config` on the candidate YAML and returns the
// parsed services. The config error (if any) is returned for the UI to show
// before a deploy is attempted.
func (a *API) validate(req ComposeReq) (any, error) {
	f, err := os.CreateTemp("", "gopit-compose-*.yaml")
	if err != nil {
		return nil, err
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(req.Yaml); err != nil {
		f.Close()
		return nil, err
	}
	f.Close()
	out, err := runCompose(composeTimeout, "-p", req.Name, "-f", f.Name(), "config", "--format", "json")
	if err != nil {
		return nil, err
	}
	cfg, err := parseComposeConfig(out)
	if err != nil {
		return nil, err
	}
	return cfg, nil
}

// ComposeConfig is the parsed result of `docker compose config --format json`.
type ComposeConfig struct {
	Services []ValidateService `json:"services"`
	Networks []string          `json:"networks"`
	Volumes  []string          `json:"volumes"`
}

// ValidateService is one parsed compose service from `config --format json`.
type ValidateService struct {
	Name  string   `json:"name"`
	Image string   `json:"image"`
	Ports []string `json:"ports"`
}

// parseComposeConfig parses `docker compose config --format json` into a
// stable service list plus the top-level network/volume names. Pure on the
// raw string so it is unit-testable.
func parseComposeConfig(raw string) (*ComposeConfig, error) {
	var cfg struct {
		Services map[string]struct {
			Image string   `json:"image"`
			Ports []string `json:"ports"`
		} `json:"services"`
		Networks map[string]json.RawMessage `json:"networks"`
		Volumes  map[string]json.RawMessage `json:"volumes"`
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return nil, fmt.Errorf("parse docker compose config output: %w", err)
	}
	names := make([]string, 0, len(cfg.Services))
	for name := range cfg.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	out := &ComposeConfig{}
	for _, name := range names {
		s := cfg.Services[name]
		out.Services = append(out.Services, ValidateService{Name: name, Image: s.Image, Ports: s.Ports})
	}
	for name := range cfg.Networks {
		out.Networks = append(out.Networks, name)
	}
	sort.Strings(out.Networks)
	for name := range cfg.Volumes {
		out.Volumes = append(out.Volumes, name)
	}
	sort.Strings(out.Volumes)
	return out, nil
}

// deploy writes the YAML to the persistent compose dir and brings the stack
// up. The file is kept after deploy so later `docker compose down` calls
// reference the same file and relative paths (build: ., volumes: ./data)
// resolve against a stable directory. A failed up leaves the file in place;
// the next deploy overwrites it.
func (a *API) deploy(req ComposeReq) (any, error) {
	composeMutex.Lock()
	defer composeMutex.Unlock()
	base, err := composeBaseDir()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(base, req.Name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	file := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(file, []byte(req.Yaml), 0o600); err != nil {
		return nil, err
	}
	out, err := runCompose(composeTimeout, "-p", req.Name, "-f", file, "up", "-d", "--remove-orphans")
	if err != nil {
		return nil, err
	}
	return map[string]string{"output": out}, nil
}

// composeDirCache caches the resolved base directory for the agent's lifetime.
var composeDirCache = struct {
	once sync.Once
	dir  string
	err  error
}{}

// composeBaseDir returns a persistent directory for compose files: prefer
// /var/lib/gopitd (system agents), fall back to the user's cache dir
// (rootless agents). The choice is fixed at first use.
func composeBaseDir() (string, error) {
	composeDirCache.once.Do(func() {
		if dir, err := makeComposeDir("/var/lib/gopitd/compose"); err == nil {
			composeDirCache.dir = dir
			return
		}
		cache, err := os.UserCacheDir()
		if err != nil {
			composeDirCache.err = err
			return
		}
		dir, err := makeComposeDir(filepath.Join(cache, "gopitd", "compose"))
		if err != nil {
			composeDirCache.err = err
			return
		}
		composeDirCache.dir = dir
	})
	return composeDirCache.dir, composeDirCache.err
}

func makeComposeDir(dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// composeFilePath returns the stored compose file for a project, or "" when
// the project has no stored file (e.g. it was brought up outside gopitd).
func composeFilePath(name string) (string, error) {
	base, err := composeBaseDir()
	if err != nil {
		return "", err
	}
	f := filepath.Join(base, name, "compose.yaml")
	if _, err := os.Stat(f); err != nil {
		return "", nil
	}
	return f, nil
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
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return []ComposeService{}, nil // project has no running containers
	}
	if err := json.Unmarshal([]byte(raw), &rawList); err != nil {
		// compose v5+ emits newline-delimited JSON (one object per line)
		// instead of an array; decode line-wise and tolerate trailing junk.
		rawList = nil
		dec := json.NewDecoder(strings.NewReader(raw))
		for {
			var s rawService
			if err := dec.Decode(&s); err != nil {
				break
			}
			rawList = append(rawList, s)
		}
		if rawList == nil {
			return nil, fmt.Errorf("parse docker compose ps output: %w", err)
		}
	}
	out := make([]ComposeService, 0, len(rawList))
	for _, s := range rawList {
		out = append(out, ComposeService{Name: s.Name, Service: s.Service, Image: s.Image, State: s.State, Health: s.Health, Status: s.Status, Ports: s.Ports, ExitCode: s.ExitCode})
	}
	return out, nil
}
