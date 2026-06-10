# UPF Load Test WebUI

Web control plane for installing the Travelping UPG-VPP load-test environment,
injecting PFCP sessions, and running Cisco TRex GTP-U traffic.

The controller installs the public workload chart directly from:

```text
oci://ghcr.io/infinitydon/travelping-upf-loadtest:0.1.8
```

No GHCR credentials are required. Workload installation disables the chart's
automatic PFCP and TRex runners; each UI request becomes a labeled Kubernetes
Job with its own status, logs, cancellation, and 24-hour history.

## Features

- Install, upgrade, inspect, and delete the workload Helm release.
- Configure PFCP session count, UE pool, base ID, QFI, and endpoint addresses.
- Restart the PFCP simulator before injection so each run replaces stale
  associations and sessions with a clean session set.
- Show the active PFCP session count and parameters on the injection tab.
- Verify the live UPF PFCP association and session count before reporting
  sessions as active or allowing a TRex run.
- Configure TRex PPS, duration, Ethernet frame size (excluding FCS), UE count,
  TEID range, and inner destination.
- Display workload pod readiness and test Job history.
- Follow live scheduling, image pull, container startup, PFCP/TRex application
  steps, Kubernetes events, logs, and pass/fail results.
- Inspect TRex time-series TX/RX rates, packet loss, L1/L2 throughput,
  utilization, CPU, queue pressure, port errors, and failure onset.
- Drain residual RX traffic before each measurement, abort runs when TRex
  queue pressure exceeds a bounded budget, and report generator saturation
  separately from UPF forwarding failure.
- Restart the shared TRex StatefulSet before each traffic Job so a saturated
  run cannot contaminate the following measurement with residual generator
  state.
- Track the active PFCP session set and block TRex submissions whose session
  count, TEID base, or first UE address do not match it.
- Reopen the monitor for historical runs and stop active runs.
- Clear all recorded Jobs and logs while preserving the active PFCP session
  parameters for subsequent traffic tests.
- Navigate directly between dashboard, environment, and test runner views.
- NodePort by default, with ClusterIP, LoadBalancer, and optional Ingress.
- Optional bearer-token authentication from a Kubernetes Secret.

## Build

```sh
docker build -t ghcr.io/infinitydon/upf-loadtest-webui:v0.1.0 .
```

## Install

```sh
helm upgrade --install upf-loadtest-webui \
  oci://ghcr.io/infinitydon/charts/upf-loadtest-webui \
  --version 0.1.15 \
  --namespace upf-loadtest-system \
  --create-namespace \
  --set auth.token='replace-with-a-long-random-token'
```

Obtain the default NodePort:

```sh
kubectl -n upf-loadtest-system get svc upf-loadtest-webui-upf-loadtest-webui
```

Set `service.type=ClusterIP` or `service.type=LoadBalancer` as required.

## Security

The default chart grants cluster-wide release-management permissions because
the workload chart creates cluster-scoped RBAC and can target another
namespace. Deploy the WebUI on a trusted management network, configure
`auth.token` or `auth.existingSecret`, and restrict NodePort access at the
network layer.
