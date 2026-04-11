"use client";

import { useState } from "react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { ShieldCheck, X, Check, Ban } from "lucide-react";
import { relativeTime } from "@/lib/format";
import type { DnaApprovalWithVersion } from "@/lib/types";

import { useDnaApprovals, useDnaApprovalDiff, useApproveDna, useRejectDna } from "@/hooks/use-dna-approvals";
import { cn } from "@/lib/utils";

// --- Components ---

function StatusBadge({ status }: { status: "pending" | "approved" | "rejected" }) {
  if (status === "approved") {
    return <Badge className="bg-emerald-500/20 text-emerald-400 hover:bg-emerald-500/30">Approved</Badge>;
  }
  if (status === "rejected") {
    return <Badge className="bg-red-500/20 text-red-400 hover:bg-red-500/30">Rejected</Badge>;
  }
  return <Badge className="bg-amber-500/20 text-amber-400 hover:bg-amber-500/30">Pending</Badge>;
}

function TomlDiffViewer({ baseToml, newToml }: { baseToml: string; newToml: string }) {
  const baseLines = baseToml.split("\n");
  const newLines = newToml.split("\n");

  // Simple line-presence diff: lines only in base = removed, only in new = added, both = context
  return (
    <pre className="bg-muted/30 rounded-lg p-4 font-mono text-xs overflow-x-auto">
      {baseLines.map((line, i) => {
        if (!newLines.includes(line)) {
          return (
            <div key={`rem-${i}`} className="text-red-400 bg-red-500/10 px-1">
              - {line}
            </div>
          );
        }
        return null;
      })}
      {newLines.map((line, i) => {
        if (!baseLines.includes(line)) {
          return (
            <div key={`add-${i}`} className="text-emerald-400 bg-emerald-500/10 px-1">
              + {line}
            </div>
          );
        } else {
          return (
            <div key={`ctx-${i}`} className="text-muted-foreground px-1 opacity-60">
              &nbsp;&nbsp;{line}
            </div>
          );
        }
      })}
    </pre>
  );
}

function DiffPanel({ 
  id, 
  onClose,
  approval, 
  onApprove, 
  onReject,
  isPendingAction
}: { 
  id: string; 
  onClose: () => void;
  approval: DnaApprovalWithVersion;
  onApprove: () => void;
  onReject: () => void;
  isPendingAction: boolean;
}) {
  const { data: diff, isLoading } = useDnaApprovalDiff(id);

  return (
    <div className="fixed inset-y-0 right-0 w-[500px] border-l border-border bg-card shadow-xl p-6 flex flex-col gap-6 overflow-y-auto z-50 animate-in slide-in-from-right duration-200">
      <div className="flex items-center justify-between">
        <div className="space-y-1">
          <h2 className="text-lg font-bold">{approval.version.strategyKey}</h2>
          <div className="flex items-center gap-2">
            <span className="font-mono text-xs text-muted-foreground">
              {approval.version.contentHash.substring(0, 8)}
            </span>
            <StatusBadge status={approval.approval.status} />
          </div>
        </div>
        <Button variant="ghost" size="icon" onClick={onClose}>
          <X className="h-4 w-4" />
        </Button>
      </div>

      <div className="space-y-4 text-sm">
        <div className="grid grid-cols-2 gap-4">
          <div>
            <span className="text-muted-foreground block text-xs uppercase tracking-wider">Detected</span>
            <span>{relativeTime(approval.version.detectedAt)}</span>
          </div>
          {approval.approval.decidedAt && (
             <div>
              <span className="text-muted-foreground block text-xs uppercase tracking-wider">Decided</span>
              <span>{relativeTime(approval.approval.decidedAt)}</span>
            </div>
          )}
        </div>
      </div>

      <div className="flex-1 min-h-[200px]">
        <h3 className="text-sm font-medium mb-2">Configuration Changes</h3>
        {isLoading ? (
          <div className="flex items-center justify-center h-32 text-muted-foreground text-sm">Loading diff...</div>
        ) : diff ? (
          <TomlDiffViewer baseToml={diff.baseToml} newToml={diff.newToml} />
        ) : (
          <div className="text-red-400 text-sm">Failed to load diff</div>
        )}
      </div>

      {approval.approval.status === "pending" && (
        <div className="grid grid-cols-2 gap-3 mt-auto pt-4 border-t border-border">
          <Button 
            variant="destructive" 
            onClick={onReject}
            disabled={isPendingAction}
          >
            {isPendingAction ? "Processing..." : (
              <>
                <Ban className="mr-2 h-4 w-4" /> Reject
              </>
            )}
          </Button>
          <Button 
            className="bg-emerald-600 hover:bg-emerald-700 text-white"
            onClick={onApprove}
            disabled={isPendingAction}
          >
            {isPendingAction ? "Processing..." : (
              <>
                <Check className="mr-2 h-4 w-4" /> Approve
              </>
            )}
          </Button>
        </div>
      )}
    </div>
  );
}

