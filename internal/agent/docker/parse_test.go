package docker

import (
	"reflect"
	"testing"
)

func TestParseComposeLS(t *testing.T) {
	raw := `NAME                STATUS              CONFIG FILES
gopit-smoke        running(2)          /tmp/opencode/smoke/compose.yaml
webstack            exited(0)           /home/user/web/compose.yml

`
	got := parseComposeLS(raw)
	want := []ComposeProject{
		{Name: "gopit-smoke", Status: "running(2)", ConfigFiles: "/tmp/opencode/smoke/compose.yaml"},
		{Name: "webstack", Status: "exited(0)", ConfigFiles: "/home/user/web/compose.yml"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseComposeLS mismatch:\n got %+v\nwant %+v", got, want)
	}

	if empty := parseComposeLS(""); len(empty) != 0 {
		t.Errorf("empty input should yield empty list, got %+v", empty)
	}
}

func TestParseComposeConfig(t *testing.T) {
	raw := `{"name":"webstack","services":{"web":{"image":"nginx:alpine","ports":["8080:80/tcp"]},"db":{"image":"postgres:16"}},"networks":{"webnet":{"driver":"bridge"}},"volumes":{"pgdata":{"driver":"local"}}}`
	got, err := parseComposeConfig(raw)
	if err != nil {
		t.Fatalf("parseComposeConfig error: %v", err)
	}
	want := &ComposeConfig{
		Services: []ValidateService{
			{Name: "db", Image: "postgres:16"},
			{Name: "web", Image: "nginx:alpine", Ports: []string{"8080:80/tcp"}},
		},
		Networks: []string{"webnet"},
		Volumes:  []string{"pgdata"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseComposeConfig mismatch:\n got %+v\nwant %+v", got, want)
	}

	if _, err := parseComposeConfig("not json"); err == nil {
		t.Error("invalid JSON should yield an error")
	}
}

func TestParseComposePS(t *testing.T) {
	raw := `[{"ID":"abc123","Name":"webstack-web-1","Project":"webstack","Service":"web","State":"running","Health":"","ExitCode":0,"Image":"nginx:alpine","Status":"Up 2 minutes","Ports":"0.0.0.0:8080->80/tcp"}]`
	got, err := parseComposePS(raw)
	if err != nil {
		t.Fatal(err)
	}
	want := []ComposeService{
		{Name: "webstack-web-1", Service: "web", Image: "nginx:alpine", State: "running", Status: "Up 2 minutes", Ports: "0.0.0.0:8080->80/tcp"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseComposePS mismatch:\n got %+v\nwant %+v", got, want)
	}

	if _, err := parseComposePS("not json"); err == nil {
		t.Error("expected error for non-JSON input")
	}
}
