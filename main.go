package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	defaultChart        = "oci://ghcr.io/infinitydon/travelping-upf-loadtest"
	defaultChartVersion = "0.1.13"
	managedByLabel      = "upf-loadtest-webui"
)

var dnsLabel = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
var upfSessionCount = regexp.MustCompile(`Sessions:\s+([0-9]+)`)

type server struct {
	staticDir string
	token     string
}

type releaseRequest struct {
	Release      string `json:"release"`
	Namespace    string `json:"namespace"`
	ChartVersion string `json:"chartVersion"`
	TargetNode   string `json:"targetNode"`
}

type sessionRequest struct {
	Release   string `json:"release"`
	Namespace string `json:"namespace"`
	Count     int    `json:"count"`
	BaseID    int    `json:"baseId"`
	UEPool    string `json:"uePool"`
	QFI       int    `json:"qfi"`
	GNBAddr   string `json:"gnbAddr"`
	UPFN3Addr string `json:"upfN3Addr"`
	UPFAddr   string `json:"upfAddr"`
}

type trafficRequest struct {
	Release    string  `json:"release"`
	Namespace  string  `json:"namespace"`
	PPS        int     `json:"pps"`
	Duration   int     `json:"duration"`
	PacketSize int     `json:"packetSize"`
	SessionCnt int     `json:"sessionCount"`
	TEIDStart  int     `json:"teidStart"`
	TEIDStep   int     `json:"teidStep"`
	UEStart    string  `json:"ueStart"`
	InnerDst   string  `json:"innerDst"`
	MaxLoss    float64 `json:"maxLossPercent"`
}

type sessionState struct {
	Available   bool   `json:"available"`
	RunName     string `json:"runName,omitempty"`
	Count       int    `json:"count,omitempty"`
	BaseID      int    `json:"baseId,omitempty"`
	UEPool      string `json:"uePool,omitempty"`
	UEStart     string `json:"ueStart,omitempty"`
	QFI         int    `json:"qfi,omitempty"`
	CompletedAt string `json:"completedAt,omitempty"`
	Unavailable string `json:"unavailableReason,omitempty"`
}

type deleteRequest struct {
	Release   string `json:"release"`
	Namespace string `json:"namespace"`
}

func main() {
	addr := env("LISTEN_ADDR", ":8080")
	s := &server{staticDir: env("STATIC_DIR", "/app/static"), token: os.Getenv("AUTH_TOKEN")}
	log.Printf("listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, s.routes()))
}

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("/api/config", s.auth(s.config))
	mux.HandleFunc("/api/status", s.auth(s.status))
	mux.HandleFunc("/api/release/install", s.auth(s.install))
	mux.HandleFunc("/api/release", s.auth(s.uninstall))
	mux.HandleFunc("/api/runs", s.auth(s.runs))
	mux.HandleFunc("/api/runs/detail", s.auth(s.runDetail))
	mux.HandleFunc("/api/runs/logs", s.auth(s.logs))
	mux.HandleFunc("/api/runs/stop", s.auth(s.stop))
	mux.HandleFunc("/api/session-state", s.auth(s.sessionState))
	mux.HandleFunc("/api/sessions", s.auth(s.sessions))
	mux.HandleFunc("/api/traffic", s.auth(s.traffic))
	mux.HandleFunc("/", s.static)
	return requestLog(mux)
}

func (s *server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.token != "" && r.Header.Get("Authorization") != "Bearer "+s.token {
			writeError(w, http.StatusUnauthorized, errors.New("unauthorized"))
			return
		}
		next(w, r)
	}
}

func (s *server) config(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]interface{}{
		"chart": defaultChart, "chartVersion": defaultChartVersion,
		"defaultRelease":        env("DEFAULT_RELEASE", "upf-loadtest"),
		"defaultNamespace":      env("DEFAULT_NAMESPACE", "upf-loadtest"),
		"authenticationEnabled": s.token != "",
	})
}

func (s *server) install(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req releaseRequest
	if !decode(w, r, &req) || !validRelease(w, req.Release, req.Namespace) {
		return
	}
	if req.ChartVersion == "" {
		req.ChartVersion = defaultChartVersion
	}
	if req.TargetNode == "" {
		writeError(w, 400, errors.New("targetNode is required"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Minute)
	defer cancel()
	args := []string{
		"upgrade", "--install", req.Release, defaultChart,
		"--version", req.ChartVersion, "--namespace", req.Namespace, "--create-namespace",
		"--wait", "--timeout", "12m",
		"--rollback-on-failure", "--reset-values",
		"--set-string", "namespace.name=" + req.Namespace,
		"--set-string", "global.namespace=" + req.Namespace,
		"--set-string", "global.targetNode=" + req.TargetNode,
		"--set-string", "upf.nodeSelector.kubernetes\\.io/hostname=" + req.TargetNode,
		"--set-string", "trex.nodeSelector.kubernetes\\.io/hostname=" + req.TargetNode,
		"--set-string", "pfcpSim.nodeSelector.kubernetes\\.io/hostname=" + req.TargetNode,
		"--set", "pfcpSim.sessionCreator.enabled=false",
		"--set", "trex.test.enabled=false",
		"--set", "global.imagePullSecrets=null",
	}
	out, err := command(ctx, "helm", args, nil)
	if err != nil {
		writeCommandError(w, err, out)
		return
	}
	writeJSON(w, 200, map[string]string{"status": "installed", "output": out})
}

func (s *server) uninstall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		methodNotAllowed(w)
		return
	}
	var req deleteRequest
	if !decode(w, r, &req) || !validRelease(w, req.Release, req.Namespace) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	out, err := command(ctx, "helm", []string{"uninstall", req.Release, "--namespace", req.Namespace, "--wait"}, nil)
	if err != nil {
		writeCommandError(w, err, out)
		return
	}
	writeJSON(w, 200, map[string]string{"status": "uninstalled", "output": out})
}

