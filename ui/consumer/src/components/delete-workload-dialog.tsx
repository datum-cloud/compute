/**
 * Delete-workload confirmation, modelled on the portal's shared
 * `ConfirmationDialog` (app/components/confirmation-dialog), which lives
 * inside the host and isn't reachable from a plugin. Same pieces: datum-ui
 * Dialog + a type-"DELETE" Input + danger/borderless buttons.
 *
 * On top of that it offers the workload's ALBs and Networks as opt-in extras,
 * since deleting the Workload leaves both behind. Rows the user can't delete
 * (RBAC) or that are shared with another workload are disabled with a tooltip.
 */
import {
  ApiError,
  useDeleteWorkload,
  usePublishedUrl,
  useWorkloadRelatedResources,
  type DeletePermissions,
  type DeleteWorkloadResult,
  type WorkloadRelatedResources,
} from '../lib/api';
import type { Workload } from '../schema';
import { Button } from '@datum-cloud/datum-ui/button';
import { Checkbox } from '@datum-cloud/datum-ui/checkbox';
import { Dialog } from '@datum-cloud/datum-ui/dialog';
import { Icon } from '@datum-cloud/datum-ui/icons';
import { Input } from '@datum-cloud/datum-ui/input';
import { Label } from '@datum-cloud/datum-ui/label';
import { Skeleton } from '@datum-cloud/datum-ui/skeleton';
import { toast } from '@datum-cloud/datum-ui/toast';
import { Tooltip } from '@datum-cloud/datum-ui/tooltip';
import { cn } from '@datum-cloud/datum-ui/utils';
import { GlobeIcon, InfoIcon, NetworkIcon, type LucideIcon } from 'lucide-react';
import { useEffect, useState } from 'react';

const CONFIRM_VALUE = 'DELETE';

const NO_RELATED: WorkloadRelatedResources = { albs: [], networks: [], serviceProxies: {} };

const ALB_DENIED = "You don't have permission to delete this Application Load Balancer";
const NETWORK_DENIED = "You don't have permission to delete this Network";

/** Open/close state for the dialog. The workload is kept after closing so the
 * content doesn't blank out during the close animation. */
export function useDeleteWorkloadDialog() {
  const [workload, setWorkload] = useState<Workload | undefined>();
  const [open, setOpen] = useState(false);
  return {
    workload,
    open,
    show: (target: Workload) => {
      setWorkload(target);
      setOpen(true);
    },
    close: () => setOpen(false),
  };
}

function toggle(set: Set<string>, name: string, on: boolean): Set<string> {
  const next = new Set(set);
  if (on) next.add(name);
  else next.delete(name);
  return next;
}

function plural(count: number, one: string, many: string): string {
  return `${count} ${count === 1 ? one : many}`;
}

/** One tickable resource. Rows the user can't tick say why inline (and in a
 * tooltip), rather than leaving a greyed-out checkbox to be guessed at. */
function RelatedRow({
  id,
  icon,
  kind,
  label,
  detail,
  checked,
  disabledReason,
  first,
  onCheckedChange,
}: {
  id: string;
  icon: LucideIcon;
  kind: string;
  label: string;
  detail?: string;
  checked: boolean;
  disabledReason?: string;
  first: boolean;
  onCheckedChange: (checked: boolean) => void;
}) {
  const disabled = !!disabledReason;
  const row = (
    <label
      htmlFor={id}
      data-e2e={id}
      className={cn(
        'flex items-center gap-3 px-3 py-2.5',
        !first && 'border-t',
        disabled ? 'cursor-not-allowed' : 'hover:bg-muted/50 cursor-pointer'
      )}>
      <Checkbox
        id={id}
        checked={checked && !disabled}
        disabled={disabled}
        onCheckedChange={(value) => onCheckedChange(value === true)}
      />
      <span
        className={cn(
          'bg-muted text-muted-foreground flex size-8 shrink-0 items-center justify-center rounded-md',
          disabled && 'opacity-50'
        )}>
        <Icon icon={icon} size={16} />
      </span>
      <span className={cn('flex min-w-0 flex-1 flex-col', disabled && 'opacity-60')}>
        <span className="truncate text-sm font-medium" title={label}>
          {label}
        </span>
        {(disabledReason ?? detail) ? (
          <span className="text-muted-foreground truncate text-xs" title={disabledReason ?? detail}>
            {disabledReason ?? detail}
          </span>
        ) : null}
      </span>
      <span className="text-muted-foreground shrink-0 text-xs">{kind}</span>
    </label>
  );
  if (!disabled) return row;
  return (
    <Tooltip message={disabledReason} side="top">
      <div>{row}</div>
    </Tooltip>
  );
}

