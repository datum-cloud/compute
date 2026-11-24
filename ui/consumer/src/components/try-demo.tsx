/**
 * The one-click demo deploy offered on the workloads list: the dialog's
 * state hook, the empty-state call-to-action card and the dialog itself.
 */
import { useCreateDemoWorkload, useWorkload } from "../lib/api";
import { Button } from "@datum-cloud/datum-ui/button";
import { Card } from "@datum-cloud/datum-ui/card";
import { Dialog } from "@datum-cloud/datum-ui/dialog";
import { Icon } from "@datum-cloud/datum-ui/icons";
import { toast } from "@datum-cloud/datum-ui/toast";
import { RocketIcon } from "lucide-react";
import { useEffect, useState } from "react";

/** Confirm → create → wait → ready, with the user deciding when to leave at
 * every step. Kept as a dialog (rather than showing progress inline in the
 * grid) because a demo Workload appearing mid-reconciliation among the
 * user's real workloads, then changing shape as it comes up, reads as a
 * glitch. Navigation is always a manual click, never automatic — the dialog
 * polls readiness only to update its own copy/buttons, and just says so
 * once the instance is up rather than whisking the user away on its own. */
type DemoPhase = "confirm" | "creating" | "waiting" | "ready" | "error";

const DEMO_PHASE_COPY: Record<DemoPhase, string> = {
  confirm: "This deploys a running workload into your project — one instance in DFW, usually live within a minute.",
  creating: "Deploying your workload…",
  waiting: "Your workload is coming up in the background. You can wait here or head to its page now — it'll keep coming up either way.",
  ready: "Your workload is live.",
  error: "Something went wrong deploying the workload.",
};

/** Owns the demo dialog's state. Deliberately hoisted out of the CTA card:
 * the workloads grid switches between an "empty" and a "populated" branch
 * as soon as the demo Workload shows up (via the query invalidation on
 * create), and each branch renders its own `<TryDemoWorkloadCard>` element —
 * if the dialog's open/phase state lived inside that card, the branch swap
 * would unmount the open dialog's instance and mount a fresh, closed one,
 * which looks exactly like the dialog auto-closing itself. Called once in
 * `WorkloadList` and threaded into both the card and a single, always-mounted
 * `TryDemoDialog` so the state survives that swap. */
export function useDemoWorkloadDialog(projectId: string | undefined) {
  const [open, setOpen] = useState(false);
  const [phase, setPhase] = useState<DemoPhase>("confirm");
  const [deployedName, setDeployedName] = useState<string | undefined>();
  const { mutate } = useCreateDemoWorkload(projectId);
  // Only polls while actually waiting on readiness — stops once ready, on
  // error, or when the dialog isn't tracking a deploy.
  const { data: workload, error: workloadError } = useWorkload(
    projectId,
    phase === "waiting" ? deployedName : undefined,
  );

  useEffect(() => {
    if (phase !== "waiting") return;
    if (workload?.health === "Available" && workload.readyReplicas > 0) {
      setPhase("ready");
    } else if (workloadError && workloadError.status !== 404) {
      setPhase("error");
    }
  }, [phase, workload, workloadError]);

  // The create call itself (a handful of sequential POSTs) is quick and has
  // no object to show yet, so closing is blocked until it settles. Once
  // we're waiting on readiness, the Workload already exists and keeps
  // reconciling in the background regardless of the dialog — the user can
  // leave any time, either back to the list or straight to the workload.
  const canClose = phase !== "creating";

  const openDialog = () => {
    setPhase("confirm");
    setDeployedName(undefined);
    setOpen(true);
  };

  const close = () => {
    if (!canClose) return;
    setOpen(false);
  };

  const confirm = () => {
    setPhase("creating");
    mutate(undefined, {
      onSuccess: (workloadName) => {
        setDeployedName(workloadName);
        setPhase("waiting");
      },
      onError: () => {
        setPhase("error");
        toast.error("Failed to deploy the workload");
      },
    });
  };

  const goToWorkload = (onView: (workloadName: string) => void) => {
    setOpen(false);
    if (deployedName) onView(deployedName);
  };

  return { open, phase, canClose, openDialog, close, confirm, goToWorkload };
}

