import React, { useEffect, useMemo, useState } from "react";
import ReactDOM from "react-dom/client";
import { Refine } from "@refinedev/core";
import {
  Alert, App as AntApp, Button, Card, Col, ConfigProvider, Descriptions, Divider, Drawer,
  Form, Input, InputNumber, Layout, List, Modal, Row, Space, Statistic, Table, Tabs, Tag,
  Typography, message, theme,
} from "antd";
import {
  CloudServerOutlined, DashboardOutlined, DeleteOutlined, PlayCircleOutlined,
  ReloadOutlined, RocketOutlined, StopOutlined,
} from "@ant-design/icons";
import "antd/dist/reset.css";
import "./styles.css";

type Config = { chart: string; chartVersion: string; defaultRelease: string; defaultNamespace: string; authenticationEnabled: boolean };
type Job = { metadata: { name: string; creationTimestamp: string; labels?: Record<string,string> }; status?: { active?: number; succeeded?: number; failed?: number } };

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
  const [busy, setBusy] = useState(false);
  const [logs, setLogs] = useState("");
  const [logOpen, setLogOpen] = useState(false);
  const [installForm] = Form.useForm();
  const [sessionForm] = Form.useForm();
  const [trafficForm] = Form.useForm();
  const [msg, holder] = message.useMessage();

  const refresh = async () => {
    try {
      const [s, r] = await Promise.all([
        api(`/api/status?release=${release}&namespace=${namespace}`),
        api(`/api/runs?namespace=${namespace}`).catch(() => ({ items: [] })),
      ]);
      setStatus(s); setJobs(r.items || []);
    } catch (e: any) { msg.error(e.message); }
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

  const run = async (fn: () => Promise<any>, ok: string) => {
    setBusy(true);
    try { await fn(); msg.success(ok); await refresh(); }
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

  const showLogs = async (name: string) => {
    try { const data = await api(`/api/runs/logs?namespace=${namespace}&name=${name}`); setLogs(data.logs); setLogOpen(true); }
    catch (e: any) { msg.error(e.message); }
  };

  return <Layout className="shell">
    {holder}
    <Layout.Sider width={245} theme="dark" className="sider">
      <div className="brand"><RocketOutlined /><div><strong>UPF Load Test</strong><small>Travelping + TRex</small></div></div>
      <List className="nav" dataSource={[
        [<DashboardOutlined />, "Dashboard"], [<CloudServerOutlined />, "Environment"],
        [<PlayCircleOutlined />, "Test runners"],
      ]} renderItem={(item) => <List.Item>{item[0]}<span>{item[1]}</span></List.Item>} />
      <div className="chart-ref"><small>Workload chart</small><code>{config?.chartVersion || "..."}</code></div>
    </Layout.Sider>
    <Layout>
      <Layout.Header className="header">
        <div><Typography.Title level={3}>Load-test control plane</Typography.Title><Typography.Text type="secondary">{namespace}/{release}</Typography.Text></div>
        <Button icon={<ReloadOutlined />} onClick={refresh}>Refresh</Button>
      </Layout.Header>
      <Layout.Content className="content">
        <Alert type="info" showIcon message={config?.chart} description="The controller pulls this public OCI chart without registry credentials." />
        <Row gutter={[16,16]} className="stats">
          <Col xs={24} md={8}><Card><Statistic title="Environment" value={status?.installed ? "Installed" : "Not installed"} valueStyle={{color: status?.installed ? "#16a34a" : "#dc2626"}} /></Card></Col>
          <Col xs={24} md={8}><Card><Statistic title="Ready pods" value={ready} suffix={`/ ${pods.length}`} /></Card></Col>
          <Col xs={24} md={8}><Card><Statistic title="Recorded runs" value={jobs.length} /></Card></Col>
        </Row>
        <Tabs defaultActiveKey="environment" items={[
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
            <Form form={sessionForm} layout="vertical" initialValues={{count:1000,baseId:1,uePool:"48.0.0.0/8",qfi:9,gnbAddr:"10.0.3.1",upfN3Addr:"10.0.3.10",upfAddr:"10.0.4.9:8805"}}
              onFinish={(v)=>run(()=>api("/api/sessions",{method:"POST",body:JSON.stringify({...v,release,namespace})}),"PFCP injection started")}>
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
            <Form form={trafficForm} layout="vertical" initialValues={{pps:100000,duration:15,packetSize:96,sessionCount:1000,teidStart:1,teidStep:10,ueStart:"48.0.0.1",innerDst:"10.0.5.1",maxLossPercent:0.1}}
              onFinish={(v)=>run(()=>api("/api/traffic",{method:"POST",body:JSON.stringify({...v,release,namespace})}),"TRex run started")}>
              <Row gutter={16}>
                <Col xs={12} md={6}><Form.Item name="pps" label="Packets / second"><InputNumber min={1}/></Form.Item></Col>
                <Col xs={12} md={6}><Form.Item name="duration" label="Duration (seconds)"><InputNumber min={1} max={86400}/></Form.Item></Col>
                <Col xs={12} md={6}><Form.Item name="packetSize" label="Frame size (no FCS)"><InputNumber min={78} max={9000}/></Form.Item></Col>
                <Col xs={12} md={6}><Form.Item name="sessionCount" label="Session count"><InputNumber min={1}/></Form.Item></Col>
                <Col xs={12} md={6}><Form.Item name="teidStart" label="TEID start"><InputNumber min={1}/></Form.Item></Col>
                <Col xs={12} md={6}><Form.Item name="teidStep" label="TEID step"><InputNumber min={1}/></Form.Item></Col>
                <Col xs={12} md={6}><Form.Item name="ueStart" label="First UE address"><Input /></Form.Item></Col>
                <Col xs={12} md={6}><Form.Item name="innerDst" label="Inner destination"><Input /></Form.Item></Col>
                <Col xs={12} md={6}><Form.Item name="maxLossPercent" label="Maximum loss (%)"><InputNumber min={0} max={100} step={0.1}/></Form.Item></Col>
              </Row>
              <Button type="primary" htmlType="submit" icon={<PlayCircleOutlined />} loading={busy} disabled={!status?.installed}>Start traffic</Button>
            </Form>
          </Card> },
          { key: "history", label: "Run history", children: <Card>
            <Table dataSource={jobRows} pagination={{pageSize:10}} columns={[
              {title:"Run",dataIndex:"name"}, {title:"Type",dataIndex:"type",render:(v)=><Tag>{v}</Tag>},
              {title:"Created",dataIndex:"created"}, {title:"State",dataIndex:"state",render:(v)=><Tag color={v==="Succeeded"?"green":v==="Failed"?"red":"blue"}>{v}</Tag>},
              {title:"Actions",render:(_,row:any)=><Space><Button size="small" onClick={()=>showLogs(row.name)}>Logs</Button>
                {row.state==="Running"&&<Button danger size="small" icon={<StopOutlined />} onClick={()=>run(()=>api(`/api/runs/stop?namespace=${namespace}&name=${row.name}`,{method:"DELETE"}),"Run stopped")}>Stop</Button>}</Space>}
            ]} />
          </Card> },
        ]} />
      </Layout.Content>
    </Layout>
    <Drawer title="Run logs" open={logOpen} width="70%" onClose={()=>setLogOpen(false)}><pre className="logs">{logs}</pre></Drawer>
  </Layout>;
}

function Root() {
  return <ConfigProvider theme={{algorithm:theme.defaultAlgorithm,token:{colorPrimary:"#0f766e",borderRadius:8}}}><AntApp><Refine><Console /></Refine></AntApp></ConfigProvider>;
}

ReactDOM.createRoot(document.getElementById("root")!).render(<React.StrictMode><Root /></React.StrictMode>);