/** A skeleton bar in a box exactly one line of `textClass` tall. The hidden
 * nbsp gives the box the host's real line height for that text size, so the
 * bar never assumes Tailwind's defaults. */
function SkeletonLine({ textClass, className }: { textClass: string; className: string }) {
  return (
    <span className={cn('flex items-center', textClass)}>
      <span className="invisible w-0">&nbsp;</span>
      <Skeleton className={cn('h-3', className)} />
    </span>
  );
}

/** Heading + hint above the list. Static copy, so the loading state renders
 * the real thing rather than guessing how it wraps. */
function RelatedHeader() {
  return (
    <div className="flex flex-col gap-0.5">
      <p className="text-sm font-medium">Also delete related resources</p>
      <p className="text-muted-foreground text-xs">
        These aren't removed with the workload. Anything left unchecked stays in your project and
        may keep serving traffic or incurring cost.
      </p>
    </div>
  );
}

function KeptAlbHint({ workloadName }: { workloadName?: string }) {
  return (
    <p className="text-muted-foreground flex items-start gap-1.5 text-xs">
      <Icon icon={InfoIcon} size={12} className="mt-0.5 shrink-0" />
      <span>
        Kept load balancers reconnect if you redeploy a workload named{' '}
        <span className="text-foreground font-medium break-all">{workloadName}</span>.
      </span>
    </p>
  );
}

/** Same element tree as `RelatedRow`, with bars in place of text. */
function RelatedRowSkeleton({ detail, first }: { detail: boolean; first: boolean }) {
  return (
    <div className={cn('flex items-center gap-3 px-3 py-2.5', !first && 'border-t')}>
      <Skeleton className="size-4 shrink-0 rounded-sm" />
      <Skeleton className="size-8 shrink-0 rounded-md" />
      <span className="flex min-w-0 flex-1 flex-col">
        <SkeletonLine textClass="text-sm font-medium" className="w-40" />
        {detail && <SkeletonLine textClass="text-xs" className="w-56" />}
      </span>
      <SkeletonLine textClass="text-xs" className="w-16 shrink-0" />
    </div>
  );
}

/**
 * Loading state for the "Also delete" section, shaped from what's already
 * known so it resolves in place: the real header and hint, one row per ALB in
 * the published-URL cache (same matcher as the lookup, with or without a
 * hostname line) and one per network on the workload's template.
 */
function RelatedSkeleton({
  albs,
  networkCount,
  permissions,
  workloadName,
}: {
  albs: { hasHostname: boolean }[];
  networkCount: number;
  permissions: DeletePermissions;
  workloadName?: string;
}) {
  // A row that can't be ticked shows its reason on the second line, so a
  // permission-denied row is two lines even without a hostname. (Rows disabled
  // for being shared can't be predicted before the lookup; those still grow.)
  const rows = [
    ...albs.map((alb) => ({ detail: alb.hasHostname || !permissions.canDeleteAlb })),
    ...Array.from({ length: networkCount }, () => ({ detail: !permissions.canDeleteNetwork })),
  ];
  if (rows.length === 0) return null;
  return (
    <div className="flex flex-col gap-2" aria-hidden>
      <RelatedHeader />
      <div className="overflow-hidden rounded-lg border">
        {rows.map((row, index) => (
          <RelatedRowSkeleton key={index} detail={row.detail} first={index === 0} />
        ))}
      </div>
      {/* ALBs start unticked, so the reconnect hint shows whenever there is one. */}
      {albs.length > 0 && <KeptAlbHint workloadName={workloadName} />}
    </div>
  );
}

