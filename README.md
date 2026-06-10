# UPF Load Test WebUI

Web control plane for installing the Travelping UPG-VPP load-test environment,
injecting PFCP sessions, and running Cisco TRex GTP-U traffic.

The controller installs the public workload chart directly from:

```text
oci://ghcr.io/infinitydon/travelping-upf-loadtest:0.1.13
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
- Display packet rates automatically as `pps`, `Kpps`, or `Mpps` in traffic
  settings, run summaries, and time-series reports.
- Display workload pod readiness and test Job history.
- Follow live scheduling, image pull, container startup, PFCP/TRex application
  steps, Kubernetes events, logs, and pass/fail results.
- Inspect TRex time-series TX/RX rates, packet loss, L1/L2 throughput,
  aggregate and per-worker CPU utilization, queue pressure, port errors, and
  failure onset.
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

## Interpreting traffic results

The configured frame size is the outer N3 Ethernet frame without its 4-byte
FCS. For uplink traffic, the UPF removes 36 bytes of outer IPv4, UDP, and
GTP-U headers before transmitting the packet on N6. A 96-byte N3 frame
therefore becomes approximately a 60-byte N6 frame.

The dashboard labels these measurements as N3 TX and N6 RX. Their packet rates
should be nearly equal for forwarded traffic, but their L1 and L2 bandwidths
are not expected to be equal because they measure differently sized frames.
For example, 1.2 Mpps of 96-byte N3 traffic is approximately 1,152 Mbps at L1,
while the decapsulated 60-byte N6 traffic is approximately 768 Mbps at L1.
Use packet counts and the reported loss percentage for the end-to-end
forwarding comparison.

## NIC performance

On this test node, Intel 700 Series SR-IOV VFs backed by physical NICs were
materially more performant than the previous Proxmox/QEMU virtio-net path.
The virtio setup repeatedly saturated the TRex generator during a
600 Kpps, 300-second run. After moving the UPF and TRex traffic interfaces to
Intel VFs, a 1.2 Mpps, 300-second run completed with zero TRex queue-full
events and low packet loss.

VPP confirms that these VFs use its DPDK `iAVF` driver. Active receive
features include RSS, IPv4 checksum verification, and scatter; active
transmit offloads include IPv4, UDP, and TCP checksums plus multi-segment
transmit. These are real hardware-backed DPDK offloads, but the measured
improvement should not be attributed to offload alone: per-session GTP-U
source-port entropy, four receive queues, and 4,096 N3 RX descriptors were
also required to distribute and absorb the traffic correctly.

TRex still constructs, schedules, and submits every packet. Its lower CPU
utilization with the Intel VFs is primarily the result of direct SR-IOV DMA
and the DPDK `net_iavf` multi-queue/vector data path, rather than the NIC
generating traffic. The GTP-U profile also changes packet fields and fixes the
inner IPv4 checksum in the TRex VM, so checksum offload does not remove that
per-packet work.

At 1.2 Mpps, four TRex workers peaked at approximately 9-16% each. A
two-worker A/B run also sustained 1.2 Mpps with zero queue-full events and
20-25% peak worker utilization. Keep four workers for rate-scaling and burst
margin. Set `trex.cpu.cores=2` with four total requested CPUs when the goal is
to return two exclusive CPUs to Kubernetes and tests will remain near the
current baseline.

## Build

```sh
docker build -t ghcr.io/infinitydon/upf-loadtest-webui:v0.1.0 .
```

## Install

```sh
helm upgrade --install upf-loadtest-webui \
  oci://ghcr.io/infinitydon/charts/upf-loadtest-webui \
  --version 0.1.24 \
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
