package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestValidName(t *testing.T) {
	valid := []string{"upf-loadtest", "a", "test-123"}
	invalid := []string{"", "UPF", "-test", "test-", "with_underscore"}
	for _, value := range valid {
		if !validName(value) {
			t.Errorf("expected %q to be valid", value)
		}
	}
	for _, value := range invalid {
		if validName(value) {
			t.Errorf("expected %q to be invalid", value)
		}
	}
}

func TestHealth(t *testing.T) {
	s := &server{staticDir: t.TempDir()}
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	response := httptest.NewRecorder()
	s.routes().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.Code)
	}
}

func TestConfigUsesConfiguredWorkloadChartVersion(t *testing.T) {
	s := &server{staticDir: t.TempDir(), chartVersion: "0.1.99"}
	request := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	response := httptest.NewRecorder()
	s.routes().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.Code)
	}
	if !strings.Contains(response.Body.String(), `"chartVersion":"0.1.99"`) {
		t.Fatalf("configured chart version missing from response: %s", response.Body.String())
	}
}

func TestStopJobArgsIgnoreMissingJobs(t *testing.T) {
	args := strings.Join(stopJobArgs("upf-loadtest", "trex-test"), " ")
	if !strings.Contains(args, "--ignore-not-found=true") {
		t.Fatalf("stop must be idempotent, got args: %s", args)
	}
}

func TestEnvBool(t *testing.T) {
	t.Setenv("TEST_BOOL", "false")
	if envBool("TEST_BOOL", true) {
		t.Fatal("expected false")
	}
	t.Setenv("TEST_BOOL", "yes")
	if !envBool("TEST_BOOL", false) {
		t.Fatal("expected true")
	}
}

func TestInstallCommandSetsMonitoring(t *testing.T) {
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(source), `"monitoring.enabled=" + strconv.FormatBool(monitoringEnabled)`) {
		t.Fatal("install command must explicitly set monitoring.enabled")
	}
}

func TestAuth(t *testing.T) {
	s := &server{staticDir: t.TempDir(), token: "secret"}
	request := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	response := httptest.NewRecorder()
	s.routes().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", response.Code)
	}

	request = httptest.NewRequest(http.MethodGet, "/api/config", nil)
	request.Header.Set("Authorization", "Bearer secret")
	response = httptest.NewRecorder()
	s.routes().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.Code)
	}
}

func TestFilterRunEvents(t *testing.T) {
	raw := map[string]interface{}{"items": []interface{}{
		map[string]interface{}{"involvedObject": map[string]interface{}{"name": "trex-123-abc"}},
		map[string]interface{}{"involvedObject": map[string]interface{}{"name": "another-pod"}},
	}}
	events := filterRunEvents(raw, "trex-123")
	if len(events) != 1 {
		t.Fatalf("expected one matching event, got %d", len(events))
	}
}

func TestFirstAddress(t *testing.T) {
	got, err := firstAddress("48.0.0.0/8")
	if err != nil || got != "48.0.0.1" {
		t.Fatalf("expected 48.0.0.1, got %q, %v", got, err)
	}
}

func TestParseUPFSessionCount(t *testing.T) {
	output := "Node: 10.0.4.1   Recovery Time Stamp: 2026/06/10 02:19:57:000 Sessions: 1000"
	if got := parseUPFSessionCount(output); got != 1000 {
		t.Fatalf("expected 1000 sessions, got %d", got)
	}
	if got := parseUPFSessionCount(""); got != 0 {
		t.Fatalf("expected no sessions, got %d", got)
	}
}

func TestFindActiveSessionState(t *testing.T) {
	pods := []byte(`{"items":[{"metadata":{"creationTimestamp":"2026-06-10T01:00:00Z"}}]}`)
	jobs := []byte(`{"items":[
		{"metadata":{"name":"old"},"spec":{"template":{"spec":{"containers":[{"env":[
			{"name":"PFCP_SERVICE","value":"upf-loadtest-travelping-upf-loadtest-pfcp-sim"},
			{"name":"SESSION_COUNT","value":"1"},{"name":"BASE_ID","value":"6001"},
			{"name":"UE_POOL","value":"48.0.0.0/8"},{"name":"QFI","value":"9"}
		]}]}}},"status":{"succeeded":1,"completionTime":"2026-06-10T00:59:00Z"}},
		{"metadata":{"name":"current","annotations":{
			"loadtest.infinitydon.io/release":"upf-loadtest",
			"loadtest.infinitydon.io/session-count":"1000",
			"loadtest.infinitydon.io/base-id":"1",
			"loadtest.infinitydon.io/ue-pool":"48.0.0.0/8",
			"loadtest.infinitydon.io/qfi":"9"
		}},"spec":{"template":{"spec":{"containers":[{"env":[]}]}}},
		"status":{"succeeded":1,"completionTime":"2026-06-10T01:01:00Z"}}
	]}`)

	state, err := findActiveSessionState(jobs, pods, "upf-loadtest")
	if err != nil {
		t.Fatal(err)
	}
	if !state.Available || state.RunName != "current" || state.Count != 1000 || state.BaseID != 1 || state.UEStart != "48.0.0.1" {
		t.Fatalf("unexpected state: %#v", state)
	}
}

func TestFindActiveSessionStateRejectsPreRestartJob(t *testing.T) {
	pods := []byte(`{"items":[{"metadata":{"creationTimestamp":"2026-06-10T02:00:00Z"}}]}`)
	jobs := []byte(`{"items":[{"metadata":{"name":"old"},"spec":{"template":{"spec":{"containers":[{"env":[
		{"name":"PFCP_SERVICE","value":"upf-loadtest-travelping-upf-loadtest-pfcp-sim"},
		{"name":"SESSION_COUNT","value":"1000"},{"name":"BASE_ID","value":"1"},{"name":"UE_POOL","value":"48.0.0.0/8"}
	]}]}}},"status":{"succeeded":1,"completionTime":"2026-06-10T01:00:00Z"}}]}`)

	state, err := findActiveSessionState(jobs, pods, "upf-loadtest")
	if err != nil {
		t.Fatal(err)
	}
	if state.Available {
		t.Fatalf("expected stale state to be unavailable: %#v", state)
	}
}

func TestTrafficScriptBoundsGeneratorPressure(t *testing.T) {
	required := []string{
		"STEP Draining residual RX traffic",
		"queue_budget=max(1000,int(expected*0.0001))",
		"wall_deadline=started+duration+max(5.0,duration*0.10)",
		"client.stop(ports=[0])",
		"unclassified_budget=max(100,int(tx*0.001))",
		`"generator_saturated":generator_saturated`,
	}
	for _, fragment := range required {
		if !strings.Contains(trafficScript, fragment) {
			t.Errorf("traffic script is missing %q", fragment)
		}
	}
}

func TestTrafficHandlerRestartsTRexServer(t *testing.T) {
	required := []string{
		`"rollout", "restart", "statefulset/" + statefulSet`,
		`"rollout", "status", "statefulset/" + statefulSet`,
	}
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range required {
		if !strings.Contains(string(source), fragment) {
			t.Errorf("traffic handler is missing %q", fragment)
		}
	}
}