function failureSummary(result: DeleteWorkloadResult): string {
  const albs = result.failed.filter((item) => item.kind === 'alb').map((item) => item.name);
  const networks = result.failed.filter((item) => item.kind === 'network').map((item) => item.name);
  const parts: string[] = [];
  if (albs.length > 0) parts.push(`Application Load Balancer ${albs.join(', ')}`);
  if (networks.length > 0) parts.push(`Network ${networks.join(', ')}`);
  return `Couldn't delete ${parts.join(' and ')}. You can remove ${
    result.failed.length === 1 ? 'it' : 'them'
  } from the project later.`;
}

export function DeleteWorkloadDialog({
  projectId,
  workload,
  open,
  permissions,
  onClose,
  onDeleted,
}: {
  projectId?: string;
  workload?: Workload;
  open: boolean;
  permissions: DeletePermissions;
  onClose: () => void;
  /** Called as soon as the user confirms, e.g. to leave the workload's detail page. */
  onDeleted?: (workloadName: string) => void;
}) {
  const related = useWorkloadRelatedResources(projectId, open ? workload : undefined);
  // Already cached by the page behind the dialog; only used to size the skeleton.
  const published = usePublishedUrl(projectId, open ? workload?.name : undefined);
  const skeletonAlbs = published.data
    ? published.data.proxies.map((alb) => ({ hasHostname: !!alb.hostname }))
    : published.data === null
      ? []
      : [{ hasHostname: true }];
  const { mutateAsync, isPending } = useDeleteWorkload(projectId);
  const [albs, setAlbs] = useState<Set<string>>(new Set());
  const [networks, setNetworks] = useState<Set<string>>(new Set());
  const [confirmText, setConfirmText] = useState('');

  useEffect(() => {
    if (!open) return;
    setAlbs(new Set());
    setNetworks(new Set());
    setConfirmText('');
  }, [open, workload?.name]);

  // A failed lookup still lets the user delete the workload on its own.
  const relatedData = related.data ?? (related.error ? NO_RELATED : undefined);
  const relatedAlbs = relatedData?.albs ?? [];
  const relatedNetworks = relatedData?.networks ?? [];
  const hasRelated = relatedAlbs.length > 0 || relatedNetworks.length > 0;
  // An ALB left in place still selects instances by workload name, so it
  // picks a same-named workload back up — worth saying while one is unticked.
  const keptAlb = relatedAlbs.some((alb) => !albs.has(alb.proxyName));

  // Related resources must have loaded (or failed) before submitting, so the
  // user never deletes with a checklist they haven't seen.
  const canSubmit = confirmText === CONFIRM_VALUE && !!relatedData && !isPending;

  const close = () => {
    if (!isPending) onClose();
  };

  const submit = () => {
    if (!workload || !canSubmit || !relatedData) return;
    const name = workload.name;
    // Leave straight away and let the deletes finish in the background.
    // mutateAsync's promise still settles after this dialog (and the detail
    // page) unmount, whereas mutate()'s per-call callbacks would be dropped.
    mutateAsync({
      workloadName: name,
      albs: [...albs],
      networks: [...networks],
      related: relatedData,
      canDeleteNetworkService: permissions.canDeleteNetworkService,
    }).then(
      (result) => {
        if (result.failed.length > 0) {
          toast.warning(`Workload ${name} is being deleted`, { description: failureSummary(result) });
        } else {
          toast.success(`Workload ${name} is being deleted`);
        }
      },
      (error: unknown) => {
        toast.error(
          error instanceof ApiError && error.status === 403
            ? "You don't have permission to delete this workload"
            : `Failed to delete workload ${name}`
        );
      }
    );
    onClose();
    onDeleted?.(name);
  };

  return (
    <Dialog open={open} onOpenChange={(next) => !next && close()}>
      <Dialog.Content>
        <Dialog.Header
          title={`Delete workload ${workload?.name ?? ''}?`}
          description="Its instances will be stopped and removed from every location. This can't be undone."
          descriptionClassName="break-all"
          onClose={isPending ? undefined : close}
        />
        <Dialog.Body className="flex flex-col gap-5 px-5 py-0">
          {related.isLoading && (
            <RelatedSkeleton
              albs={skeletonAlbs}
              networkCount={workload?.networks.length ?? 0}
              permissions={permissions}
              workloadName={workload?.name}
            />
          )}

          {related.error && (
            <p className="text-muted-foreground text-sm">
              Couldn't look up this workload's load balancers and networks. Only the workload will be deleted.
            </p>
          )}

          {hasRelated && (
            <div className="flex flex-col gap-2">
              <RelatedHeader />
              <div className="overflow-hidden rounded-lg border">
                {relatedAlbs.map((alb, index) => (
                  <RelatedRow
                    key={alb.proxyName}
                    id={`delete-workload-dialog-alb-${alb.proxyName}`}
                    icon={GlobeIcon}
                    kind="Load balancer"
                    label={alb.displayName || alb.proxyName}
                    detail={alb.hostname}
                    first={index === 0}
                    checked={albs.has(alb.proxyName)}
                    disabledReason={
                      !permissions.canDeleteAlb
                        ? ALB_DENIED
                        : alb.sharedWithOtherWorkloads
                          ? 'Also routes to other workloads'
                          : undefined
                    }
                    onCheckedChange={(on) => setAlbs((prev) => toggle(prev, alb.proxyName, on))}
                  />
                ))}
                {relatedNetworks.map((network, index) => (
                  <RelatedRow
                    key={network.name}
                    id={`delete-workload-dialog-network-${network.name}`}
                    icon={NetworkIcon}
                    kind="Network"
                    label={network.name}
                    first={relatedAlbs.length === 0 && index === 0}
                    checked={networks.has(network.name)}
                    disabledReason={
                      !permissions.canDeleteNetwork
                        ? NETWORK_DENIED
                        : network.sharedWith.length > 0
                          ? `Used by ${plural(network.sharedWith.length, 'other workload', 'other workloads')}`
                          : undefined
                    }
                    onCheckedChange={(on) =>
                      setNetworks((prev) => toggle(prev, network.name, on))
                    }
                  />
                ))}
              </div>
              {keptAlb && <KeptAlbHint workloadName={workload?.name} />}
            </div>
          )}

          <div className="mb-1 flex flex-col gap-3">
            <Label htmlFor="delete-workload-dialog-input" className="cursor-text select-text">
              Type "{CONFIRM_VALUE}" to confirm.
            </Label>
            <Input
              id="delete-workload-dialog-input"
              type="text"
              autoComplete="off"
              data-e2e="delete-workload-dialog-input"
              placeholder="Type in here..."
              value={confirmText}
              onChange={(event) => setConfirmText(event.target.value)}
              onKeyDown={(event) => {
                if (event.key === 'Enter') submit();
              }}
            />
          </div>
        </Dialog.Body>
        <Dialog.Footer>
          <Button
            type="quaternary"
            theme="borderless"
            data-e2e="delete-workload-dialog-cancel"
            onClick={close}
            disabled={isPending}>
            Cancel
          </Button>
          <Button
            type="danger"
            theme="solid"
            data-e2e="delete-workload-dialog-submit"
            onClick={submit}
            disabled={!canSubmit}
            loading={isPending}>
            Delete
          </Button>
        </Dialog.Footer>
      </Dialog.Content>
    </Dialog>
  );
}