/** Static CTA card offering a one-click demo deploy, shown in the empty state
 * of a project with no workloads yet. Once there are workloads, the same
 * action lives in the list toolbar instead, where both views can reach it.
 * Purely presentational: the dialog it opens is owned and rendered by the
 * parent (see `useDemoWorkloadDialog`), since this card gets
 * unmounted/remounted when the page switches branches once the demo
 * appears. */
export function TryDemoWorkloadCard({
  projectId,
  onOpen,
}: {
  projectId?: string;
  onOpen: () => void;
}) {
  return (
    <Card
      size="sm"
      className="flex h-full flex-row flex-wrap items-center justify-between gap-3 px-4 py-3"
      data-testid="compute-plugin-try-demo-card"
    >
      <div className="flex min-w-0 items-center gap-3">
        <Icon icon={RocketIcon} size={16} className="text-muted-foreground shrink-0" aria-hidden />
        <div className="flex min-w-0 flex-col">
          <h3 className="text-sm font-medium">Ready to deploy?</h3>
          <p className="text-muted-foreground text-sm">
            Launch a running workload in one click
          </p>
        </div>
      </div>
      <Button type="primary" theme="solid" size="small" disabled={!projectId} onClick={onOpen}>
        Deploy now
      </Button>
    </Card>
  );
}

/** The demo confirm/progress dialog itself — rendered once, unconditionally,
 * by `WorkloadList` (see `useDemoWorkloadDialog` for why). */
export function TryDemoDialog({
  open,
  phase,
  canClose,
  onClose,
  onConfirm,
  onGoToWorkload,
}: {
  open: boolean;
  phase: DemoPhase;
  canClose: boolean;
  onClose: () => void;
  onConfirm: () => void;
  onGoToWorkload: () => void;
}) {
  return (
    <Dialog open={open} onOpenChange={(next) => !next && onClose()}>
      <Dialog.Content>
        <Dialog.Header
          title="Deploy a workload"
          description={
            // Two lines held for every phase: the copy changes length as the
            // deploy moves on, and a centred dialog would jump with it.
            <span style={{ display: "block", minHeight: "2lh" }}>{DEMO_PHASE_COPY[phase]}</span>
          }
          onClose={canClose ? onClose : undefined}
        />
        <Dialog.Footer>
          {phase === "confirm" && (
            <>
              <Button type="secondary" theme="outline" size="small" onClick={onClose}>
                Cancel
              </Button>
              <Button type="primary" theme="solid" size="small" onClick={onConfirm}>
                Deploy
              </Button>
            </>
          )}
          {phase === "creating" && (
            <Button type="secondary" theme="solid" size="small" loading disabled>
              Creating…
            </Button>
          )}
          {phase === "waiting" && (
            <>
              <Button type="secondary" theme="outline" size="small" onClick={onClose}>
                Close
              </Button>
              <Button type="secondary" theme="solid" size="small" onClick={onGoToWorkload}>
                Go to workload
              </Button>
            </>
          )}
          {phase === "ready" && (
            <>
              <Button type="secondary" theme="outline" size="small" onClick={onClose}>
                Close
              </Button>
              <Button type="secondary" theme="solid" size="small" onClick={onGoToWorkload}>
                View workload
              </Button>
            </>
          )}
          {phase === "error" && (
            <>
              <Button type="secondary" theme="outline" size="small" onClick={onClose}>
                Close
              </Button>
              <Button type="primary" theme="solid" size="small" onClick={onConfirm}>
                Try again
              </Button>
            </>
          )}
        </Dialog.Footer>
      </Dialog.Content>
    </Dialog>
  );
}