func (s *server) status(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	release, namespace := queryRelease(r)
	if !validRelease(w, release, namespace) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	helmOut, helmErr := command(ctx, "helm", []string{"status", release, "-n", namespace, "-o", "json"}, nil)
	podsOut, podsErr := command(ctx, "kubectl", []string{"get", "pods", "-n", namespace, "-l", "app.kubernetes.io/instance=" + release, "-o", "json"}, nil)
	result := map[string]interface{}{"installed": helmErr == nil, "release": release, "namespace": namespace}
	if helmErr == nil {
		var v interface{}
		if json.Unmarshal([]byte(helmOut), &v) == nil {
			result["helm"] = v
		}
	}
	if podsErr == nil {
		var v interface{}
		if json.Unmarshal([]byte(podsOut), &v) == nil {
			result["pods"] = v
		}
	}
	writeJSON(w, 200, result)
}

func (s *server) sessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req sessionRequest
	if !decode(w, r, &req) || !validRelease(w, req.Release, req.Namespace) {
		return
	}
	if req.Count < 1 || req.Count > 1000000 || req.BaseID < 1 || req.QFI < 1 || req.QFI > 63 {
		writeError(w, 400, errors.New("count, baseId, or qfi is outside the allowed range"))
		return
	}
	if _, err := firstAddress(req.UEPool); err != nil {
		writeError(w, 400, fmt.Errorf("invalid UE pool: %w", err))
		return
	}
	resetCtx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	upfStatefulSet := req.Release + "-travelping-upf-loadtest-upf"
	if out, err := command(resetCtx, "kubectl", []string{
		"rollout", "restart", "statefulset/" + upfStatefulSet, "-n", req.Namespace,
	}, nil); err != nil {
		writeError(w, 500, fmt.Errorf("restart UPF: %w: %s", err, out))
		return
	}
	if out, err := command(resetCtx, "kubectl", []string{
		"rollout", "status", "statefulset/" + upfStatefulSet, "-n", req.Namespace, "--timeout=120s",
	}, nil); err != nil {
		writeError(w, 500, fmt.Errorf("wait for UPF restart: %w: %s", err, out))
		return
	}
	deployment := req.Release + "-travelping-upf-loadtest-pfcp-sim"
	if out, err := command(resetCtx, "kubectl", []string{
		"rollout", "restart", "deployment/" + deployment, "-n", req.Namespace,
	}, nil); err != nil {
		writeError(w, 500, fmt.Errorf("restart PFCP simulator: %w: %s", err, out))
		return
	}
	if out, err := command(resetCtx, "kubectl", []string{
		"rollout", "status", "deployment/" + deployment, "-n", req.Namespace, "--timeout=120s",
	}, nil); err != nil {
		writeError(w, 500, fmt.Errorf("wait for PFCP simulator restart: %w: %s", err, out))
		return
	}
	name := runName("pfcp")
	service := req.Release + "-travelping-upf-loadtest-pfcp-sim"
	script := `set -eu
echo "STEP Waiting for PFCP simulator"
server="${PFCP_SERVICE}:54321"
for i in $(seq 1 60); do nc -z -w 2 "${PFCP_SERVICE}" 54321 && break; sleep 2; done
echo "STEP Configuring PFCP simulator"
pfcpctl -s "$server" service configure --n3-addr "$UPF_N3_ADDR" --remote-peer-addr "$UPF_ADDR"
echo "STEP Establishing PFCP association"
pfcpctl -s "$server" service associate
echo "STEP Creating ${SESSION_COUNT} PFCP sessions"
pfcpctl -s "$server" session create --count "$SESSION_COUNT" --baseID "$BASE_ID" --gnb-addr "$GNB_ADDR" --ue-pool "$UE_POOL" --qfi "$QFI"
echo "PFCP_RUN_COMPLETE sessions=${SESSION_COUNT}"`
	node := s.workloadNode(r.Context(), req.Namespace, req.Release, "pfcp-sim")
	job := jobManifest(name, req.Namespace, "pfcp", "ghcr.io/infinitydon/pfcpsim-travelping:v1.4.4-10", node,
		[]string{"/bin/sh", "-c", script}, map[string]string{
			"PFCP_SERVICE": service, "SESSION_COUNT": strconv.Itoa(req.Count),
			"BASE_ID": strconv.Itoa(req.BaseID), "UE_POOL": req.UEPool, "QFI": strconv.Itoa(req.QFI),
			"GNB_ADDR": req.GNBAddr, "UPF_N3_ADDR": req.UPFN3Addr, "UPF_ADDR": req.UPFAddr,
		})
	setJobAnnotations(job, map[string]string{
		"loadtest.infinitydon.io/release":       req.Release,
		"loadtest.infinitydon.io/session-count": strconv.Itoa(req.Count),
		"loadtest.infinitydon.io/base-id":       strconv.Itoa(req.BaseID),
		"loadtest.infinitydon.io/ue-pool":       req.UEPool,
		"loadtest.infinitydon.io/qfi":           strconv.Itoa(req.QFI),
	})
	s.createJob(w, r, job, name)
}

