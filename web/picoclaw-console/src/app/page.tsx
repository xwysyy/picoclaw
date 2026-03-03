"use client";

import { useEffect, useMemo, useRef, useState } from "react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { ScrollArea } from "@/components/ui/scroll-area";
import { Separator } from "@/components/ui/separator";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Textarea } from "@/components/ui/textarea";

import {
  Activity,
  Clock,
  Copy,
  Download,
  FileText,
  RefreshCw,
  Search,
  Send,
  SquareArrowOutUpRight,
} from "lucide-react";

const LS_KEY = "picoclaw.console.api_key";

type ConsoleTraceItem = {
  token: string;
  session_key?: string;
  channel?: string;
  chat_id?: string;
  run_id?: string;
  events_path: string;
  events_size_bytes?: number;
  mod_time?: string;
  last_event_type?: string;
  last_event_ts?: string;
};

type ConsoleSessionsItem = {
  key: string;
  summary?: string;
  created?: string;
  updated?: string;
  file?: string;
};

type TailResponse = {
  ok: boolean;
  path?: string;
  lines_requested?: number;
  truncated?: boolean;
  lines?: string[];
  error?: string;
};

function buildHeaders(apiKey: string) {
  const headers: Record<string, string> = {};
  const k = (apiKey || "").trim();
  if (k) headers["Authorization"] = `Bearer ${k}`;
  return headers;
}

async function apiJSON<T>(path: string, apiKey: string): Promise<T> {
  const res = await fetch(path, { headers: buildHeaders(apiKey) });
  if (!res.ok) {
    const text = await res.text();
    throw new Error(`${res.status} ${res.statusText}\n${text}`);
  }
  return (await res.json()) as T;
}

function pretty(v: any) {
  try {
    return JSON.stringify(v, null, 2);
  } catch {
    return String(v);
  }
}

function fmtBytes(n?: number) {
  if (!n || !Number.isFinite(n) || n <= 0) return "-";
  const units = ["B", "KiB", "MiB", "GiB"];
  let v = n;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  const digits = v >= 10 || i === 0 ? 0 : 1;
  return `${v.toFixed(digits)} ${units[i]}`;
}

function fmtDurationMs(ms?: number) {
  if (ms == null || !Number.isFinite(ms) || ms < 0) return "-";
  if (ms < 1000) return `${Math.round(ms)}ms`;
  const s = ms / 1000;
  if (s < 60) return `${s.toFixed(1)}s`;
  const m = Math.floor(s / 60);
  const rs = Math.round(s - m * 60);
  return `${m}m${rs.toString().padStart(2, "0")}s`;
}

function fmtDateTimeMs(ms?: number) {
  if (!ms || !Number.isFinite(ms) || ms <= 0) return "-";
  try {
    const d = new Date(ms);
    return d.toLocaleString();
  } catch {
    return String(ms);
  }
}

function fmtISO(iso?: string) {
  const s = (iso || "").trim();
  if (!s) return "-";
  try {
    const d = new Date(s);
    return d.toLocaleString();
  } catch {
    return s;
  }
}

function safeCopy(text: string) {
  const t = (text || "").trim();
  if (!t) return;
  try {
    void navigator.clipboard.writeText(t);
  } catch {
    // ignore
  }
}

