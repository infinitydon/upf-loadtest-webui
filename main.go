package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
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
	defaultChartVersion = "0.1.2"
	managedByLabel      = "upf-loadtest-webui"
)

var dnsLabel = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

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
	job := jobManifest(name, req.Namespace, "pfcp", "ghcr.io/infinitydon/pfcpsim-travelping:v1.4.4-7", node,
		[]string{"/bin/sh", "-c", script}, map[string]string{
			"PFCP_SERVICE": service, "SESSION_COUNT": strconv.Itoa(req.Count),
			"BASE_ID": strconv.Itoa(req.BaseID), "UE_POOL": req.UEPool, "QFI": strconv.Itoa(req.QFI),
			"GNB_ADDR": req.GNBAddr, "UPF_N3_ADDR": req.UPFN3Addr, "UPF_ADDR": req.UPFAddr,
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
	s.createJob(w, r, job, name)
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
vm.var(name="ue_ip",min_value=ue_start,max_value=ue_max,size=4,step=1,op="inc")
vm.var(name="teid",min_value=teid_start,max_value=teid_start+(count-1)*teid_step,size=4,step=teid_step,op="inc")
vm.write(fv_name="teid",pkt_offset=46); vm.write(fv_name="ue_ip",pkt_offset="IP:1.src"); vm.fix_chksum(offset="IP:1")
stream=STLStream(packet=STLPktBuilder(pkt=packet,vm=vm),mode=STLTXCont(pps=1))
client=STLClient(server=server)
def stop(*_):
    try: client.stop(ports=[0])
    except: pass
signal.signal(signal.SIGTERM,stop)
try:
    print("STEP Connecting to TRex server",flush=True)
    client.connect(); client.acquire(ports=[0,1],force=True); client.reset(ports=[0,1])
    print("STEP Installing GTP-U stream",flush=True)
    client.add_streams([stream],ports=[0]); client.clear_stats()
    print("STEP Transmitting traffic",flush=True)
    client.start(ports=[0],mult="%spps"%pps,duration=duration); client.wait_on_traffic(ports=[0])
    print("STEP Collecting traffic counters",flush=True)
    stats=client.get_stats(); tx=stats[0]["opackets"]; rx=stats[1]["ipackets"]; lost=max(tx-rx,0)
    loss_percent=lost*100.0/tx if tx else 100.0
    result={"timestamp":time.time(),"requested_pps":pps,"duration_seconds":duration,"packet_size":size,
            "tx_packets":tx,"rx_packets":rx,"lost_packets":lost,"loss_percent":loss_percent,
            "max_loss_percent":max_loss,"passed":tx>0 and rx>0 and loss_percent<=max_loss}
    print("TREX_RESULT "+json.dumps(result),flush=True)
    if not result["passed"]: raise SystemExit("traffic result exceeded the loss threshold")
finally:
    if client.is_connected(): client.disconnect()
PY
cd /opt/trex/v3.06
exec python3 /tmp/run.py`