func (s *server) traffic(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req trafficRequest
	if !decode(w, r, &req) || !validRelease(w, req.Release, req.Namespace) {
		return
	}
	if req.PPS < 1 || req.Duration < 1 || req.Duration > 86400 || req.PacketSize < 78 || req.PacketSize > 9000 ||
		req.SessionCnt < 1 || req.TEIDStart < 1 || req.TEIDStep < 1 || req.MaxLoss < 0 || req.MaxLoss > 100 {
		writeError(w, 400, errors.New("traffic parameters are outside the allowed range"))
		return
	}
	state, err := s.activeSessionState(r.Context(), req.Namespace, req.Release)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	if !state.Available {
		writeError(w, http.StatusConflict, errors.New("no active PFCP session set; inject PFCP sessions after the latest simulator restart"))
		return
	}
	if req.SessionCnt != state.Count || req.TEIDStart != state.BaseID || req.UEStart != state.UEStart {
		writeError(w, http.StatusConflict, fmt.Errorf(
			"traffic parameters do not match active PFCP sessions: expected sessionCount=%d, teidStart=%d, ueStart=%s",
			state.Count, state.BaseID, state.UEStart,
		))
		return
	}
	resetCtx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	statefulSet := req.Release + "-travelping-upf-loadtest-trex"
	if out, err := command(resetCtx, "kubectl", []string{
		"rollout", "restart", "statefulset/" + statefulSet, "-n", req.Namespace,
	}, nil); err != nil {
		writeError(w, 500, fmt.Errorf("restart TRex server: %w: %s", err, out))
		return
	}
	if out, err := command(resetCtx, "kubectl", []string{
		"rollout", "status", "statefulset/" + statefulSet, "-n", req.Namespace, "--timeout=120s",
	}, nil); err != nil {
		writeError(w, 500, fmt.Errorf("wait for TRex server restart: %w: %s", err, out))
		return
	}
	name := runName("trex")
	service := req.Release + "-travelping-upf-loadtest-trex-rpc"
	node := s.workloadNode(r.Context(), req.Namespace, req.Release, "trex")
	job := jobManifest(name, req.Namespace, "traffic", "eisai/cisco-trex:v3.06", node,
		[]string{"/bin/bash", "-c", trafficScript}, map[string]string{
			"PYTHONPATH":  "/opt/trex/v3.06/automation/trex_control_plane/interactive",
			"TREX_SERVER": service, "PPS": strconv.Itoa(req.PPS), "DURATION": strconv.Itoa(req.Duration),
			"PACKET_SIZE": strconv.Itoa(req.PacketSize), "SESSION_COUNT": strconv.Itoa(req.SessionCnt),
			"TEID_START": strconv.Itoa(req.TEIDStart), "TEID_STEP": strconv.Itoa(req.TEIDStep),
			"UE_START": req.UEStart, "INNER_DST": req.InnerDst,
			"MAX_LOSS_PERCENT": strconv.FormatFloat(req.MaxLoss, 'f', -1, 64),
		})
	setJobAnnotations(job, map[string]string{
		"loadtest.infinitydon.io/release":       req.Release,
		"loadtest.infinitydon.io/session-count": strconv.Itoa(req.SessionCnt),
		"loadtest.infinitydon.io/teid-start":    strconv.Itoa(req.TEIDStart),
		"loadtest.infinitydon.io/ue-start":      req.UEStart,
	})
	s.createJob(w, r, job, name)
}

func (s *server) sessionState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	release, namespace := queryRelease(r)
	if !validRelease(w, release, namespace) {
		return
	}
	state, err := s.activeSessionState(r.Context(), namespace, release)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, state)
}

