import React, { useEffect, useMemo, useState } from "react";
import ReactDOM from "react-dom/client";
import { Refine } from "@refinedev/core";
import {
  Alert, App as AntApp, Button, Card, Col, ConfigProvider, Descriptions, Divider, Drawer,
  Empty, Form, Input, InputNumber, Layout, Menu, Modal, Progress, Row, Space, Statistic,
  Table, Tabs, Tag, Timeline, Typography, message, theme,
} from "antd";
import {
  CloudServerOutlined, DashboardOutlined, DeleteOutlined, PlayCircleOutlined,
  ReloadOutlined, RocketOutlined, StopOutlined, UnorderedListOutlined,
} from "@ant-design/icons";
import "antd/dist/reset.css";
import "./styles.css";

type Config = { chart: string; chartVersion: string; defaultRelease: string; defaultNamespace: string; authenticationEnabled: boolean };
type Job = { metadata: { name: string; creationTimestamp: string; labels?: Record<string,string> }; status?: { active?: number; succeeded?: number; failed?: number } };
type RunDetail = {
  job?: any;
  pods?: { items?: any[] };
  events?: any[];
  logs?: string;
};
type SessionState = {
  available: boolean; runName?: string; count?: number; baseId?: number; uePool?: string;
  ueStart?: string; qfi?: number; completedAt?: string; unavailableReason?: string;
};

function formatPps(value: unknown) {
  const rate = Number(value || 0);
  const magnitude = Math.abs(rate);
  const formatter = new Intl.NumberFormat(undefined, {maximumFractionDigits: 2});
  if (magnitude >= 1_000_000) return `${formatter.format(rate / 1_000_000)} Mpps`;
  if (magnitude >= 1_000) return `${formatter.format(rate / 1_000)} Kpps`;
  return `${new Intl.NumberFormat(undefined, {maximumFractionDigits: 0}).format(rate)} pps`;
}

const getToken = () => localStorage.getItem("upfToken") || "";
async function api(path: string, init?: RequestInit) {
  const headers = new Headers(init?.headers);
  headers.set("Content-Type", "application/json");
  if (getToken()) headers.set("Authorization", `Bearer ${getToken()}`);
  const response = await fetch(path, { ...init, headers });
  const body = await response.json().catch(() => ({}));
  if (!response.ok) throw new Error(body.error || response.statusText);
  return body;
}

