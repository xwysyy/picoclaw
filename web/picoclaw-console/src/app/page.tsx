"use client";

import { useEffect, useMemo, useState } from "react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { ScrollArea } from "@/components/ui/scroll-area";
import { Separator } from "@/components/ui/separator";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";

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

export default function Home() {
  const [apiKey, setApiKey] = useState<string>("");
  const [savedKey, setSavedKey] = useState<string>("");

  const [status, setStatus] = useState<any>(null);
  const [cron, setCron] = useState<any>(null);
  const [runs, setRuns] = useState<{ items?: ConsoleTraceItem[] } | null>(null);
  const [tools, setTools] = useState<{ items?: ConsoleTraceItem[] } | null>(null);
  const [sessions, setSessions] = useState<{ items?: ConsoleSessionsItem[] } | null>(null);

  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string>("");

  const [tailOpen, setTailOpen] = useState(false);
  const [tailPath, setTailPath] = useState<string>("");
  const [tailLines, setTailLines] = useState<string[]>([]);

  useEffect(() => {
    try {
      const key = localStorage.getItem(LS_KEY) || "";
      setApiKey(key);
      setSavedKey(key);
    } catch {
      // ignore
    }
  }, []);

  const authBadge = useMemo(() => {
    const hasKey = (apiKey || "").trim().length > 0;
    return hasKey ? <Badge>auth: bearer</Badge> : <Badge variant="secondary">auth: loopback</Badge>;
  }, [apiKey]);

  async function refreshAll() {
    setErr("");
    setBusy(true);
    try {
      const [st, cr, ru, to, se] = await Promise.all([
        apiJSON<any>("/api/console/status", apiKey),
        apiJSON<any>("/api/console/cron", apiKey),
        apiJSON<any>("/api/console/runs", apiKey),
        apiJSON<any>("/api/console/tools", apiKey),
        apiJSON<any>("/api/console/sessions", apiKey),
      ]);
      setStatus(st);
      setCron(cr);
      setRuns(ru);
      setTools(to);
      setSessions(se);
    } catch (e: any) {
      setErr(e?.message ? String(e.message) : String(e));
    } finally {
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
    setTailPath(path);
    setTailLines([]);
    setTailOpen(true);
    try {
      const resp = await apiJSON<any>(`/api/console/tail?path=${encodeURIComponent(path)}&lines=200`, apiKey);
      setTailLines(Array.isArray(resp?.lines) ? resp.lines.map((s: any) => String(s)) : []);
    } catch (e: any) {
      setErr(e?.message ? String(e.message) : String(e));
    }
  }

  const headerInfo = status?.info || {};
  const lastActive = status?.last_active || {};

  return (
    <div className="min-h-screen bg-background text-foreground">
      <div className="mx-auto max-w-6xl px-4 py-6">
        <div className="flex flex-col gap-2 md:flex-row md:items-center md:justify-between">
          <div>
            <div className="text-xl font-semibold tracking-tight">PicoClaw Console</div>
            <div className="text-sm text-muted-foreground">
              Read-only UI (Next.js + shadcn). Data via <code>/api/console/*</code>.
            </div>
          </div>
          <div className="flex items-center gap-2">
            {authBadge}
            <Button variant="secondary" onClick={refreshAll} disabled={busy}>
              {busy ? "Refreshing..." : "Refresh"}
            </Button>
          </div>
        </div>

        <Separator className="my-4" />

        <Card>
          <CardHeader className="pb-2">
            <CardTitle className="text-base">Auth</CardTitle>
          </CardHeader>
          <CardContent className="flex flex-col gap-3 md:flex-row md:items-center">
            <div className="flex-1">
              <Input
                type="password"
                placeholder="gateway.api_key (optional on loopback)"
                value={apiKey}
                onChange={(e) => setApiKey(e.target.value)}
              />
              <div className="mt-1 text-xs text-muted-foreground">
                Saved: {savedKey ? "yes" : "no"} · Requests use <code>Authorization: Bearer</code>.
              </div>
            </div>
            <div className="flex gap-2">
              <Button onClick={saveKey} variant="default">
                Save
              </Button>
              <Button onClick={clearKey} variant="outline">
                Clear
              </Button>
            </div>
          </CardContent>
        </Card>

        {err ? (
          <div className="mt-4 rounded-md border border-destructive/40 bg-destructive/10 p-3 text-sm text-destructive">
            <div className="font-medium">Error</div>
            <pre className="mt-1 whitespace-pre-wrap text-xs">{err}</pre>
          </div>
        ) : null}

        <Tabs defaultValue="status" className="mt-6">
          <TabsList>
            <TabsTrigger value="status">Status</TabsTrigger>
            <TabsTrigger value="cron">Cron</TabsTrigger>
            <TabsTrigger value="sessions">Sessions</TabsTrigger>
            <TabsTrigger value="runs">Run traces</TabsTrigger>
            <TabsTrigger value="tools">Tool traces</TabsTrigger>
          </TabsList>

          <TabsContent value="status" className="mt-4 space-y-4">
            <div className="grid gap-4 md:grid-cols-3">
              <Card>
                <CardHeader className="pb-2">
                  <CardTitle className="text-sm">Model</CardTitle>
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
                  <CardTitle className="text-sm">Workspace</CardTitle>
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
                  <CardTitle className="text-sm">Last active</CardTitle>
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
            </div>

            <Card>
              <CardHeader className="pb-2">
                <CardTitle className="text-sm">Raw</CardTitle>
              </CardHeader>
              <CardContent>
                <ScrollArea className="h-[360px] rounded-md border p-3">
                  <pre className="text-xs">{pretty(status)}</pre>
                </ScrollArea>
              </CardContent>
            </Card>
          </TabsContent>

          <TabsContent value="cron" className="mt-4 space-y-4">
            <Card>
              <CardHeader className="pb-2">
                <CardTitle className="text-sm">Jobs</CardTitle>
              </CardHeader>
              <CardContent>
                <div className="mb-2 text-xs text-muted-foreground">
                  store: <code>{cron?.path || "cron/jobs.json"}</code>
                </div>
                <ScrollArea className="h-[420px] rounded-md border">
                  <Table>
                    <TableHeader>
                      <TableRow>
                        <TableHead>Name</TableHead>
                        <TableHead>Schedule</TableHead>
                        <TableHead>Deliver</TableHead>
                        <TableHead>Last</TableHead>
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {(cron?.jobs || []).map((j: any) => (
                        <TableRow key={j.id}>
                          <TableCell className="align-top">
                            <div className="font-medium">{j.name || j.id}</div>
                            <div className="text-xs text-muted-foreground">{j.enabled ? "enabled" : "disabled"}</div>
                          </TableCell>
                          <TableCell className="align-top text-xs">
                            <div>
                              <code>{j.schedule?.kind || "-"}</code>
                            </div>
                            {j.schedule?.expr ? (
                              <div className="text-muted-foreground">
                                <code>{j.schedule.expr}</code>
                              </div>
                            ) : null}
                          </TableCell>
                          <TableCell className="align-top text-xs">
                            <code>{j.payload?.deliver ? "true" : "false"}</code>
                          </TableCell>
                          <TableCell className="align-top text-xs">
                            <div>
                              <code>{j.state?.lastStatus || "-"}</code>
                            </div>
                            {j.state?.lastError ? <div className="text-destructive">{j.state.lastError}</div> : null}
                          </TableCell>
                        </TableRow>
                      ))}
                      {!cron?.jobs?.length ? (
                        <TableRow>
                          <TableCell colSpan={4} className="text-sm text-muted-foreground">
                            No jobs.
                          </TableCell>
                        </TableRow>
                      ) : null}
                    </TableBody>
                  </Table>
                </ScrollArea>

                <div className="mt-3 flex gap-2">
                  <Button variant="outline" onClick={() => download("cron/jobs.json")}>
                    Download jobs.json
                  </Button>
                </div>
              </CardContent>
            </Card>
          </TabsContent>

          <TabsContent value="sessions" className="mt-4 space-y-4">
            <Card>
              <CardHeader className="pb-2">
                <CardTitle className="text-sm">Sessions</CardTitle>
              </CardHeader>
              <CardContent>
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
                      {(sessions?.items || []).map((s) => (
                        <TableRow key={s.key}>
                          <TableCell className="align-top">
                            <code>{s.key}</code>
                          </TableCell>
                          <TableCell className="align-top text-xs">
                            <code>{s.updated || "-"}</code>
                          </TableCell>
                          <TableCell className="align-top text-xs text-muted-foreground">
                            {s.summary ? s.summary.slice(0, 160) : "-"}
                          </TableCell>
                          <TableCell className="align-top">
                            <div className="flex gap-2">
                              {s.file ? (
                                <Button variant="outline" size="sm" onClick={() => download(s.file!)}>
                                  Download
                                </Button>
                              ) : null}
                            </div>
                          </TableCell>
                        </TableRow>
                      ))}
                      {!sessions?.items?.length ? (
                        <TableRow>
                          <TableCell colSpan={4} className="text-sm text-muted-foreground">
                            No sessions.
                          </TableCell>
                        </TableRow>
                      ) : null}
                    </TableBody>
                  </Table>
                </ScrollArea>
              </CardContent>
            </Card>
          </TabsContent>

          <TabsContent value="runs" className="mt-4 space-y-4">
            <TraceTable title="Run traces" items={runs?.items || []} onDownload={download} onTail={openTail} />
          </TabsContent>

          <TabsContent value="tools" className="mt-4 space-y-4">
            <TraceTable title="Tool traces" items={tools?.items || []} onDownload={download} onTail={openTail} />
          </TabsContent>
        </Tabs>
      </div>

      <Dialog open={tailOpen} onOpenChange={setTailOpen}>
        <DialogContent className="max-w-3xl">
          <DialogHeader>
            <DialogTitle className="text-sm">
              Tail: <code>{tailPath}</code>
            </DialogTitle>
          </DialogHeader>
          <ScrollArea className="h-[420px] rounded-md border p-3">
            <pre className="text-xs">{tailLines.join("\n") || "(empty)"}</pre>
          </ScrollArea>
          <div className="flex gap-2">
            <Button variant="outline" onClick={() => download(tailPath)} disabled={!tailPath}>
              Download
            </Button>
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
}: {
  title: string;
  items: ConsoleTraceItem[];
  onDownload: (path: string) => void;
  onTail: (path: string) => void;
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
                      <code>{it.last_event_ts || it.mod_time || "-"}</code>
                    </div>
                  </TableCell>
                  <TableCell className="align-top text-xs">
                    <code>{it.events_path}</code>
                    <div className="text-muted-foreground">
                      {it.events_size_bytes ? `${it.events_size_bytes} bytes` : "-"}
                    </div>
                  </TableCell>
                  <TableCell className="align-top">
                    <div className="flex gap-2">
                      <Button variant="outline" size="sm" onClick={() => onDownload(it.events_path)}>
                        Download
                      </Button>
                      <Button variant="secondary" size="sm" onClick={() => onTail(it.events_path)}>
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