func (s *server) activeSessionState(ctx context.Context, namespace, release string) (sessionState, error) {
	queryCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	podOut, err := command(queryCtx, "kubectl", []string{
		"get", "pods", "-n", namespace,
		"-l", "app.kubernetes.io/instance=" + release + ",component=pfcp-sim",
		"-o", "json",
	}, nil)
	if err != nil {
		return sessionState{}, fmt.Errorf("read PFCP simulator pod: %w: %s", err, podOut)
	}
	jobsOut, err := command(queryCtx, "kubectl", []string{
		"get", "jobs", "-n", namespace,
		"-l", "app.kubernetes.io/managed-by=" + managedByLabel + ",loadtest.infinitydon.io/type=pfcp",
		"-o", "json",
	}, nil)
	if err != nil {
		return sessionState{}, fmt.Errorf("read PFCP jobs: %w: %s", err, jobsOut)
	}
	state, err := findActiveSessionState([]byte(jobsOut), []byte(podOut), release)
	if err != nil {
		return sessionState{}, err
	}
	if !state.Available {
		persisted, err := s.persistedSessionState(queryCtx, namespace, release, []byte(podOut))
		if err != nil {
			return sessionState{}, err
		}
		if persisted.Available {
			state = persisted
		}
	}
	if !state.Available {
		return state, nil
	}
	liveCount, err := s.liveUPFSessionCount(queryCtx, namespace, release)
	if err != nil {
		state.Available = false
		state.Unavailable = err.Error()
		return state, nil
	}
	if liveCount != state.Count {
		state.Available = false
		state.Unavailable = fmt.Sprintf(
			"UPF reports %d live sessions, but the last injection recorded %d; inject PFCP sessions again",
			liveCount, state.Count,
		)
	}
	return state, nil
}

func (s *server) liveUPFSessionCount(ctx context.Context, namespace, release string) (int, error) {
	podOut, err := command(ctx, "kubectl", []string{
		"get", "pods", "-n", namespace,
		"-l", "app.kubernetes.io/instance=" + release + ",component=upf",
		"-o", "jsonpath={.items[0].metadata.name}",
	}, nil)
	if err != nil || strings.TrimSpace(podOut) == "" {
		return 0, fmt.Errorf("UPF pod is unavailable")
	}
	out, err := command(ctx, "kubectl", []string{
		"exec", "-n", namespace, strings.TrimSpace(podOut), "-c", "upf", "--",
		"vppctl", "show", "upf", "association",
	}, nil)
	if err != nil {
		return 0, fmt.Errorf("cannot read live UPF sessions: %w: %s", err, out)
	}
	return parseUPFSessionCount(out), nil
}

func parseUPFSessionCount(output string) int {
	match := upfSessionCount.FindStringSubmatch(output)
	if len(match) != 2 {
		return 0
	}
	count, _ := strconv.Atoi(match[1])
	return count
}