export default function Home() {
  const [apiKey, setApiKey] = useState<string>("");
  const [savedKey, setSavedKey] = useState<string>("");

  const [health, setHealth] = useState<any>(null);
  const [ready, setReady] = useState<any>(null);

  const [status, setStatus] = useState<any>(null);
  const [cron, setCron] = useState<any>(null);
  const [runs, setRuns] = useState<{ items?: ConsoleTraceItem[] } | null>(null);
  const [tools, setTools] = useState<{ items?: ConsoleTraceItem[] } | null>(null);
  const [sessions, setSessions] = useState<{ items?: ConsoleSessionsItem[] } | null>(null);

  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string>("");
  const [lastRefreshISO, setLastRefreshISO] = useState<string>("");

  const [autoRefresh, setAutoRefresh] = useState(false);
  const [autoRefreshSeconds, setAutoRefreshSeconds] = useState<string>("10");

  const [viewerOpen, setViewerOpen] = useState(false);
  const [viewerTitle, setViewerTitle] = useState<string>("");
  const [viewerPath, setViewerPath] = useState<string>("");
  const [viewerResp, setViewerResp] = useState<TailResponse | null>(null);
  const [viewerQuery, setViewerQuery] = useState<string>("");
  const [viewerMode, setViewerMode] = useState<"structured" | "raw">("structured");

  const [cronDetailOpen, setCronDetailOpen] = useState(false);
  const [cronDetailJob, setCronDetailJob] = useState<any>(null);

  const [sessionDetailOpen, setSessionDetailOpen] = useState(false);
  const [sessionDetail, setSessionDetail] = useState<ConsoleSessionsItem | null>(null);

  const [cronQuery, setCronQuery] = useState<string>("");
  const [cronOnlyProblems, setCronOnlyProblems] = useState<boolean>(false);

  const [sessionsQuery, setSessionsQuery] = useState<string>("");
  const [traceQuery, setTraceQuery] = useState<string>("");

  const didInitRef = useRef(false);

  useEffect(() => {
    if (didInitRef.current) return;
    didInitRef.current = true;

    let key = "";
    try {
      key = localStorage.getItem(LS_KEY) || "";
    } catch {
      // ignore
    }
    setApiKey(key);
    setSavedKey(key);
    void refreshAll(key);
  }, []);

  const authBadge = useMemo(() => {
    const hasKey = (apiKey || "").trim().length > 0;
    return hasKey ? <Badge>auth: bearer</Badge> : <Badge variant="secondary">auth: loopback</Badge>;
  }, [apiKey]);

  const healthBadge = useMemo(() => {
    const ok = health?.status === "ok";
    return ok ? <Badge className="gap-1"><Activity className="h-3 w-3" />health</Badge> : <Badge variant="secondary">health: ?</Badge>;
  }, [health]);

  const readyBadge = useMemo(() => {
    const ok = ready?.status === "ready";
    return ok ? <Badge className="gap-1"><Clock className="h-3 w-3" />ready</Badge> : <Badge variant="secondary">ready: ?</Badge>;
  }, [ready]);

  useEffect(() => {
    if (!autoRefresh) return;
    const sec = Math.max(2, Math.min(300, parseInt(autoRefreshSeconds || "10", 10) || 10));
    const id = setInterval(() => {
      if (!busy) void refreshAll();
    }, sec * 1000);
    return () => clearInterval(id);
  }, [autoRefresh, autoRefreshSeconds, apiKey, busy]);

  async function refreshAll(apiKeyOverride?: string) {
    setErr("");
    setBusy(true);
    const k = apiKeyOverride != null ? apiKeyOverride : apiKey;

    // Fetch health/ready even when API key is missing or invalid. These endpoints
    // are unauthenticated and give useful "is it up?" signals.
    try {
      const [h, r] = await Promise.all([apiJSON<any>("/health", k), apiJSON<any>("/ready", k)]);
      setHealth(h);
      setReady(r);
    } catch {
      // ignore health/ready errors
    }

    try {
      const [st, cr, ru, to, se] = await Promise.all([
        apiJSON<any>("/api/console/status", k),
        apiJSON<any>("/api/console/cron", k),
        apiJSON<any>("/api/console/runs", k),
        apiJSON<any>("/api/console/tools", k),
        apiJSON<any>("/api/console/sessions", k),
      ]);
      setStatus(st);
      setCron(cr);
      setRuns(ru);
      setTools(to);
      setSessions(se);
    } catch (e: any) {
      setErr(e?.message ? String(e.message) : String(e));
    } finally {
      setLastRefreshISO(new Date().toISOString());
      setBusy(false);
    }
  }

  function saveKey() {
    try {
      localStorage.setItem(LS_KEY, apiKey);
      setSavedKey(apiKey);
    } catch {
      // ignore
    }
  }

  function clearKey() {
    try {
      localStorage.removeItem(LS_KEY);
    } catch {
      // ignore
    }
    setApiKey("");
    setSavedKey("");
  }

  async function download(path: string) {
    setErr("");
    try {
      const url = `/api/console/file?path=${encodeURIComponent(path)}`;
      const res = await fetch(url, { headers: buildHeaders(apiKey) });
      if (!res.ok) {
        const text = await res.text();
        throw new Error(`${res.status} ${res.statusText}\n${text}`);
      }
      const blob = await res.blob();
      const a = document.createElement("a");
      a.href = URL.createObjectURL(blob);
      a.download = path.split("/").pop() || "download";
      document.body.appendChild(a);
      a.click();
      a.remove();
      setTimeout(() => URL.revokeObjectURL(a.href), 1000);
    } catch (e: any) {
      setErr(e?.message ? String(e.message) : String(e));
    }
  }

  async function openTail(path: string) {
    setErr("");
    setViewerTitle("Tail");
    setViewerPath(path);
    setViewerResp(null);
    setViewerQuery("");
    setViewerMode("structured");
    setViewerOpen(true);
    try {
      const resp = await apiJSON<TailResponse>(`/api/console/tail?path=${encodeURIComponent(path)}&lines=200`, apiKey);
      setViewerResp(resp);
    } catch (e: any) {
      setErr(e?.message ? String(e.message) : String(e));
    }
  }

  async function openEvents(path: string) {
    setErr("");
    setViewerTitle("Events");
    setViewerPath(path);
    setViewerResp(null);
    setViewerQuery("");
    setViewerMode("structured");
    setViewerOpen(true);
    try {
      const resp = await apiJSON<TailResponse>(`/api/console/tail?path=${encodeURIComponent(path)}&lines=260`, apiKey);
      setViewerResp(resp);
    } catch (e: any) {
      setErr(e?.message ? String(e.message) : String(e));
    }
  }

  async function sendTestNotify() {
    setErr("");
    try {
      const res = await fetch("/api/notify", {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          ...buildHeaders(apiKey),
        },
        body: JSON.stringify({
          content: `PicoClaw Console: test notify (${new Date().toLocaleString()})`,
        }),
      });
      if (!res.ok) {
        const text = await res.text();
        throw new Error(`${res.status} ${res.statusText}\n${text}`);
      }
    } catch (e: any) {
      setErr(e?.message ? String(e.message) : String(e));
    }
  }

  const headerInfo = status?.info || {};
  const lastActive = status?.last_active || {};

  const filteredJobs = useMemo(() => {
    const list = Array.isArray(cron?.jobs) ? cron.jobs : [];
    const q = cronQuery.trim().toLowerCase();
    return list.filter((j: any) => {
      if (cronOnlyProblems) {
        const st = String(j?.state?.lastStatus || "").toLowerCase();
        if (st && st !== "ok") {
          // ok
        } else if (j?.state?.lastError) {
          // ok
        } else {
          return false;
        }
      }
      if (!q) return true;
      const hay = [j?.id, j?.name, j?.payload?.kind, j?.schedule?.kind, j?.schedule?.expr, j?.payload?.channel, j?.payload?.to]
        .filter(Boolean)
        .map((v: any) => String(v).toLowerCase())
        .join(" ");
      return hay.includes(q);
    });
  }, [cron, cronQuery, cronOnlyProblems]);

  const filteredSessions = useMemo(() => {
    const list = Array.isArray(sessions?.items) ? sessions.items : [];
    const q = sessionsQuery.trim().toLowerCase();
    if (!q) return list;
    return list.filter((s) => {
      const hay = [s.key, s.summary, s.created, s.updated, s.file]
        .filter(Boolean)
        .map((v) => String(v).toLowerCase())
        .join(" ");
      return hay.includes(q);
    });
  }, [sessions, sessionsQuery]);

  const filteredRuns = useMemo(() => {
    const list = Array.isArray(runs?.items) ? runs.items : [];
    const q = traceQuery.trim().toLowerCase();
    if (!q) return list;
    return list.filter((it) => {
      const hay = [it.session_key, it.token, it.channel, it.chat_id, it.last_event_type, it.last_event_ts, it.events_path]
        .filter(Boolean)
        .map((v) => String(v).toLowerCase())
        .join(" ");
      return hay.includes(q);
    });
  }, [runs, traceQuery]);

  const filteredTools = useMemo(() => {
    const list = Array.isArray(tools?.items) ? tools.items : [];
    const q = traceQuery.trim().toLowerCase();
    if (!q) return list;
    return list.filter((it) => {
      const hay = [it.session_key, it.token, it.channel, it.chat_id, it.last_event_type, it.last_event_ts, it.events_path]
        .filter(Boolean)
        .map((v) => String(v).toLowerCase())
        .join(" ");
      return hay.includes(q);
    });
  }, [tools, traceQuery]);

  const viewerLines = useMemo(() => {
    return Array.isArray(viewerResp?.lines) ? viewerResp!.lines!.map((s) => String(s)) : [];
  }, [viewerResp]);

  const viewerParsed = useMemo(() => {
    const parsed: { raw: string; obj?: any; err?: string }[] = [];
    for (const line of viewerLines) {
      const t = (line || "").trim();
      if (!t) continue;
      try {
        parsed.push({ raw: line, obj: JSON.parse(t) });
      } catch (e: any) {
        parsed.push({ raw: line, err: e?.message ? String(e.message) : "parse error" });
      }
    }
    return parsed;
  }, [viewerLines]);

  const viewerHasStructured = useMemo(() => viewerParsed.some((p) => p.obj != null), [viewerParsed]);

  const viewerFiltered = useMemo(() => {
    const q = viewerQuery.trim().toLowerCase();
    if (!q) return viewerParsed;
    return viewerParsed.filter((p) => {
      const hay = (p.raw || "").toLowerCase();
      return hay.includes(q);
    });
  }, [viewerParsed, viewerQuery]);

  return (
    <div className="min-h-screen bg-background text-foreground">
      <div className="mx-auto max-w-6xl px-4 py-6">
        <div className="flex flex-col gap-3 md:flex-row md:items-end md:justify-between">
          <div className="flex flex-col gap-1">
            <div className="flex items-center gap-2">
              <div className="text-xl font-semibold tracking-tight">PicoClaw Console</div>
              <div className="flex flex-wrap items-center gap-2">
                {authBadge}
                {healthBadge}
                {readyBadge}
              </div>
            </div>
            <div className="text-sm text-muted-foreground">
              A lightweight read-only operations console. Data via <code>/api/console/*</code>.
            </div>
            <div className="text-xs text-muted-foreground">
              {lastRefreshISO ? (
                <>
                  last refresh: <code>{fmtISO(lastRefreshISO)}</code>
                </>
              ) : (
                <>
                  tip: click <code>Refresh</code> or enable auto-refresh
                </>
              )}
            </div>
          </div>

          <div className="flex flex-col gap-2 md:items-end">
            <div className="flex flex-wrap items-center gap-2">
              <Button variant={autoRefresh ? "default" : "outline"} onClick={() => setAutoRefresh((v) => !v)}>
                <RefreshCw className="mr-2 h-4 w-4" />
                Auto {autoRefresh ? "On" : "Off"}
              </Button>
              <div className="flex items-center gap-2 rounded-md border bg-card px-2 py-1">
                <span className="text-xs text-muted-foreground">sec</span>
                <Input
                  value={autoRefreshSeconds}
                  onChange={(e) => setAutoRefreshSeconds(e.target.value)}
                  className="h-8 w-[70px]"
                  inputMode="numeric"
                />
              </div>
              <Button onClick={() => refreshAll()} disabled={busy}>
                <RefreshCw className="mr-2 h-4 w-4" />
                {busy ? "Refreshing..." : "Refresh"}
              </Button>
            </div>
            <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
              <span>
                uptime: <code>{health?.uptime || "-"}</code>
              </span>
              <span className="hidden md:inline">·</span>
              <span>
                now: <code>{fmtISO(status?.now)}</code>
              </span>
            </div>
          </div>
        </div>

        <Separator className="my-4" />

        <Card>
          <CardHeader className="pb-2">
            <CardTitle className="text-base">Access</CardTitle>
          </CardHeader>
          <CardContent className="flex flex-col gap-3">
            <div className="flex flex-col gap-3 md:flex-row md:items-center">
              <div className="flex-1">
                <Input
                  type="password"
                  placeholder="gateway.api_key (optional on loopback)"
                  value={apiKey}
                  onChange={(e) => setApiKey(e.target.value)}
                />
                <div className="mt-1 flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
                  <span>
                    saved: <code>{savedKey ? "yes" : "no"}</code>
                  </span>
                  <span>·</span>
                  <span>
                    header: <code>Authorization: Bearer</code>
                  </span>
                </div>
              </div>
              <div className="flex flex-wrap gap-2">
                <Button onClick={saveKey} variant="default">
                  Save
                </Button>
                <Button onClick={clearKey} variant="outline">
                  Clear
                </Button>
                <Button onClick={sendTestNotify} variant="secondary">
                  <Send className="mr-2 h-4 w-4" />
                  Test notify
                </Button>
              </div>
            </div>
            <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
              <span>
                last_active: <code>{lastActive?.channel || "-"}</code> / <code>{lastActive?.chat_id || "-"}</code>
              </span>
              <Button
                type="button"
                size="sm"
                variant="ghost"
                className="h-7 px-2"
                onClick={() => safeCopy(`${lastActive?.channel || ""}:${lastActive?.chat_id || ""}`)}
              >
                <Copy className="h-3.5 w-3.5" />
              </Button>
              <span className="hidden md:inline">·</span>
              <span>
                state: <code>{lastActive?.state?.timestamp ? fmtISO(lastActive.state.timestamp) : "-"}</code>
              </span>
            </div>
          </CardContent>
        </Card>

        {err ? (
          <div className="mt-4 rounded-md border border-destructive/40 bg-destructive/10 p-3 text-sm text-destructive">
            <div className="font-medium">Error</div>
            <pre className="mt-1 whitespace-pre-wrap text-xs">{err}</pre>
          </div>
        ) : null}

        <Tabs defaultValue="overview" className="mt-6">
          <TabsList>
            <TabsTrigger value="overview">Overview</TabsTrigger>
            <TabsTrigger value="cron">Cron</TabsTrigger>
            <TabsTrigger value="sessions">Sessions</TabsTrigger>
            <TabsTrigger value="traces">Traces</TabsTrigger>
            <TabsTrigger value="raw">Raw</TabsTrigger>
          </TabsList>

          <TabsContent value="overview" className="mt-4 space-y-4">
            <div className="grid gap-4 md:grid-cols-4">
              <Card>
                <CardHeader className="pb-2">
                  <CardTitle className="flex items-center gap-2 text-sm">
                    <Activity className="h-4 w-4" /> Model
                  </CardTitle>
                </CardHeader>
                <CardContent className="text-sm">
                  <div className="font-medium">{headerInfo?.model || "-"}</div>
                  <div className="mt-1 text-xs text-muted-foreground">
                    notify.on_task_complete: {headerInfo?.notify_on_task_complete ? "true" : "false"}
                  </div>
                </CardContent>
              </Card>

              <Card>
                <CardHeader className="pb-2">
                  <CardTitle className="flex items-center gap-2 text-sm">
                    <FileText className="h-4 w-4" /> Workspace
                  </CardTitle>
                </CardHeader>
                <CardContent className="text-sm">
                  <div className="break-all font-medium">{status?.workspace || "-"}</div>
                  <div className="mt-1 text-xs text-muted-foreground">
                    runs: {status?.runs?.sessions ?? 0} · tools: {status?.tools?.sessions ?? 0} · cron:{" "}
                    {status?.cron?.jobs ?? 0}
                  </div>
                </CardContent>
              </Card>

              <Card>
                <CardHeader className="pb-2">
                  <CardTitle className="flex items-center gap-2 text-sm">
                    <Clock className="h-4 w-4" /> Last active
                  </CardTitle>
                </CardHeader>
                <CardContent className="text-sm">
                  <div className="font-medium">
                    {lastActive?.channel || "-"} / {lastActive?.chat_id || "-"}
                  </div>
                  <div className="mt-1 text-xs text-muted-foreground">
                    now: {status?.now || "-"}
                  </div>
                </CardContent>
              </Card>

              <Card>
                <CardHeader className="pb-2">
                  <CardTitle className="flex items-center gap-2 text-sm">
                    <SquareArrowOutUpRight className="h-4 w-4" /> Links
                  </CardTitle>
                </CardHeader>
                <CardContent className="flex flex-wrap gap-2">
                  <Button variant="outline" size="sm" onClick={() => window.open("/health", "_blank")}>
                    Health
                  </Button>
                  <Button variant="outline" size="sm" onClick={() => window.open("/ready", "_blank")}>
                    Ready
                  </Button>
                  <Button variant="outline" size="sm" onClick={() => window.open("/api/notify", "_blank")}>
                    Notify
                  </Button>
                </CardContent>
              </Card>
            </div>

            <Card>
              <CardHeader className="pb-2">
                <CardTitle className="text-sm">Highlights</CardTitle>
              </CardHeader>
              <CardContent>
                <div className="grid gap-3 md:grid-cols-2">
                  <div className="rounded-md border p-3">
                    <div className="text-xs font-medium text-muted-foreground">Gateway</div>
                    <div className="mt-2 flex flex-col gap-1 text-sm">
                      <div className="flex items-center justify-between gap-2">
                        <span className="text-muted-foreground">health</span>
                        <code>{health?.status || "-"}</code>
                      </div>
                      <div className="flex items-center justify-between gap-2">
                        <span className="text-muted-foreground">ready</span>
                        <code>{ready?.status || "-"}</code>
                      </div>
                      <div className="flex items-center justify-between gap-2">
                        <span className="text-muted-foreground">uptime</span>
                        <code>{health?.uptime || "-"}</code>
                      </div>
                    </div>
                  </div>

                  <div className="rounded-md border p-3">
                    <div className="text-xs font-medium text-muted-foreground">Features</div>
                    <div className="mt-2 flex flex-col gap-1 text-sm">
                      <div className="flex items-center justify-between gap-2">
                        <span className="text-muted-foreground">tool trace</span>
                        <code>{headerInfo?.tool_trace_enabled ? "enabled" : "disabled"}</code>
                      </div>
                      <div className="flex items-center justify-between gap-2">
                        <span className="text-muted-foreground">run trace</span>
                        <code>{headerInfo?.run_trace_enabled ? "enabled" : "disabled"}</code>
                      </div>
                      <div className="flex items-center justify-between gap-2">
                        <span className="text-muted-foreground">web evidence</span>
                        <code>{headerInfo?.web_evidence_mode ? "enabled" : "disabled"}</code>
                      </div>
                      <div className="flex items-center justify-between gap-2">
                        <span className="text-muted-foreground">inbound queue</span>
                        <code>{headerInfo?.inbound_queue_enabled ? `on (${headerInfo?.inbound_queue_max_concurrency || 1})` : "off"}</code>
                      </div>
                    </div>
                  </div>
                </div>
              </CardContent>
            </Card>
          </TabsContent>

          <TabsContent value="cron" className="mt-4 space-y-4">
            <Card>
              <CardHeader className="pb-2">
                <CardTitle className="text-sm">Cron jobs</CardTitle>
              </CardHeader>
              <CardContent>
                <div className="flex flex-col gap-2 md:flex-row md:items-center md:justify-between">
                  <div className="text-xs text-muted-foreground">
                    store: <code>{cron?.path || "cron/jobs.json"}</code>
                    {typeof cron?.version !== "undefined" ? (
                      <>
                        {" "}
                        · version: <code>{String(cron.version)}</code>
                      </>
                    ) : null}
                  </div>
                  <div className="flex flex-wrap items-center gap-2">
                    <div className="relative">
                      <Search className="pointer-events-none absolute left-2 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
                      <Input
                        value={cronQuery}
                        onChange={(e) => setCronQuery(e.target.value)}
                        placeholder="Filter jobs…"
                        className="h-9 w-[240px] pl-8"
                      />
                    </div>
                    <Button
                      variant={cronOnlyProblems ? "default" : "outline"}
                      onClick={() => setCronOnlyProblems((v) => !v)}
                      className="h-9"
                    >
                      {cronOnlyProblems ? "Only problems" : "All jobs"}
                    </Button>
                    <Button variant="outline" onClick={() => download("cron/jobs.json")} className="h-9">
                      <Download className="mr-2 h-4 w-4" />
                      jobs.json
                    </Button>
                  </div>
                </div>

                <ScrollArea className="h-[420px] rounded-md border">
                  <Table>
                    <TableHeader>
                      <TableRow>
                        <TableHead>Name</TableHead>
                        <TableHead>Schedule</TableHead>
                        <TableHead>Deliver</TableHead>
                        <TableHead>Next</TableHead>
                        <TableHead>Last</TableHead>
                        <TableHead className="w-[220px]">Actions</TableHead>
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {filteredJobs.map((j: any) => (
                        <TableRow key={j.id}>
                          <TableCell className="align-top">
                            <div className="flex items-center justify-between gap-2">
                              <div className="font-medium">{j.name || j.id}</div>
                              <Button
                                type="button"
                                size="sm"
                                variant="ghost"
                                className="h-7 px-2"
                                onClick={() => safeCopy(String(j.id || ""))}
                                title="Copy job id"
                              >
                                <Copy className="h-3.5 w-3.5" />
                              </Button>
                            </div>
                            <div className="text-xs text-muted-foreground">
                              <code>{j.id}</code> · {j.enabled ? "enabled" : "disabled"}
                            </div>
                          </TableCell>
                          <TableCell className="align-top text-xs">
                            <div>
                              <code>{j.schedule?.kind || "-"}</code>
                            </div>
                            {j.schedule?.expr ? (
                              <div className="text-muted-foreground">
                                <code>
                                  {j.schedule.expr}
                                  {j.schedule?.tz ? ` (${j.schedule.tz})` : ""}
                                </code>
                              </div>
                            ) : null}
                            {j.schedule?.atMs ? (
                              <div className="text-muted-foreground">
                                <code>at: {fmtDateTimeMs(j.schedule.atMs)}</code>
                              </div>
                            ) : null}
                          </TableCell>
                          <TableCell className="align-top text-xs">
                            {j.payload?.deliver ? <Badge>deliver</Badge> : <Badge variant="secondary">no</Badge>}
                          </TableCell>
                          <TableCell className="align-top text-xs">
                            <code>{fmtDateTimeMs(j.state?.nextRunAtMs)}</code>
                          </TableCell>
                          <TableCell className="align-top text-xs">
                            <div>
                              <Badge
                                variant={
                                  j.state?.lastStatus === "ok"
                                    ? "default"
                                    : j.state?.lastStatus
                                      ? "destructive"
                                      : "secondary"
                                }
                              >
                                {j.state?.lastStatus || "-"}
                              </Badge>
                            </div>
                            <div className="mt-1 text-muted-foreground">
                              <code>{fmtDateTimeMs(j.state?.lastRunAtMs)}</code> · <code>{fmtDurationMs(j.state?.lastDurationMs)}</code>
                            </div>
                            {j.state?.lastError ? (
                              <div className="mt-1 line-clamp-2 text-destructive" title={String(j.state.lastError)}>
                                {String(j.state.lastError)}
                              </div>
                            ) : null}
                          </TableCell>
                          <TableCell className="align-top">
                            <div className="flex flex-wrap gap-2">
                              <Button
                                variant="secondary"
                                size="sm"
                                onClick={() => {
                                  setCronDetailJob(j);
                                  setCronDetailOpen(true);
                                }}
                              >
                                Details
                              </Button>
                              {j.state?.lastOutputPreview ? (
                                <Button
                                  variant="outline"
                                  size="sm"
                                  onClick={() => {
                                    setViewerTitle("Cron output preview");
                                    setViewerPath("cron/jobs.json");
                                    setViewerResp({
                                      ok: true,
                                      path: "cron/jobs.json",
                                      lines: String(j.state.lastOutputPreview).split("\n"),
                                      truncated: false,
                                    });
                                    setViewerQuery("");
                                    setViewerMode("raw");
                                    setViewerOpen(true);
                                  }}
                                >
                                  Preview
                                </Button>
                              ) : null}
                            </div>
                          </TableCell>
                        </TableRow>
                      ))}
                      {!filteredJobs.length ? (
                        <TableRow>
                          <TableCell colSpan={6} className="text-sm text-muted-foreground">
                            No jobs matched.
                          </TableCell>
                        </TableRow>
                      ) : null}
                    </TableBody>
                  </Table>
                </ScrollArea>
              </CardContent>
            </Card>
          </TabsContent>

          <TabsContent value="sessions" className="mt-4 space-y-4">
            <Card>
              <CardHeader className="pb-2">
                <CardTitle className="text-sm">Sessions</CardTitle>
              </CardHeader>
              <CardContent>
                <div className="mb-3 flex flex-col gap-2 md:flex-row md:items-center md:justify-between">
                  <div className="text-xs text-muted-foreground">
                    stored in <code>sessions/*.json</code>
                  </div>
                  <div className="relative">
                    <Search className="pointer-events-none absolute left-2 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
                    <Input
                      value={sessionsQuery}
                      onChange={(e) => setSessionsQuery(e.target.value)}
                      placeholder="Search sessions…"
                      className="h-9 w-[260px] pl-8"
                    />
                  </div>
                </div>
                <ScrollArea className="h-[420px] rounded-md border">
                  <Table>
                    <TableHeader>
                      <TableRow>
                        <TableHead>Key</TableHead>
                        <TableHead>Updated</TableHead>
                        <TableHead>Summary</TableHead>
                        <TableHead className="w-[220px]">Actions</TableHead>
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {filteredSessions.map((s) => (
                        <TableRow key={s.key}>
                          <TableCell className="align-top">
                            <div className="flex items-start justify-between gap-2">
                              <code className="break-all">{s.key}</code>
                              <Button
                                type="button"
                                size="sm"
                                variant="ghost"
                                className="h-7 px-2"
                                onClick={() => safeCopy(s.key)}
                                title="Copy session key"
                              >
                                <Copy className="h-3.5 w-3.5" />
                              </Button>
                            </div>
                          </TableCell>
                          <TableCell className="align-top text-xs">
                            <div>
                              <code>{fmtISO(s.updated)}</code>
                            </div>
                            <div className="text-muted-foreground">
                              <code>{fmtISO(s.created)}</code>
                            </div>
                          </TableCell>
                          <TableCell className="align-top text-xs text-muted-foreground">
                            {s.summary ? (
                              <span title={s.summary}>{s.summary.slice(0, 200)}</span>
                            ) : (
                              "-"
                            )}
                          </TableCell>
                          <TableCell className="align-top">
                            <div className="flex flex-wrap gap-2">
                              <Button
                                variant="secondary"
                                size="sm"
                                onClick={() => {
                                  setSessionDetail(s);
                                  setSessionDetailOpen(true);
                                }}
                              >
                                View
                              </Button>
                              {s.file ? (
                                <Button variant="outline" size="sm" onClick={() => download(s.file!)}>
                                  <Download className="mr-2 h-4 w-4" />
                                  JSON
                                </Button>
                              ) : null}
                            </div>
                          </TableCell>
                        </TableRow>
                      ))}
                      {!filteredSessions.length ? (
                        <TableRow>
                          <TableCell colSpan={4} className="text-sm text-muted-foreground">
                            No sessions matched.
                          </TableCell>
                        </TableRow>
                      ) : null}
                    </TableBody>
                  </Table>
                </ScrollArea>
              </CardContent>
            </Card>
          </TabsContent>

          <TabsContent value="traces" className="mt-4 space-y-4">
            <Card>
              <CardHeader className="pb-2">
                <CardTitle className="text-sm">Traces</CardTitle>
              </CardHeader>
              <CardContent>
                <div className="mb-3 flex flex-col gap-2 md:flex-row md:items-center md:justify-between">
                  <div className="text-xs text-muted-foreground">
                    <code>.picoclaw/audit/*/events.jsonl</code>
                  </div>
                  <div className="relative">
                    <Search className="pointer-events-none absolute left-2 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
                    <Input
                      value={traceQuery}
                      onChange={(e) => setTraceQuery(e.target.value)}
                      placeholder="Search traces…"
                      className="h-9 w-[260px] pl-8"
                    />
                  </div>
                </div>
                <Tabs defaultValue="runs">
                  <TabsList>
                    <TabsTrigger value="runs">Runs</TabsTrigger>
                    <TabsTrigger value="tools">Tools</TabsTrigger>
                  </TabsList>
                  <TabsContent value="runs" className="mt-4 space-y-4">
                    <TraceTable
                      title="Run traces"
                      items={filteredRuns}
                      onDownload={download}
                      onTail={openTail}
                      onEvents={openEvents}
                    />
                  </TabsContent>
                  <TabsContent value="tools" className="mt-4 space-y-4">
                    <TraceTable
                      title="Tool traces"
                      items={filteredTools}
                      onDownload={download}
                      onTail={openTail}
                      onEvents={openEvents}
                    />
                  </TabsContent>
                </Tabs>
              </CardContent>
            </Card>
          </TabsContent>

          <TabsContent value="raw" className="mt-4 space-y-4">
            <Card>
              <CardHeader className="pb-2">
                <CardTitle className="text-sm">Raw JSON</CardTitle>
              </CardHeader>
              <CardContent>
                <ScrollArea className="h-[520px] rounded-md border p-3">
                  <pre className="text-xs">{pretty({ health, ready, status, cron, runs, tools, sessions })}</pre>
                </ScrollArea>
              </CardContent>
            </Card>
          </TabsContent>
        </Tabs>
      </div>

      <Dialog open={viewerOpen} onOpenChange={setViewerOpen}>
        <DialogContent className="max-w-4xl">
          <DialogHeader>
            <DialogTitle className="text-sm">
              {viewerTitle}: <code>{viewerPath}</code>
              {viewerResp?.truncated ? (
                <Badge variant="secondary" className="ml-2">
                  truncated
                </Badge>
              ) : null}
            </DialogTitle>
          </DialogHeader>

          <div className="flex flex-col gap-2 md:flex-row md:items-center md:justify-between">
            <div className="flex items-center gap-2 text-xs text-muted-foreground">
              <span>
                lines: <code>{viewerResp?.lines?.length ?? 0}</code>
              </span>
              <span className="hidden md:inline">·</span>
              <span>
                requested: <code>{viewerResp?.lines_requested ?? "-"}</code>
              </span>
            </div>
            <div className="flex flex-wrap items-center gap-2">
              <div className="relative">
                <Search className="pointer-events-none absolute left-2 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
                <Input
                  value={viewerQuery}
                  onChange={(e) => setViewerQuery(e.target.value)}
                  placeholder="Filter…"
                  className="h-9 w-[240px] pl-8"
                />
              </div>
              <Button
                variant={viewerMode === "structured" ? "default" : "outline"}
                onClick={() => setViewerMode("structured")}
                disabled={!viewerHasStructured}
              >
                Structured
              </Button>
              <Button variant={viewerMode === "raw" ? "default" : "outline"} onClick={() => setViewerMode("raw")}>
                Raw
              </Button>
              <Button variant="outline" onClick={() => safeCopy(viewerPath)} title="Copy path">
                <Copy className="h-4 w-4" />
              </Button>
              <Button variant="outline" onClick={() => download(viewerPath)} disabled={!viewerPath}>
                <Download className="mr-2 h-4 w-4" />
                Download
              </Button>
            </div>
          </div>

          {viewerMode === "structured" && viewerHasStructured ? (
            <ScrollArea className="h-[520px] rounded-md border">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead className="w-[190px]">Time</TableHead>
                    <TableHead className="w-[140px]">Type</TableHead>
                    <TableHead className="w-[90px]">Iter</TableHead>
                    <TableHead>Details</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {viewerFiltered.map((p, idx) => {
                    const o = p.obj;
                    if (!o) return null;
                    const typ = String(o.type || "");
                    const iter = o.iteration != null ? String(o.iteration) : "-";
                    const ms = o.ts_ms != null ? Number(o.ts_ms) : undefined;
                    const ts = ms ? fmtDateTimeMs(ms) : fmtISO(o.ts);
                    const tool = o.tool ? String(o.tool) : "";
                    const dur = o.duration_ms != null ? fmtDurationMs(Number(o.duration_ms)) : "";
                    const isErr = Boolean(o.is_error) || (typeof o.error === "string" && o.error.trim() !== "");

                    const detailParts: string[] = [];
                    if (tool) detailParts.push(`tool=${tool}`);
                    if (dur) detailParts.push(`dur=${dur}`);
                    if (Array.isArray(o.tool_calls) && o.tool_calls.length) detailParts.push(`tool_calls=${o.tool_calls.join(",")}`);
                    if (Array.isArray(o.tool_batch) && o.tool_batch.length) detailParts.push(`tool_batch=${o.tool_batch.length}`);
                    const preview =
                      typeof o.preview === "string"
                        ? o.preview
                        : typeof o.for_llm_preview === "string"
                          ? o.for_llm_preview
                          : typeof o.response_preview === "string"
                            ? o.response_preview
                            : "";

                    return (
                      <TableRow key={`${typ}:${idx}`}>
                        <TableCell className="align-top text-xs">
                          <code>{ts}</code>
                        </TableCell>
                        <TableCell className="align-top text-xs">
                          <div className="flex items-center gap-2">
                            <code>{typ || "-"}</code>
                            {isErr ? <Badge variant="destructive">err</Badge> : null}
                          </div>
                        </TableCell>
                        <TableCell className="align-top text-xs">
                          <code>{iter}</code>
                        </TableCell>
                        <TableCell className="align-top text-xs">
                          {detailParts.length ? (
                            <div className="text-muted-foreground">
                              <code>{detailParts.join(" · ")}</code>
                            </div>
                          ) : null}
                          {preview ? <div className="mt-1 whitespace-pre-wrap">{String(preview).slice(0, 600)}</div> : null}
                          {o.error ? (
                            <div className="mt-1 whitespace-pre-wrap text-destructive">{String(o.error).slice(0, 600)}</div>
                          ) : null}
                        </TableCell>
                      </TableRow>
                    );
                  })}
                  {!viewerFiltered.some((p) => p.obj) ? (
                    <TableRow>
                      <TableCell colSpan={4} className="text-sm text-muted-foreground">
                        No structured events matched.
                      </TableCell>
                    </TableRow>
                  ) : null}
                </TableBody>
              </Table>
            </ScrollArea>
          ) : (
            <ScrollArea className="h-[520px] rounded-md border p-3">
              <pre className="text-xs whitespace-pre-wrap">
                {(viewerFiltered.map((p) => p.raw).join("\n") || "(empty)").slice(0, 40000)}
              </pre>
            </ScrollArea>
          )}
        </DialogContent>
      </Dialog>

      <Dialog open={cronDetailOpen} onOpenChange={setCronDetailOpen}>
        <DialogContent className="max-w-4xl">
          <DialogHeader>
            <DialogTitle className="text-sm">
              Cron job: <code>{cronDetailJob?.name || cronDetailJob?.id || "-"}</code>
            </DialogTitle>
          </DialogHeader>

          <div className="grid gap-3 md:grid-cols-2">
            <div className="rounded-md border p-3">
              <div className="text-xs font-medium text-muted-foreground">Identity</div>
              <div className="mt-2 text-sm">
                <div className="flex items-center justify-between gap-2">
                  <span className="text-muted-foreground">id</span>
                  <code className="break-all">{cronDetailJob?.id || "-"}</code>
                </div>
                <div className="mt-1 flex items-center justify-between gap-2">
                  <span className="text-muted-foreground">enabled</span>
                  <code>{cronDetailJob?.enabled ? "true" : "false"}</code>
                </div>
                <div className="mt-1 flex items-center justify-between gap-2">
                  <span className="text-muted-foreground">deliver</span>
                  <code>{cronDetailJob?.payload?.deliver ? "true" : "false"}</code>
                </div>
              </div>
            </div>

            <div className="rounded-md border p-3">
              <div className="text-xs font-medium text-muted-foreground">Schedule</div>
              <div className="mt-2 text-sm">
                <div className="flex items-center justify-between gap-2">
                  <span className="text-muted-foreground">kind</span>
                  <code>{cronDetailJob?.schedule?.kind || "-"}</code>
                </div>
                {cronDetailJob?.schedule?.expr ? (
                  <div className="mt-1 flex items-center justify-between gap-2">
                    <span className="text-muted-foreground">expr</span>
                    <code className="break-all">{cronDetailJob.schedule.expr}</code>
                  </div>
                ) : null}
                {cronDetailJob?.schedule?.tz ? (
                  <div className="mt-1 flex items-center justify-between gap-2">
                    <span className="text-muted-foreground">tz</span>
                    <code>{cronDetailJob.schedule.tz}</code>
                  </div>
                ) : null}
                {cronDetailJob?.schedule?.atMs ? (
                  <div className="mt-1 flex items-center justify-between gap-2">
                    <span className="text-muted-foreground">at</span>
                    <code>{fmtDateTimeMs(cronDetailJob.schedule.atMs)}</code>
                  </div>
                ) : null}
                <div className="mt-1 flex items-center justify-between gap-2">
                  <span className="text-muted-foreground">next</span>
                  <code>{fmtDateTimeMs(cronDetailJob?.state?.nextRunAtMs)}</code>
                </div>
              </div>
            </div>
          </div>

          <div className="rounded-md border p-3">
            <div className="text-xs font-medium text-muted-foreground">Payload</div>
            <div className="mt-2 grid gap-2 md:grid-cols-2">
              <div className="text-xs text-muted-foreground">
                channel: <code>{cronDetailJob?.payload?.channel || "-"}</code> · to:{" "}
                <code className="break-all">{cronDetailJob?.payload?.to || "-"}</code>
              </div>
              <div className="text-xs text-muted-foreground">
                kind: <code>{cronDetailJob?.payload?.kind || "-"}</code>
              </div>
            </div>
            <Textarea
              readOnly
              className="mt-2 h-[150px] resize-none font-mono text-xs"
              value={String(cronDetailJob?.payload?.message || "")}
            />
          </div>

          <div className="rounded-md border p-3">
            <div className="text-xs font-medium text-muted-foreground">Last run</div>
            <div className="mt-2 grid gap-2 md:grid-cols-4 text-sm">
              <div>
                <div className="text-xs text-muted-foreground">status</div>
                <div className="mt-1">
                  <Badge
                    variant={
                      cronDetailJob?.state?.lastStatus === "ok"
                        ? "default"
                        : cronDetailJob?.state?.lastStatus
                          ? "destructive"
                          : "secondary"
                    }
                  >
                    {cronDetailJob?.state?.lastStatus || "-"}
                  </Badge>
                </div>
              </div>
              <div>
                <div className="text-xs text-muted-foreground">started</div>
                <div className="mt-1">
                  <code>{fmtDateTimeMs(cronDetailJob?.state?.lastRunAtMs)}</code>
                </div>
              </div>
              <div>
                <div className="text-xs text-muted-foreground">duration</div>
                <div className="mt-1">
                  <code>{fmtDurationMs(cronDetailJob?.state?.lastDurationMs)}</code>
                </div>
              </div>
              <div>
                <div className="text-xs text-muted-foreground">history</div>
                <div className="mt-1">
                  <code>{Array.isArray(cronDetailJob?.state?.runHistory) ? cronDetailJob.state.runHistory.length : 0}</code>
                </div>
              </div>
            </div>
            {cronDetailJob?.state?.lastError ? (
              <div className="mt-2 rounded-md border border-destructive/40 bg-destructive/10 p-2 text-xs text-destructive">
                {String(cronDetailJob.state.lastError)}
              </div>
            ) : null}
            {cronDetailJob?.state?.lastOutputPreview ? (
              <ScrollArea className="mt-2 h-[160px] rounded-md border p-2">
                <pre className="text-xs whitespace-pre-wrap">{String(cronDetailJob.state.lastOutputPreview)}</pre>
              </ScrollArea>
            ) : null}
          </div>

          {Array.isArray(cronDetailJob?.state?.runHistory) && cronDetailJob.state.runHistory.length ? (
            <Card>
              <CardHeader className="pb-2">
                <CardTitle className="text-sm">Run history</CardTitle>
              </CardHeader>
              <CardContent>
                <ScrollArea className="h-[220px] rounded-md border">
                  <Table>
                    <TableHeader>
                      <TableRow>
                        <TableHead>Run</TableHead>
                        <TableHead>Started</TableHead>
                        <TableHead>Status</TableHead>
                        <TableHead>Duration</TableHead>
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {cronDetailJob.state.runHistory.map((h: any) => (
                        <TableRow key={String(h.runId || `${h.startedAtMs}`)}>
                          <TableCell className="align-top text-xs">
                            <code>{h.runId || "-"}</code>
                          </TableCell>
                          <TableCell className="align-top text-xs">
                            <code>{fmtDateTimeMs(h.startedAtMs)}</code>
                          </TableCell>
                          <TableCell className="align-top text-xs">
                            <Badge variant={h.status === "ok" ? "default" : "destructive"}>{h.status || "-"}</Badge>
                            {h.error ? (
                              <div className="mt-1 text-destructive" title={String(h.error)}>
                                {String(h.error).slice(0, 120)}
                              </div>
                            ) : null}
                          </TableCell>
                          <TableCell className="align-top text-xs">
                            <code>{fmtDurationMs(h.durationMs)}</code>
                          </TableCell>
                        </TableRow>
                      ))}
                    </TableBody>
                  </Table>
                </ScrollArea>
              </CardContent>
            </Card>
          ) : null}
        </DialogContent>
      </Dialog>

      <Dialog open={sessionDetailOpen} onOpenChange={setSessionDetailOpen}>
        <DialogContent className="max-w-4xl">
          <DialogHeader>
            <DialogTitle className="text-sm">
              Session: <code>{sessionDetail?.key || "-"}</code>
            </DialogTitle>
          </DialogHeader>

          <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
            <span>
              created: <code>{fmtISO(sessionDetail?.created)}</code>
            </span>
            <span>·</span>
            <span>
              updated: <code>{fmtISO(sessionDetail?.updated)}</code>
            </span>
            {sessionDetail?.file ? (
              <>
                <span>·</span>
                <span>
                  file: <code>{sessionDetail.file}</code>
                </span>
              </>
            ) : null}
          </div>

          <ScrollArea className="h-[520px] rounded-md border p-3">
            <pre className="text-xs whitespace-pre-wrap">{sessionDetail?.summary || "(no summary)"}</pre>
          </ScrollArea>

          <div className="flex flex-wrap gap-2">
            <Button variant="outline" onClick={() => safeCopy(sessionDetail?.key || "")}>
              <Copy className="mr-2 h-4 w-4" />
              Copy key
            </Button>
            {sessionDetail?.file ? (
              <Button variant="outline" onClick={() => download(sessionDetail.file!)}>
                <Download className="mr-2 h-4 w-4" />
                Download JSON
              </Button>
            ) : null}
          </div>
        </DialogContent>
      </Dialog>
    </div>
  );
}