function Console() {
  const [config, setConfig] = useState<Config>();
  const [release, setRelease] = useState("upf-loadtest");
  const [namespace, setNamespace] = useState("upf-loadtest");
  const [status, setStatus] = useState<any>();
  const [jobs, setJobs] = useState<Job[]>([]);
  const [sessionState, setSessionState] = useState<SessionState>();
  const [busy, setBusy] = useState(false);
  const [activeTab, setActiveTab] = useState("dashboard");
  const [activeRun, setActiveRun] = useState<{name:string; type:string}>();
  const [runDetail, setRunDetail] = useState<RunDetail>();
  const [runOpen, setRunOpen] = useState(false);
  const [installForm] = Form.useForm();
  const [sessionForm] = Form.useForm();
  const [trafficForm] = Form.useForm();
  const configuredPps = Form.useWatch("pps", trafficForm);
  const [msg, holder] = message.useMessage();

  const refresh = async (showError = true) => {
    try {
      const [s, r, pfcp] = await Promise.all([
        api(`/api/status?release=${release}&namespace=${namespace}`),
        api(`/api/runs?namespace=${namespace}`).catch(() => ({ items: [] })),
        api(`/api/session-state?release=${release}&namespace=${namespace}`).catch(() => ({ available: false })),
      ]);
      setStatus(s); setJobs(r.items || []); setSessionState(pfcp);
      if (pfcp.available) {
        trafficForm.setFieldsValue({sessionCount: pfcp.count, teidStart: pfcp.baseId, ueStart: pfcp.ueStart});
      }
    } catch (e: any) {
      if (showError) msg.error(e.message);
    }
  };

  useEffect(() => {
    api("/api/config").then((c: Config) => {
      setConfig(c); setRelease(c.defaultRelease); setNamespace(c.defaultNamespace);
      installForm.setFieldsValue({ release: c.defaultRelease, namespace: c.defaultNamespace, chartVersion: c.chartVersion, targetNode: "ebpf-bng-node-01" });
    }).catch((e) => {
      const token = window.prompt("API token");
      if (token) { localStorage.setItem("upfToken", token); window.location.reload(); }
      else msg.error(e.message);
    });
  }, []);

  useEffect(() => { if (config) refresh(); }, [config, release, namespace]);

  useEffect(() => {
    if (!config) return;
    let stopped = false;
    let timer: number;
    const poll = async () => {
      await refresh(false);
      if (!stopped) timer = window.setTimeout(poll, 5000);
    };
    timer = window.setTimeout(poll, 5000);
    return () => {
      stopped = true;
      window.clearTimeout(timer);
    };
  }, [config, release, namespace]);

  const run = async (fn: () => Promise<any>, ok: string) => {
    setBusy(true);
    try { const result = await fn(); msg.success(ok); await refresh(); return result; }
    catch (e: any) { msg.error(e.message); }
    finally { setBusy(false); }
  };

  const pods = status?.pods?.items || [];
  const ready = pods.filter((p: any) => p.status?.containerStatuses?.every((c: any) => c.ready)).length;
  const jobRows = useMemo(() => jobs.map((j) => ({
    key: j.metadata.name, name: j.metadata.name, type: j.metadata.labels?.["loadtest.infinitydon.io/type"] || "run",
    created: new Date(j.metadata.creationTimestamp).toLocaleString(),
    state: j.status?.active ? "Running" : j.status?.succeeded ? "Succeeded" : j.status?.failed ? "Failed" : "Pending",
  })), [jobs]);

  const monitorRun = (name: string, type: string) => {
    setActiveRun({name, type});
    setRunDetail(undefined);
    setRunOpen(true);
  };

  useEffect(() => {
    if (!runOpen || !activeRun) return;
    let stopped = false;
    const poll = async () => {
      try {
        const detail = await api(`/api/runs/detail?namespace=${namespace}&name=${activeRun.name}`);
        if (!stopped) setRunDetail(detail);
        const terminal = detail.job?.status?.succeeded || detail.job?.status?.failed;
        if (!terminal && !stopped) window.setTimeout(poll, 1000);
        if (terminal && !stopped) refresh();
      } catch (e: any) {
        if (!stopped) {
          msg.error(e.message);
          window.setTimeout(poll, 2000);
        }
      }
    };
    poll();
    return () => { stopped = true; };
  }, [runOpen, activeRun?.name, namespace]);

  const startTrackedRun = async (path: string, values: any, type: string) => {
    const result = await run(
      () => api(path, {method:"POST", body:JSON.stringify({...values, release, namespace})}),
      type === "pfcp" ? "PFCP injection submitted" : "TRex load test submitted",
    );
    if (result?.name && type === "pfcp") {
      trafficForm.setFieldsValue({
        sessionCount: values.count,
        teidStart: values.baseId,
        ueStart: firstUE(values.uePool),
      });
    }
    if (result?.name) monitorRun(result.name, type);
  };

  const runPod = runDetail?.pods?.items?.[0];
  const runState = runDetail?.job?.status?.succeeded ? "Succeeded" : runDetail?.job?.status?.failed ? "Failed" :
    runDetail?.job?.status?.active ? "Running" : "Pending";
  const runType = activeRun?.type || runDetail?.job?.metadata?.labels?.["loadtest.infinitydon.io/type"] || "run";
  const stepLines = (runDetail?.logs || "").split("\n").filter((line) => line.startsWith("STEP ")).map((line) => line.slice(5));
  const configuredSteps = runType === "pfcp"
    ? ["Waiting for PFCP simulator", "Configuring PFCP simulator", "Establishing PFCP association", "Creating PFCP sessions"]
    : ["Preparing TRex traffic profile", "Connecting to TRex server", "Installing GTP-U stream", "Transmitting traffic", "Collecting traffic counters"];
  const completedSteps = configuredSteps.filter((step) => stepLines.some((line) =>
    line.startsWith(step) || (step === "Creating PFCP sessions" && /^Creating \d+ PFCP sessions$/.test(line))
  ));
  const podPhase = runPod?.status?.phase;
  const containerWaiting = runPod?.status?.containerStatuses?.[0]?.state?.waiting?.reason;
  const progress = runState === "Succeeded" || runState === "Failed" ? 100 :
    Math.min(90, (podPhase ? 20 : 5) + completedSteps.length * (70 / configuredSteps.length));
  const resultLine = (runDetail?.logs || "").split("\n").find((line) => line.startsWith("TREX_RESULT "));
  let trafficResult: any;
  try { trafficResult = resultLine ? JSON.parse(resultLine.slice(12)) : undefined; } catch { trafficResult = undefined; }

  return <Layout className="shell">
    {holder}
    <Layout.Sider width={245} theme="dark" className="sider">
      <div className="brand"><RocketOutlined /><div><strong>UPF Load Test</strong><small>Travelping + TRex</small></div></div>
      <Menu className="nav" theme="dark" mode="inline" selectedKeys={[activeTab === "sessions" || activeTab === "traffic" || activeTab === "history" ? "runners" : activeTab]}
        onClick={({key}) => setActiveTab(key === "runners" ? "sessions" : key)}
        items={[
          {key:"dashboard", icon:<DashboardOutlined />, label:"Dashboard"},
          {key:"environment", icon:<CloudServerOutlined />, label:"Environment"},
          {key:"runners", icon:<PlayCircleOutlined />, label:"Test runners"},
        ]} />
      <div className="chart-ref"><small>Workload chart</small><code>{config?.chartVersion || "..."}</code></div>
    </Layout.Sider>
    <Layout>
      <Layout.Header className="header">
        <div><Typography.Title level={3}>Load-test control plane</Typography.Title><Typography.Text type="secondary">{namespace}/{release}</Typography.Text></div>
        <Button icon={<ReloadOutlined />} onClick={() => refresh()}>Refresh</Button>
      </Layout.Header>
      <Layout.Content className="content">
        <Row gutter={[16,16]} className="stats">
          <Col xs={24} md={8}><Card><Statistic title="Environment" value={status?.installed ? "Installed" : "Not installed"} valueStyle={{color: status?.installed ? "#16a34a" : "#dc2626"}} /></Card></Col>
          <Col xs={24} md={8}><Card><Statistic title="Ready pods" value={ready} suffix={`/ ${pods.length}`} /></Card></Col>
          <Col xs={24} md={8}><Card><Statistic title="Recorded runs" value={jobs.length} /></Card></Col>
        </Row>
        <Tabs activeKey={activeTab} onChange={setActiveTab} items={[
          { key: "dashboard", label: "Dashboard", children: <Row gutter={[16,16]}>
            <Col xs={24} lg={14}><Card title="Environment overview">
              <Descriptions bordered size="small" column={1}>
                <Descriptions.Item label="Release">{namespace}/{release}</Descriptions.Item>
                <Descriptions.Item label="Status"><Tag color={status?.installed ? "green" : "red"}>{status?.helm?.info?.status || "not installed"}</Tag></Descriptions.Item>
                <Descriptions.Item label="Workload pods">{ready} of {pods.length} ready</Descriptions.Item>
                <Descriptions.Item label="OCI chart">{config?.chart}:{config?.chartVersion}</Descriptions.Item>
              </Descriptions>
            </Card></Col>
            <Col xs={24} lg={10}><Card title="Recent activity">
              {jobRows.length ? <Timeline items={jobRows.slice().reverse().slice(0,5).map((item:any)=>({
                color:item.state==="Succeeded"?"green":item.state==="Failed"?"red":"blue",
                children:<Button type="link" className="activity-link" onClick={()=>monitorRun(item.name,item.type)}>{item.type}: {item.name} ({item.state})</Button>
              }))}/> : <Empty description="No test runs yet"/>}
            </Card></Col>
          </Row> },
          { key: "environment", label: "Environment", children: <Card>
            <Form form={installForm} layout="vertical" onFinish={(v) => {
              setRelease(v.release); setNamespace(v.namespace);
              run(() => api("/api/release/install", {method:"POST", body:JSON.stringify(v)}), "Environment installed");
            }}>
              <Row gutter={16}>
                <Col xs={24} md={6}><Form.Item name="release" label="Release" rules={[{required:true}]}><Input /></Form.Item></Col>
                <Col xs={24} md={6}><Form.Item name="namespace" label="Namespace" rules={[{required:true}]}><Input /></Form.Item></Col>
                <Col xs={24} md={6}><Form.Item name="chartVersion" label="Chart version" rules={[{required:true}]}><Input /></Form.Item></Col>
                <Col xs={24} md={6}><Form.Item name="targetNode" label="Target node" rules={[{required:true}]}><Input /></Form.Item></Col>
              </Row>
              <Space><Button type="primary" htmlType="submit" loading={busy}>Install / upgrade</Button>
                <Button danger icon={<DeleteOutlined />} disabled={!status?.installed || busy} onClick={() => Modal.confirm({
                  title:"Delete load-test environment?", content:`Uninstall ${namespace}/${release}.`,
                  onOk:()=>run(()=>api("/api/release",{method:"DELETE",body:JSON.stringify({release,namespace})}),"Environment deleted")
                })}>Delete</Button></Space>
            </Form>
            <Divider />
            <Descriptions title="Runtime status" bordered size="small" column={2}>
              <Descriptions.Item label="Release status">{status?.helm?.info?.status || "unknown"}</Descriptions.Item>
              <Descriptions.Item label="Chart">{status?.helm?.chart || config?.chartVersion}</Descriptions.Item>
              <Descriptions.Item label="Pods" span={2}>{pods.map((p:any)=><Tag color={p.status?.phase==="Running"?"green":"orange"} key={p.metadata.name}>{p.metadata.name}: {p.status?.phase}</Tag>)}</Descriptions.Item>
            </Descriptions>
          </Card> },
          { key: "sessions", label: "PFCP sessions", children: <Card title="Inject PFCP sessions">
            {sessionState?.available ? <Alert className="session-state" type="success" showIcon
              message={`Active PFCP sessions: ${sessionState.count}`}
              description={`Base ID ${sessionState.baseId}, UE pool ${sessionState.uePool}, first UE ${sessionState.ueStart}, QFI ${sessionState.qfi}. Source: ${sessionState.runName}${sessionState.completedAt ? `, completed ${new Date(sessionState.completedAt).toLocaleString()}` : ""}.`}
            /> : <Alert className="session-state" type="warning" showIcon
              message="No active PFCP sessions"
              description={sessionState?.unavailableReason || "Start an injection to create PFCP sessions."}
            />}
            <Form form={sessionForm} layout="vertical" initialValues={{count:1000,baseId:1,uePool:"48.0.0.0/8",qfi:9,gnbAddr:"10.0.3.1",upfN3Addr:"10.0.3.10",upfAddr:"10.0.4.9:8805"}}
              onFinish={(v)=>startTrackedRun("/api/sessions",v,"pfcp")}>
              <Row gutter={16}>
                <Col xs={12} md={4}><Form.Item name="count" label="Sessions"><InputNumber min={1} max={1000000}/></Form.Item></Col>
                <Col xs={12} md={4}><Form.Item name="baseId" label="Base ID"><InputNumber min={1}/></Form.Item></Col>
                <Col xs={12} md={4}><Form.Item name="qfi" label="QFI"><InputNumber min={1} max={63}/></Form.Item></Col>
                <Col xs={12} md={6}><Form.Item name="uePool" label="UE pool"><Input /></Form.Item></Col>
                <Col xs={12} md={6}><Form.Item name="gnbAddr" label="gNB N3 address"><Input /></Form.Item></Col>
                <Col xs={12} md={8}><Form.Item name="upfN3Addr" label="UPF N3 address"><Input /></Form.Item></Col>
                <Col xs={12} md={8}><Form.Item name="upfAddr" label="UPF PFCP address"><Input /></Form.Item></Col>
              </Row>
              <Button type="primary" htmlType="submit" icon={<PlayCircleOutlined />} loading={busy} disabled={!status?.installed}>Start injection</Button>
            </Form>
          </Card> },
          { key: "traffic", label: "TRex traffic", children: <Card title="Start GTP-U traffic">
            {sessionState?.available ? <Alert className="session-state" type="success" showIcon
              message={`Active PFCP sessions: ${sessionState.count}`}
              description={`Base ID ${sessionState.baseId}, first UE ${sessionState.ueStart}, pool ${sessionState.uePool}. Source: ${sessionState.runName}.`}
            /> : <Alert className="session-state" type="warning" showIcon
              message="No active PFCP session set"
              description={sessionState?.unavailableReason || "Inject PFCP sessions before starting traffic."}
            />}
            <Form form={trafficForm} layout="vertical" initialValues={{pps:100000,duration:15,packetSize:96,sessionCount:1000,teidStart:1,teidStep:10,ueStart:"48.0.0.1",innerDst:"10.0.5.1",maxLossPercent:0.1}}
              onFinish={(v)=>startTrackedRun("/api/traffic",v,"traffic")}>
              <Row gutter={16}>
                <Col xs={12} md={6}><Form.Item name="pps" label="Packets per second" extra={`Configured rate: ${formatPps(configuredPps)}`}><InputNumber min={1}/></Form.Item></Col>
                <Col xs={12} md={6}><Form.Item name="duration" label="Duration (seconds)"><InputNumber min={1} max={86400}/></Form.Item></Col>
                <Col xs={12} md={6}><Form.Item name="packetSize" label="Frame size (no FCS)"><InputNumber min={78} max={9000}/></Form.Item></Col>
                <Col xs={12} md={6}><Form.Item name="sessionCount" label="Session count"><InputNumber min={1}/></Form.Item></Col>
                <Col xs={12} md={6}><Form.Item name="teidStart" label="TEID start"><InputNumber min={1}/></Form.Item></Col>
                <Col xs={12} md={6}><Form.Item name="teidStep" label="TEID step"><InputNumber min={1}/></Form.Item></Col>
                <Col xs={12} md={6}><Form.Item name="ueStart" label="First UE address"><Input /></Form.Item></Col>
                <Col xs={12} md={6}><Form.Item name="innerDst" label="Inner destination"><Input /></Form.Item></Col>
                <Col xs={12} md={6}><Form.Item name="maxLossPercent" label="Maximum loss (%)"><InputNumber min={0} max={100} step={0.1}/></Form.Item></Col>
              </Row>
              <Button type="primary" htmlType="submit" icon={<PlayCircleOutlined />} loading={busy} disabled={!status?.installed || !sessionState?.available}>Start traffic</Button>
            </Form>
          </Card> },
          { key: "history", label: "Run history", children: <Card title="Run history" extra={
            <Button danger icon={<DeleteOutlined />} disabled={!jobs.length || busy} onClick={() => Modal.confirm({
              title:"Clear all run history?",
              content:"This deletes every recorded PFCP and TRex Job and its logs. The active PFCP session parameters will be preserved separately.",
              okText:"Clear history", okButtonProps:{danger:true},
              onOk:()=>run(
                ()=>api(`/api/runs?release=${release}&namespace=${namespace}`,{method:"DELETE"}),
                "Run history cleared",
              ),
            })}>Clear run history</Button>
          }>
            <Table dataSource={jobRows} pagination={{pageSize:10}} columns={[
              {title:"Run",dataIndex:"name"}, {title:"Type",dataIndex:"type",render:(v)=><Tag>{v}</Tag>},
              {title:"Created",dataIndex:"created"}, {title:"State",dataIndex:"state",render:(v)=><Tag color={v==="Succeeded"?"green":v==="Failed"?"red":"blue"}>{v}</Tag>},
              {title:"Actions",render:(_,row:any)=><Space><Button size="small" icon={<UnorderedListOutlined/>} onClick={()=>monitorRun(row.name,row.type)}>Monitor</Button>
                {row.state==="Running"&&<Button danger size="small" icon={<StopOutlined />} onClick={()=>run(()=>api(`/api/runs/stop?namespace=${namespace}&name=${row.name}`,{method:"DELETE"}),"Run stopped")}>Stop</Button>}</Space>}
            ]} />
          </Card> },
        ]} />
      </Layout.Content>
    </Layout>
    <Drawer title={<Space><span>Run monitor</span>{activeRun && <Tag>{activeRun.name}</Tag>}<Tag color={runState==="Succeeded"?"green":runState==="Failed"?"red":"blue"}>{runState}</Tag></Space>}
      open={runOpen} width="75%" onClose={()=>setRunOpen(false)}
      extra={runState==="Running" && activeRun ? <Button danger icon={<StopOutlined/>} onClick={()=>run(()=>api(`/api/runs/stop?namespace=${namespace}&name=${activeRun.name}`,{method:"DELETE"}),"Run stopped")}>Stop</Button> : null}>
      <Progress percent={Math.round(progress)} status={runState==="Failed"?"exception":runState==="Succeeded"?"success":"active"} />
      <Row gutter={[16,16]} className="monitor-grid">
        <Col xs={24} lg={10}><Card size="small" title="Execution sequence">
          <Timeline items={[
            {color:runDetail?.job?"green":"blue",children:"Kubernetes Job created"},
            {color:podPhase?"green":"gray",children:podPhase ? `Pod scheduled: ${runPod?.spec?.nodeName || "pending node"}` : "Waiting for pod scheduling"},
            {color:podPhase==="Running"||runState==="Succeeded"||runState==="Failed"?"green":"blue",children:containerWaiting ? `Container: ${containerWaiting}` : "Runner container started"},
            ...configuredSteps.map((step)=>({color:completedSteps.includes(step)?"green":runState==="Failed"?"red":"gray",children:step})),
            {color:runState==="Succeeded"?"green":runState==="Failed"?"red":"gray",children:`Run ${runState.toLowerCase()}`},
          ]}/>
        </Card></Col>
        <Col xs={24} lg={14}><Card size="small" title="Kubernetes events">
          {(runDetail?.events || []).length ? <Timeline items={(runDetail?.events || []).slice(-10).map((event:any)=>({
            color:event.type==="Warning"?"red":"blue",
            children:<><strong>{event.reason}</strong><br/><Typography.Text type="secondary">{event.message}</Typography.Text></>
          }))}/> : <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="Waiting for events"/>}
        </Card></Col>
      </Row>
      {trafficResult && <Card size="small" title="TRex result" className="result-card">
        <Alert type={trafficResult.passed?"success":trafficResult.generator_saturated?"warning":"error"} showIcon
          message={trafficResult.passed?"Load test passed":trafficResult.generator_saturated?"Traffic generator saturated":"Load test failed"}
          description={trafficResult.reason}/>
        <Row gutter={[16,16]} className="result-grid">
          <Col xs={12} lg={6}><Statistic title="Verdict" value={trafficResult.passed?"PASS":"FAIL"} valueStyle={{color:trafficResult.passed?"#16a34a":"#dc2626"}}/></Col>
          <Col xs={12} lg={6}><Statistic title="Loss" value={trafficResult.loss_percent} precision={4} suffix="%"/></Col>
          <Col xs={12} lg={6}><Statistic title="TX packets" value={trafficResult.tx_packets}/></Col>
          <Col xs={12} lg={6}><Statistic title="RX packets" value={trafficResult.rx_packets}/></Col>
          <Col xs={12} lg={6}><Statistic title="Actual TX rate" value={formatPps(trafficResult.actual_tx_pps)}/></Col>
          <Col xs={12} lg={6}><Statistic title="Actual RX rate" value={formatPps(trafficResult.actual_rx_pps)}/></Col>
          <Col xs={12} lg={6}><Statistic title="N3 TX wire rate" value={trafficResult.tx_l1_mbps} precision={2} suffix="Mbps"/></Col>
          <Col xs={12} lg={6}><Statistic title="N6 RX wire rate" value={trafficResult.rx_l1_mbps} precision={2} suffix="Mbps"/></Col>
          <Col xs={12} lg={6}><Statistic title="Lost packets" value={trafficResult.lost_packets}/></Col>
          <Col xs={12} lg={6}><Statistic title="Unclassified RX" value={trafficResult.unclassified_rx_packets||0}/></Col>
          <Col xs={12} lg={6}><Statistic title="Peak TRex CPU" value={trafficResult.peak_cpu_percent} precision={1} suffix="%"/></Col>
          <Col xs={12} lg={6}><Statistic title="Port errors" value={(trafficResult.tx_errors||0)+(trafficResult.rx_errors||0)}/></Col>
          <Col xs={12} lg={6}><Statistic title="Queue full events" value={trafficResult.queue_full||0}/></Col>
          <Col xs={12} lg={6}><Statistic title="Queue budget" value={trafficResult.queue_full_budget||0}/></Col>
          <Col xs={12} lg={6}><Statistic title="Run wall time" value={trafficResult.run_elapsed_seconds||0} precision={1} suffix="s"/></Col>
        </Row>
        <Descriptions bordered size="small" column={2}>
          <Descriptions.Item label="Requested profile">{formatPps(trafficResult.requested_pps)} for {trafficResult.duration_seconds}s</Descriptions.Item>
          <Descriptions.Item label="Frame size">{trafficResult.packet_size} bytes, excluding FCS</Descriptions.Item>
          <Descriptions.Item label="Expected packets">{trafficResult.expected_packets?.toLocaleString()}</Descriptions.Item>
          <Descriptions.Item label="Loss threshold">{trafficResult.max_loss_percent}%</Descriptions.Item>
          <Descriptions.Item label="N3 TX L2 throughput">{trafficResult.tx_l2_mbps?.toFixed(2)} Mbps</Descriptions.Item>
          <Descriptions.Item label="N6 RX L2 throughput">{trafficResult.rx_l2_mbps?.toFixed(2)} Mbps</Descriptions.Item>
          <Descriptions.Item label="Wire-rate comparison" span={2}>N3 TX includes outer IPv4, UDP, and GTP-U headers. N6 RX is measured after the UPF removes that 36-byte encapsulation, so its Mbps value is normally lower at the same packet rate.</Descriptions.Item>
          <Descriptions.Item label="RX accounting" span={2}>RX above transmitted packets is reported separately as unclassified port traffic and is not counted as forwarded test traffic.</Descriptions.Item>
          <Descriptions.Item label="Pre-run drain" span={2}>{(trafficResult.drained_rx_packets||0).toLocaleString()} residual packets observed before counters were cleared.</Descriptions.Item>
          <Descriptions.Item label="First threshold breach" span={2}>{trafficResult.first_loss_threshold_seconds == null ? "None" : `${trafficResult.first_loss_threshold_seconds}s`}</Descriptions.Item>
        </Descriptions>
        <Divider>Time-series samples</Divider>
        <Table size="small" pagination={{pageSize:10}} scroll={{x:900}} rowKey="elapsed_seconds"
          dataSource={trafficResult.samples || []} columns={[
            {title:"Elapsed",dataIndex:"elapsed_seconds",render:(v)=>`${v}s`},
            {title:"TX rate",dataIndex:"tx_pps",render:(v)=>formatPps(v)},
            {title:"RX rate",dataIndex:"rx_pps",render:(v)=>formatPps(v)},
            {title:"Cumulative loss",dataIndex:"loss_percent",render:(v)=><Tag color={v>trafficResult.max_loss_percent?"red":"green"}>{Number(v).toFixed(4)}%</Tag>},
            {title:"N3 TX L1",dataIndex:"tx_l1_bps",render:(v)=>`${((v||0)/1000000).toFixed(2)} Mbps`},
            {title:"N6 RX L1",dataIndex:"rx_l1_bps",render:(v)=>`${((v||0)/1000000).toFixed(2)} Mbps`},
            {title:"TRex CPU",dataIndex:"cpu_util_percent",render:(v)=>`${Number(v||0).toFixed(1)}%`},
            {title:"Errors",render:(_,r:any)=>(r.tx_errors||0)+(r.rx_errors||0)},
          ]}/>
      </Card>}
      <Card size="small" title="Live runner output"><pre className="logs">{runDetail?.logs || `Waiting for runner output${containerWaiting ? ` (${containerWaiting})` : ""}...`}</pre></Card>
    </Drawer>
  </Layout>;
}

function firstUE(cidr: string) {
  const [address] = cidr.split("/");
  const parts = address.split(".").map(Number);
  if (parts.length !== 4 || parts.some((part) => !Number.isInteger(part) || part < 0 || part > 255)) return address;
  for (let index = 3; index >= 0; index--) {
    parts[index]++;
    if (parts[index] <= 255) break;
    parts[index] = 0;
  }
  return parts.join(".");
}

function Root() {
  return <ConfigProvider theme={{algorithm:theme.defaultAlgorithm,token:{colorPrimary:"#0f766e",borderRadius:8}}}><AntApp><Refine><Console /></Refine></AntApp></ConfigProvider>;
}

ReactDOM.createRoot(document.getElementById("root")!).render(<React.StrictMode><Root /></React.StrictMode>);