// --- Main Page ---

export default function ApprovalsPage() {
  const { data: approvals, isLoading } = useDnaApprovals();
  const [selectedId, setSelectedId] = useState<string | null>(null);
  
  const approveMutation = useApproveDna();
  const rejectMutation = useRejectDna();

  const handleApprove = () => {
    if (selectedId) {
      approveMutation.mutate(selectedId, {
        onSuccess: () => setSelectedId(null)
      });
    }
  };

  const handleReject = () => {
    if (selectedId) {
      rejectMutation.mutate(selectedId, {
         onSuccess: () => setSelectedId(null)
      });
    }
  };

  const isPendingAction = approveMutation.isPending || rejectMutation.isPending;

  // Stats
  const total = approvals?.length || 0;
  const pending = approvals?.filter(a => a.approval.status === "pending").length || 0;
  const approved = approvals?.filter(a => a.approval.status === "approved").length || 0;

  // Selected Item
  const selectedApproval = approvals?.find(a => a.approval.id === selectedId);

  return (
    <div className="flex flex-col gap-6 p-6 h-full">
      {/* Header */}
      <div className="flex items-center justify-between">
        <div>
          <h1 className="flex items-center gap-2 text-2xl font-bold text-foreground">
            <ShieldCheck className="h-6 w-6" />
            DNA Approvals
          </h1>
          <p className="text-sm text-muted-foreground">
            Review and approve strategy configuration changes
          </p>
        </div>
      </div>

      {/* Stats */}
      <div className="grid gap-4 md:grid-cols-3">
        <Card>
          <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
            <CardTitle className="text-sm font-medium">Total Versions</CardTitle>
            <ShieldCheck className="h-4 w-4 text-muted-foreground" />
          </CardHeader>
          <CardContent>
            <div className="text-2xl font-bold">{total}</div>
          </CardContent>
        </Card>
        <Card>
          <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
            <CardTitle className="text-sm font-medium">Pending Review</CardTitle>
            <div className="h-2 w-2 rounded-full bg-amber-500 animate-pulse" />
          </CardHeader>
          <CardContent>
            <div className="text-2xl font-bold text-amber-500">{pending}</div>
          </CardContent>
        </Card>
        <Card>
          <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
            <CardTitle className="text-sm font-medium">Approved</CardTitle>
            <Check className="h-4 w-4 text-emerald-500" />
          </CardHeader>
          <CardContent>
            <div className="text-2xl font-bold text-emerald-500">{approved}</div>
          </CardContent>
        </Card>
      </div>

      {/* Main Content */}
      <div className="flex-1 flex gap-6 min-h-0 relative">
        <div className="flex-1 min-w-0">
          <div className="rounded-md border border-border bg-card">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Strategy</TableHead>
                  <TableHead>Version</TableHead>
                  <TableHead>Status</TableHead>
                  <TableHead>Detected</TableHead>
                  <TableHead>Decided</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {isLoading ? (
                  <TableRow>
                    <TableCell colSpan={5} className="h-24 text-center text-muted-foreground">
                      Loading approvals...
                    </TableCell>
                  </TableRow>
                ) : approvals?.length === 0 ? (
                   <TableRow>
                    <TableCell colSpan={5} className="h-24 text-center text-muted-foreground">
                      No pending approvals — all strategies are running on approved DNA
                    </TableCell>
                  </TableRow>
                ) : (
                  approvals?.map((item) => (
                    <TableRow 
                      key={item.approval.id}
                      className={cn(
                        "cursor-pointer hover:bg-muted/50 transition-colors",
                        selectedId === item.approval.id && "bg-muted"
                      )}
                      onClick={() => setSelectedId(item.approval.id)}
                      data-state={selectedId === item.approval.id ? "selected" : undefined}
                    >
                      <TableCell className="font-mono font-medium">
                        {item.version.strategyKey}
                      </TableCell>
                      <TableCell className="font-mono text-xs text-muted-foreground">
                        {item.version.contentHash.substring(0, 8)}
                      </TableCell>
                      <TableCell>
                        <StatusBadge status={item.approval.status} />
                      </TableCell>
                      <TableCell className="text-muted-foreground">
                        {relativeTime(item.version.detectedAt)}
                      </TableCell>
                      <TableCell className="text-muted-foreground">
                        {item.approval.decidedAt ? relativeTime(item.approval.decidedAt) : "—"}
                      </TableCell>
                    </TableRow>
                  ))
                )}
              </TableBody>
            </Table>
          </div>
        </div>

        {/* Detail Panel */}
        {selectedId && selectedApproval && (
          <DiffPanel 
            id={selectedId}
            onClose={() => setSelectedId(null)}
            approval={selectedApproval}
            onApprove={handleApprove}
            onReject={handleReject}
            isPendingAction={isPendingAction}
          />
        )}
      </div>
    </div>
  );
}