function TraceTable({
  title,
  items,
  onDownload,
  onTail,
  onEvents,
}: {
  title: string;
  items: ConsoleTraceItem[];
  onDownload: (path: string) => void;
  onTail: (path: string) => void;
  onEvents: (path: string) => void;
}) {
  return (
    <Card>
      <CardHeader className="pb-2">
        <CardTitle className="text-sm">{title}</CardTitle>
      </CardHeader>
      <CardContent>
        <ScrollArea className="h-[420px] rounded-md border">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Session</TableHead>
                <TableHead>Last event</TableHead>
                <TableHead>File</TableHead>
                <TableHead className="w-[220px]">Actions</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((it) => (
                <TableRow key={it.token}>
                  <TableCell className="align-top">
                    <div className="font-medium">
                      <code>{it.session_key || it.token}</code>
                    </div>
                    <div className="text-xs text-muted-foreground">
                      <code>{it.token}</code>
                    </div>
                  </TableCell>
                  <TableCell className="align-top text-xs">
                    <div>
                      <code>{it.last_event_type || "-"}</code>
                    </div>
                    <div className="text-muted-foreground">
                      <code>{fmtISO(it.last_event_ts || it.mod_time)}</code>
                    </div>
                  </TableCell>
                  <TableCell className="align-top text-xs">
                    <code>{it.events_path}</code>
                    <div className="text-muted-foreground">
                      {it.events_size_bytes ? fmtBytes(it.events_size_bytes) : "-"}
                    </div>
                  </TableCell>
                  <TableCell className="align-top">
                    <div className="flex flex-wrap gap-2">
                      <Button variant="secondary" size="sm" onClick={() => onEvents(it.events_path)}>
                        View
                      </Button>
                      <Button variant="outline" size="sm" onClick={() => onDownload(it.events_path)}>
                        <Download className="mr-2 h-4 w-4" />
                        JSONL
                      </Button>
                      <Button variant="outline" size="sm" onClick={() => onTail(it.events_path)}>
                        Tail
                      </Button>
                    </div>
                  </TableCell>
                </TableRow>
              ))}
              {!items.length ? (
                <TableRow>
                  <TableCell colSpan={4} className="text-sm text-muted-foreground">
                    No items.
                  </TableCell>
                </TableRow>
              ) : null}
            </TableBody>
          </Table>
        </ScrollArea>
      </CardContent>
    </Card>
  );
}