func findActiveSessionState(jobsJSON, podsJSON []byte, release string) (sessionState, error) {
	simulatorStarted, found, err := simulatorStart(podsJSON)
	if err != nil {
		return sessionState{}, err
	}
	if !found {
		return sessionState{Unavailable: "PFCP simulator pod was not found"}, nil
	}

	var jobs struct {
		Items []struct {
			Metadata struct {
				Name        string            `json:"name"`
				Annotations map[string]string `json:"annotations"`
			} `json:"metadata"`
			Spec struct {
				Template struct {
					Spec struct {
						Containers []struct {
							Env []struct {
								Name  string `json:"name"`
								Value string `json:"value"`
							} `json:"env"`
						} `json:"containers"`
					} `json:"spec"`
				} `json:"template"`
			} `json:"spec"`
			Status struct {
				Succeeded      int       `json:"succeeded"`
				CompletionTime time.Time `json:"completionTime"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(jobsJSON, &jobs); err != nil {
		return sessionState{}, fmt.Errorf("decode PFCP jobs: %w", err)
	}

	var active sessionState
	var latest time.Time
	for _, job := range jobs.Items {
		if job.Status.Succeeded < 1 || job.Status.CompletionTime.Before(simulatorStarted) || !job.Status.CompletionTime.After(latest) {
			continue
		}
		values := map[string]string{}
		if len(job.Spec.Template.Spec.Containers) > 0 {
			for _, item := range job.Spec.Template.Spec.Containers[0].Env {
				values[item.Name] = item.Value
			}
		}
		annotations := job.Metadata.Annotations
		jobRelease := annotations["loadtest.infinitydon.io/release"]
		if jobRelease == "" {
			if !strings.HasPrefix(values["PFCP_SERVICE"], release+"-") {
				continue
			}
		} else if jobRelease != release {
			continue
		}
		count, countErr := strconv.Atoi(firstNonEmpty(annotations["loadtest.infinitydon.io/session-count"], values["SESSION_COUNT"]))
		baseID, baseErr := strconv.Atoi(firstNonEmpty(annotations["loadtest.infinitydon.io/base-id"], values["BASE_ID"]))
		qfi, _ := strconv.Atoi(firstNonEmpty(annotations["loadtest.infinitydon.io/qfi"], values["QFI"]))
		uePool := firstNonEmpty(annotations["loadtest.infinitydon.io/ue-pool"], values["UE_POOL"])
		ueStart, ueErr := firstAddress(uePool)
		if countErr != nil || baseErr != nil || ueErr != nil {
			continue
		}
		active = sessionState{
			Available: true, RunName: job.Metadata.Name, Count: count, BaseID: baseID,
			UEPool: uePool, UEStart: ueStart, QFI: qfi, CompletedAt: job.Status.CompletionTime.Format(time.RFC3339),
		}
		latest = job.Status.CompletionTime
	}
	if !active.Available {
		active.Unavailable = "No successful PFCP injection exists after the latest simulator restart"
	}
	return active, nil
}

func simulatorStart(podsJSON []byte) (time.Time, bool, error) {
	var pods struct {
		Items []struct {
			Metadata struct {
				CreationTimestamp time.Time `json:"creationTimestamp"`
			} `json:"metadata"`
		} `json:"items"`
	}
	if err := json.Unmarshal(podsJSON, &pods); err != nil {
		return time.Time{}, false, fmt.Errorf("decode PFCP simulator pods: %w", err)
	}
	if len(pods.Items) == 0 {
		return time.Time{}, false, nil
	}
	simulatorStarted := pods.Items[0].Metadata.CreationTimestamp
	for _, pod := range pods.Items[1:] {
		if pod.Metadata.CreationTimestamp.After(simulatorStarted) {
			simulatorStarted = pod.Metadata.CreationTimestamp
		}
	}
	return simulatorStarted, true, nil
}

func (s *server) persistedSessionState(ctx context.Context, namespace, release string, podsJSON []byte) (sessionState, error) {
	out, err := command(ctx, "kubectl", []string{
		"get", "configmap", sessionStateConfigMap(release), "-n", namespace,
		"-o", "jsonpath={.data.state\\.json}",
	}, nil)
	if err != nil || out == "" {
		return sessionState{}, nil
	}
	var state sessionState
	if err := json.Unmarshal([]byte(out), &state); err != nil {
		return sessionState{}, fmt.Errorf("decode persisted PFCP state: %w", err)
	}
	completedAt, err := time.Parse(time.RFC3339, state.CompletedAt)
	if err != nil {
		return sessionState{}, nil
	}
	startedAt, found, err := simulatorStart(podsJSON)
	if err != nil {
		return sessionState{}, err
	}
	if !found || completedAt.Before(startedAt) {
		return sessionState{}, nil
	}
	return state, nil
}

func (s *server) persistSessionState(ctx context.Context, namespace, release string, state sessionState) error {
	body, _ := json.Marshal(state)
	configMap := map[string]interface{}{
		"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": map[string]interface{}{
			"name": sessionStateConfigMap(release), "namespace": namespace,
			"labels": map[string]string{"app.kubernetes.io/managed-by": managedByLabel},
		},
		"data": map[string]string{"state.json": string(body)},
	}
	manifest, _ := json.Marshal(configMap)
	out, err := command(ctx, "kubectl", []string{"apply", "-f", "-"}, manifest)
	if err != nil {
		return fmt.Errorf("persist PFCP session state: %w: %s", err, out)
	}
	return nil
}

func sessionStateConfigMap(release string) string {
	sum := sha256.Sum256([]byte(release))
	return fmt.Sprintf("upf-loadtest-state-%x", sum[:8])
}

func firstAddress(cidr string) (string, error) {
	prefix, err := netip.ParsePrefix(cidr)
	if err != nil {
		return "", err
	}
	return prefix.Masked().Addr().Next().String(), nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func (s *server) createJob(w http.ResponseWriter, r *http.Request, job map[string]interface{}, name string) {
	body, _ := json.Marshal(job)
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	out, err := command(ctx, "kubectl", []string{"apply", "-f", "-"}, body)
	if err != nil {
		writeCommandError(w, err, out)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"name": name, "status": "created", "output": out})
}

func (s *server) runs(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodDelete {
		s.clearRuns(w, r)
		return
	}
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	namespace := r.URL.Query().Get("namespace")
	if !validName(namespace) {
		writeError(w, 400, errors.New("invalid namespace"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	out, err := command(ctx, "kubectl", []string{"get", "jobs", "-n", namespace, "-l", "app.kubernetes.io/managed-by=" + managedByLabel, "-o", "json"}, nil)
	if err != nil {
		writeCommandError(w, err, out)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(out))
}

func (s *server) clearRuns(w http.ResponseWriter, r *http.Request) {
	release, namespace := queryRelease(r)
	if !validRelease(w, release, namespace) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	state, err := s.activeSessionState(ctx, namespace, release)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	if state.Available {
		if err := s.persistSessionState(ctx, namespace, release, state); err != nil {
			writeError(w, 500, err)
			return
		}
	}
	out, err := command(ctx, "kubectl", []string{
		"delete", "jobs", "-n", namespace,
		"-l", "app.kubernetes.io/managed-by=" + managedByLabel,
		"--ignore-not-found=true", "--wait=false",
	}, nil)
	if err != nil {
		writeCommandError(w, err, out)
		return
	}
	writeJSON(w, 200, map[string]interface{}{
		"status": "cleared", "activeSessionStatePreserved": state.Available, "output": out,
	})
}

func (s *server) runDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	namespace, name := r.URL.Query().Get("namespace"), r.URL.Query().Get("name")
	if !validName(namespace) || !validName(name) {
		writeError(w, 400, errors.New("invalid namespace or run name"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	jobOut, err := command(ctx, "kubectl", []string{"get", "job", name, "-n", namespace, "-o", "json"}, nil)
	if err != nil {
		writeCommandError(w, err, jobOut)
		return
	}
	podsOut, _ := command(ctx, "kubectl", []string{"get", "pods", "-n", namespace, "-l", "job-name=" + name, "-o", "json"}, nil)
	eventsOut, _ := command(ctx, "kubectl", []string{
		"get", "events", "-n", namespace,
		"--field-selector", "involvedObject.kind=Pod",
		"--sort-by=.metadata.creationTimestamp", "-o", "json",
	}, nil)
	logsOut, _ := command(ctx, "kubectl", []string{"logs", "-n", namespace, "job/" + name, "--tail=1000"}, nil)

	var job, pods, events interface{}
	json.Unmarshal([]byte(jobOut), &job)
	json.Unmarshal([]byte(podsOut), &pods)
	json.Unmarshal([]byte(eventsOut), &events)
	writeJSON(w, 200, map[string]interface{}{
		"job": job, "pods": pods, "events": filterRunEvents(events, name), "logs": logsOut,
	})
}

func filterRunEvents(raw interface{}, jobName string) []interface{} {
	result := []interface{}{}
	object, ok := raw.(map[string]interface{})
	if !ok {
		return result
	}
	items, ok := object["items"].([]interface{})
	if !ok {
		return result
	}
	for _, item := range items {
		event, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		involved, _ := event["involvedObject"].(map[string]interface{})
		name, _ := involved["name"].(string)
		if strings.HasPrefix(name, jobName+"-") {
			result = append(result, event)
		}
	}
	return result
}

func (s *server) logs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	namespace, name := r.URL.Query().Get("namespace"), r.URL.Query().Get("name")
	if !validName(namespace) || !validName(name) {
		writeError(w, 400, errors.New("invalid namespace or run name"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	out, err := command(ctx, "kubectl", []string{"logs", "-n", namespace, "job/" + name, "--tail=1000"}, nil)
	if err != nil {
		writeCommandError(w, err, out)
		return
	}
	writeJSON(w, 200, map[string]string{"logs": out})
}

func (s *server) stop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		methodNotAllowed(w)
		return
	}
	namespace, name := r.URL.Query().Get("namespace"), r.URL.Query().Get("name")
	if !validName(namespace) || !validName(name) {
		writeError(w, 400, errors.New("invalid namespace or run name"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	out, err := command(ctx, "kubectl", []string{"delete", "job", name, "-n", namespace, "--wait=false"}, nil)
	if err != nil {
		writeCommandError(w, err, out)
		return
	}
	writeJSON(w, 200, map[string]string{"status": "stopped", "output": out})
}

func (s *server) static(w http.ResponseWriter, r *http.Request) {
	path := filepath.Clean(r.URL.Path)
	if path == "/" {
		path = "/index.html"
	}
	full := filepath.Join(s.staticDir, path)
	if info, err := os.Stat(full); err == nil && !info.IsDir() {
		http.ServeFile(w, r, full)
		return
	}
	http.ServeFile(w, r, filepath.Join(s.staticDir, "index.html"))
}

func jobManifest(name, namespace, kind, image, node string, commandArgs []string, envs map[string]string) map[string]interface{} {
	envList := make([]map[string]string, 0, len(envs))
	for k, v := range envs {
		envList = append(envList, map[string]string{"name": k, "value": v})
	}
	podSpec := map[string]interface{}{
		"restartPolicy": "Never",
		"containers": []map[string]interface{}{{"name": "runner", "image": image, "imagePullPolicy": "IfNotPresent",
			"command": commandArgs[:1], "args": commandArgs[1:], "env": envList,
			"resources": map[string]interface{}{"requests": map[string]string{"cpu": "100m", "memory": "128Mi"}, "limits": map[string]string{"cpu": "1", "memory": "1Gi"}},
		}},
	}
	if node != "" {
		podSpec["nodeSelector"] = map[string]string{"kubernetes.io/hostname": node}
	}
	return map[string]interface{}{
		"apiVersion": "batch/v1", "kind": "Job",
		"metadata": map[string]interface{}{"name": name, "namespace": namespace, "labels": map[string]string{
			"app.kubernetes.io/managed-by": managedByLabel, "loadtest.infinitydon.io/type": kind,
		}},
		"spec": map[string]interface{}{
			"backoffLimit": 0, "ttlSecondsAfterFinished": 86400,
			"template": map[string]interface{}{"metadata": map[string]interface{}{"labels": map[string]string{
				"app.kubernetes.io/managed-by": managedByLabel, "loadtest.infinitydon.io/type": kind,
			}}, "spec": podSpec},
		},
	}
}

func setJobAnnotations(job map[string]interface{}, annotations map[string]string) {
	metadata := job["metadata"].(map[string]interface{})
	metadata["annotations"] = annotations
	template := job["spec"].(map[string]interface{})["template"].(map[string]interface{})
	templateMetadata := template["metadata"].(map[string]interface{})
	templateMetadata["annotations"] = annotations
}

func (s *server) workloadNode(ctx context.Context, namespace, release, component string) string {
	out, err := command(ctx, "kubectl", []string{
		"get", "pods", "-n", namespace,
		"-l", "app.kubernetes.io/instance=" + release + ",component=" + component,
		"-o", "jsonpath={.items[0].spec.nodeName}",
	}, nil)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func command(ctx context.Context, name string, args []string, stdin []byte) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	err := cmd.Run()
	return strings.TrimSpace(output.String()), err
}

func decode(w http.ResponseWriter, r *http.Request, dst interface{}) bool {
	defer r.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		writeError(w, 400, fmt.Errorf("invalid request: %w", err))
		return false
	}
	return true
}

func validRelease(w http.ResponseWriter, release, namespace string) bool {
	if !validName(release) || !validName(namespace) {
		writeError(w, 400, errors.New("release and namespace must be valid Kubernetes names"))
		return false
	}
	return true
}

func validName(v string) bool { return len(v) > 0 && len(v) <= 63 && dnsLabel.MatchString(v) }

func queryRelease(r *http.Request) (string, string) {
	return r.URL.Query().Get("release"), r.URL.Query().Get("namespace")
}

func runName(prefix string) string {
	return fmt.Sprintf("%s-%s", prefix, strings.ToLower(strconv.FormatInt(time.Now().UnixMilli(), 36)))
}

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}
func writeCommandError(w http.ResponseWriter, err error, out string) {
	writeError(w, 500, fmt.Errorf("%v: %s", err, out))
}
func methodNotAllowed(w http.ResponseWriter) {
	writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
}
func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func requestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
	})
}

const trafficScript = `set -eu
echo "STEP Preparing TRex traffic profile"
cat >/tmp/run.py <<'PY'
import ipaddress, json, os, signal, time
from trex.stl.api import *
from scapy.contrib.gtp import GTP_U_Header

server=os.environ["TREX_SERVER"]; pps=int(os.environ["PPS"]); duration=int(os.environ["DURATION"])
size=int(os.environ["PACKET_SIZE"]); count=int(os.environ["SESSION_COUNT"])
teid_start=int(os.environ["TEID_START"]); teid_step=int(os.environ["TEID_STEP"])
ue_start=os.environ["UE_START"]; inner_dst=os.environ["INNER_DST"]; max_loss=float(os.environ["MAX_LOSS_PERCENT"])
pad=size-78
packet=(Ether()/IP(src="10.0.3.1",dst="10.0.3.10")/UDP(sport=2152,dport=2152,chksum=0)/
        GTP_U_Header(teid=teid_start)/IP(src=ue_start,dst=inner_dst)/UDP(sport=1024,dport=9,chksum=0)/Raw(load=b"x"*pad))
vm=STLVM()
ue_max=str(ipaddress.ip_address(ue_start)+count-1)
outer_sport_max=min(65535,1024+count-1)
vm.var(name="ue_ip",min_value=ue_start,max_value=ue_max,size=4,step=1,op="inc")
vm.var(name="teid",min_value=teid_start,max_value=teid_start+(count-1)*teid_step,size=4,step=teid_step,op="inc")
vm.var(name="outer_sport",min_value=1024,max_value=outer_sport_max,size=2,step=1,op="inc")
vm.write(fv_name="outer_sport",pkt_offset="UDP:0.sport")
vm.write(fv_name="teid",pkt_offset=46); vm.write(fv_name="ue_ip",pkt_offset="IP:1.src"); vm.fix_chksum(offset="IP:1")
stream=STLStream(packet=STLPktBuilder(pkt=packet,vm=vm),mode=STLTXCont(pps=1))
client=STLClient(server=server)
def stop(*_):
    try: client.stop(ports=[0])
    except: pass
signal.signal(signal.SIGTERM,stop)
try:
    print("STEP Connecting to TRex server",flush=True)
    client.connect(); client.acquire(ports=[0,1],force=True)
    try: client.stop(ports=[0])
    except: pass
    client.reset(ports=[0,1])
    print("STEP Draining residual RX traffic",flush=True)
    drain_deadline=time.monotonic()+10.0
    previous_drain_rx=None; stable_samples=0
    while time.monotonic()<drain_deadline and stable_samples<2:
        time.sleep(1.0)
        drain_rx=client.get_stats(ports=[1])[1]["ipackets"]
        if previous_drain_rx is not None and drain_rx==previous_drain_rx: stable_samples+=1
        else: stable_samples=0
        previous_drain_rx=drain_rx
    drained_rx_packets=previous_drain_rx or 0
    print("STEP Installing GTP-U stream",flush=True)
    client.add_streams([stream],ports=[0]); client.clear_stats()
    print("STEP Transmitting traffic",flush=True)
    started=time.monotonic(); interval=max(1.0,duration/300.0); samples=[]
    previous_time=started; previous_tx=0; previous_rx=0
    expected=pps*duration
    queue_budget=max(1000,int(expected*0.0001))
    wall_deadline=started+duration+max(5.0,duration*0.10)
    saturation_reason=None
    client.start(ports=[0],mult="%spps"%pps,duration=duration)
    while client.is_traffic_active(ports=[0]):
        time.sleep(interval)
        now=time.monotonic(); stats=client.get_stats(ports=[0,1]); elapsed=now-started
        util_stats=client.get_util_stats().get("cpu",[])
        worker_cpu=[max(entry.get("history") or [0]) for entry in util_stats]
        tx=stats[0]["opackets"]; rx=stats[1]["ipackets"]; lost=max(tx-rx,0)
        sample_period=max(now-previous_time,0.001)
        interval_tx=max(tx-previous_tx,0); interval_rx=max(rx-previous_rx,0)
        sample={"elapsed_seconds":round(elapsed,3),"tx_packets":tx,"rx_packets":rx,"lost_packets":lost,
                "loss_percent":lost*100.0/tx if tx else 100.0,
                "tx_pps":interval_tx/sample_period,"rx_pps":interval_rx/sample_period,
                "tx_l1_bps":stats[0].get("tx_bps_L1",0),"rx_l1_bps":stats[1].get("rx_bps_L1",0),
                "tx_util_percent":stats[0].get("tx_util",0),"rx_util_percent":stats[1].get("rx_util",0),
                "cpu_util_percent":stats.get("global",{}).get("cpu_util",0),
                "peak_worker_cpu_percent":max(worker_cpu or [0]),"trex_worker_count":len(worker_cpu),
                "queue_full":stats.get("global",{}).get("queue_full",0),
                "tx_errors":stats[0].get("oerrors",0),"rx_errors":stats[1].get("ierrors",0)}
        samples.append(sample)
        print("TREX_SAMPLE "+json.dumps(sample),flush=True)
        previous_time=now; previous_tx=tx; previous_rx=rx
        if sample["queue_full"]>queue_budget:
            saturation_reason="TRex queue pressure exceeded the run budget"
            client.stop(ports=[0])
            break
        if now>wall_deadline:
            saturation_reason="TRex did not finish within the wall-clock deadline"
            client.stop(ports=[0])
            break
    client.wait_on_traffic(ports=[0])
    print("STEP Collecting traffic counters",flush=True)
    stats=client.get_stats(); tx=stats[0]["opackets"]; raw_rx=stats[1]["ipackets"]
    rx=min(raw_rx,tx); unclassified_rx=max(raw_rx-tx,0); lost=max(tx-rx,0)
    loss_percent=lost*100.0/tx if tx else 100.0
    elapsed=max(time.monotonic()-started,0.001); rate_period=elapsed
    unclassified_budget=max(100,int(tx*0.001))
    first_bad=next((s for s in samples if s["loss_percent"]>max_loss),None)
    queue_full=stats.get("global",{}).get("queue_full",0)
    generator_saturated=saturation_reason is not None or queue_full>queue_budget or unclassified_rx>unclassified_budget
    if saturation_reason: reason=saturation_reason
    elif queue_full>queue_budget: reason="TRex queue pressure exceeded the run budget"
    elif unclassified_rx>unclassified_budget: reason="Residual or delayed RX traffic exceeded the accounting budget"
    elif tx == 0: reason="TRex transmitted no packets"
    elif rx == 0: reason="UPF forwarded no packets"
    elif loss_percent > max_loss and first_bad and first_bad["rx_packets"] > 0:
        reason="Forwarding degraded during the run at %.1f seconds"%first_bad["elapsed_seconds"]
    elif loss_percent > max_loss: reason="Packet loss exceeded the configured threshold"
    elif tx < expected*0.98: reason="TRex did not sustain the requested packet rate"
    else: reason="Traffic remained within the configured loss threshold"
    passed=not generator_saturated and tx>0 and rx>0 and loss_percent<=max_loss and tx>=expected*0.98
    result={"timestamp":time.time(),"requested_pps":pps,"duration_seconds":duration,"packet_size":size,
            "run_elapsed_seconds":elapsed,"drained_rx_packets":drained_rx_packets,
            "tx_packets":tx,"rx_packets":rx,"lost_packets":lost,"loss_percent":loss_percent,
            "max_loss_percent":max_loss,"expected_packets":expected,
            "actual_tx_pps":tx/rate_period,"actual_rx_pps":rx/rate_period,
            "tx_l2_mbps":stats[0]["obytes"]*8.0/rate_period/1000000,
            "rx_l2_mbps":stats[1]["ibytes"]*8.0/rate_period/1000000,
            "tx_l1_mbps":(stats[0]["obytes"]+tx*20)*8.0/rate_period/1000000,
            "rx_l1_mbps":(stats[1]["ibytes"]+raw_rx*20)*8.0/rate_period/1000000,
            "unclassified_rx_packets":unclassified_rx,
            "unclassified_rx_budget":unclassified_budget,
            "tx_errors":stats[0].get("oerrors",0),"rx_errors":stats[1].get("ierrors",0),
            "queue_full":queue_full,"queue_full_budget":queue_budget,
            "generator_saturated":generator_saturated,
            "peak_cpu_percent":max([s["cpu_util_percent"] for s in samples] or [0]),
            "peak_worker_cpu_percent":max([s["peak_worker_cpu_percent"] for s in samples] or [0]),
            "trex_worker_count":max([s["trex_worker_count"] for s in samples] or [0]),
            "first_loss_threshold_seconds":first_bad["elapsed_seconds"] if first_bad else None,
            "reason":reason,"passed":passed,"samples":samples}
    print("TREX_RESULT "+json.dumps(result),flush=True)
    if not result["passed"]: raise SystemExit(reason)
finally:
    if client.is_connected(): client.disconnect()
PY
cd /opt/trex/v3.06
exec python3 /tmp/run.py`
