// Compute overview dashboard
//
// For operating and scaling compute across both runtime classes:
// general-purpose (Kata) and unikernel (Unikraft). Top to bottom it answers:
// is the platform healthy and where not, can a customer deploy and use a
// workload and how long does it take, is there room on the
// compute hosts, and where is demand going. Instance-level detail, runtime
// component health and controller internals sit in collapsed rows below.
//
// Stats and tables cover the dashboard time range; time series show rates
// over it.
//
// Usage:
//   jsonnet -J vendor config.jsonnet > dashboard.json
// Or:
//   task dashboards:build

local g = import 'github.com/grafana/grafonnet/gen/grafonnet-v11.4.0/main.libsonnet';

local prometheus    = g.query.prometheus;
local stat          = g.panel.stat;
local table         = g.panel.table;
local timeSeries    = g.panel.timeSeries;
local stateTimeline = g.panel.stateTimeline;
local text          = g.panel.text;
local row           = g.panel.row;
local var           = g.dashboard.variable;

local ds = '${datasource}';

// Size of the default instance type (datumcloud-d1-standard-2), used to
// express headroom as a number of instances.
local slotCpu = 1;
local slotMemoryBytes = 2 * 1024 * 1024 * 1024;

// How long a host that stopped reporting keeps its place in the capacity and
// node panels, so a dead host shows as not Ready instead of disappearing.
local hostLookback = '1d';

// ---------------------------------------------------------------------------
// Control-plane selectors
// ---------------------------------------------------------------------------

// Every query labels the runtime class as `runtime`. Instances created before
// runtime classes existed have no class and run on Unikraft; admission stamps
// a class onto everything newer.
//
// Instance series carry no location label yet, so the region filter matches
// the location suffix of the generated name (<workload>-<placement>-<location>-<index>).
local instanceInfoSelector = 'datum_cloud_compute_instance_info{name=~".*-${region}-[0-9]+", "milo.project.name"!~"${exclude_projects}"}';

// Series are carried over scrape gaps of up to this long, so a missed scrape
// does not read as instances disappearing or turning not Ready.
local gap = '10m';
local instanceInfo = 'last_over_time(%s[%s])' % [instanceInfoSelector, gap];
local readyTrue = 'last_over_time(datum_cloud_compute_instance_status_condition{condition="Ready", status="True"}[%s])' % gap;

local withRuntime(expr) = |||
  label_match(
    label_replace(label_replace(%s, "runtime", "$1", "runtime_class", "(.+)"), "runtime", "unikernel", "runtime_class", ""),
    "runtime", "$runtime"
  )
||| % expr;

local withLocation(expr) = 'label_replace(%s, "location", "$1", "name", ".*-([a-z]+-[a-z]+-[0-9]+)-[0-9]+")' % expr;

local instanceKey = 'name, namespace, "milo.project.name"';
local projectKey = '"milo.project.name"';

// Instances present at the end of the range, at its start, or at any point
// during it.
local instancesByKey = 'count by (%s, runtime) (%s)' % [instanceKey, withRuntime(instanceInfo)];
local instancesAtStart = 'count by (%s, runtime) (%s)' % [instanceKey, withRuntime('last_over_time(%s[%s] offset $__range)' % [instanceInfoSelector, gap])];
local instancesInRange = 'count by (%s, runtime) (%s)' % [instanceKey, withRuntime('count_over_time(%s[$__range])' % instanceInfoSelector)];

local projectsOf(instances) = 'count by (%s) (%s)' % [projectKey, instances];

// Not Ready instances with their location and the reason on their Ready
// condition.
local readyReason = 'max by (%s, reason) (last_over_time(datum_cloud_compute_instance_status_condition{condition="Ready", status!="True"}[10m]))' % instanceKey;
local notReady = withLocation(|||
  (
    (%(instances)s unless on (%(key)s) %(ready)s)
    * on (%(key)s) group_left (reason) %(reason)s
  )
  or
  (%(instances)s unless on (%(key)s) %(ready)s unless on (%(key)s) %(reason)s)
||| % { instances: instancesByKey, key: instanceKey, reason: readyReason, ready: readyTrue });

local deploymentsNotReady = |||
  count by (name, namespace, "milo.project.name") (datum_cloud_compute_workload_deployment_info{name=~".*-${region}", "milo.project.name"!~"${exclude_projects}"})
  unless on (name, namespace, "milo.project.name")
  datum_cloud_compute_workload_deployment_status_condition{condition="ReplicasReady", status="True"}
|||;

// Both runtime classes at zero, so per-runtime trends draw a baseline instead
// of dropping a runtime that has nothing to show.
local runtimeZeros = |||
  label_match(
    label_replace(vector(0), "runtime", "general-purpose", "", "") or label_replace(vector(0), "runtime", "unikernel", "", ""),
    "runtime", "$runtime"
  )
|||;

// ---------------------------------------------------------------------------
// Instance pods on the cells
// ---------------------------------------------------------------------------

// Pod series carry no provider label, so the runtime is derived from the
// RuntimeClass for Kata pods and the virtual node for Unikraft pods. Only
// instance pods match either side.
local kataRuntimeClasses = 'kata-clh|kata-qemu|katars';
local podRuntime = |||
  label_match(
    max by (cluster, namespace, pod, region, runtime) (
      label_replace(kube_pod_runtimeclass_name_info{runtimeclass_name=~"%s", region=~"$region"}, "runtime", "general-purpose", "", "")
      or
      label_replace(kube_pod_info{node=~"kraftlet-.*", region=~"$region"}, "runtime", "unikernel", "", "")
    ),
    "runtime", "$runtime"
  )
||| % kataRuntimeClasses;

// Instance namespaces on a cell are named after the project's cluster.
local namespaceProject = |||
  label_replace(
    max by (cluster, namespace, label_meta_datumapis_com_upstream_cluster_name) (kube_namespace_labels{label_meta_datumapis_com_upstream_cluster_name=~"cluster-.+"}),
    "project", "$1", "label_meta_datumapis_com_upstream_cluster_name", "cluster-(.*)"
  )
|||;

// Instance pods with their runtime and project, outside excluded projects.
local instancePods = 'label_mismatch(%s * on (cluster, namespace) group_left (project) %s, "project", "${exclude_projects}")' % [podRuntime, namespaceProject];

// Adds runtime and project labels to a per-pod expression and drops anything
// that is not an instance pod.
local forInstances(expr) =
  '(%s) * on (cluster, namespace, pod) group_left (runtime, project) %s' % [expr, instancePods];

// Zero for every region and runtime with instance pods, so the breakdown lists
// healthy combinations.
local zeroByRegionRuntime = 'count by (region, runtime) (%s) * 0' % instancePods;

// Not Ready instances that have no pod, such as those still provisioning or
// waiting on quota. Suspended instances have no pod on purpose.
local notReadyWithoutPod = |||
  label_mismatch(
    label_replace(label_replace(%(notReady)s, "project", "$1", "milo.project.name", "(.*)"), "pod", "$1", "name", "(.*)")
    unless on (project, pod) %(pods)s,
    "reason", "Suspended"
  )
||| % { notReady: notReady, pods: instancePods };

// Who is at fault. Bad images, crashing applications and invalid container
// configuration are the customer's to fix; anything else that keeps an
// instance from running, including failures to create or start the
// container, is the platform's.
local customerReasons = 'ErrImagePull|ImagePullBackOff|InvalidImageName|ImageInspectError|RegistryUnavailable|CrashLoopBackOff|CreateContainerConfigError';

local podCreated = 'max by (cluster, namespace, pod) (kube_pod_created{region=~"$region"})';

// Starting pods pass through waiting and pending; only flag them once they
// are older than ten minutes.
local oldPods = '%s < time() - 600' % podCreated;

local waitingWith(match) =
  'max by (cluster, namespace, pod, region, problem) (label_replace(kube_pod_container_status_waiting_reason{reason%s, region=~"$region"} == 1, "problem", "$1", "reason", "(.*)"))' % match;

local customerIssues = forInstances(waitingWith('=~"%s"' % customerReasons));

local platformIssues = forInstances(|||
  (%(platformWaiting)s and on (cluster, namespace, pod) %(old)s unless on (cluster, namespace, pod) kube_pod_status_unschedulable == 1)
  or label_replace(max by (cluster, namespace, pod, region) (kube_pod_status_unschedulable{region=~"$region"} == 1), "problem", "Unschedulable", "", "")
  or (
    label_replace(max by (cluster, namespace, pod, region) (kube_pod_status_phase{phase="Pending", region=~"$region"} == 1), "problem", "Pending", "", "")
    and on (cluster, namespace, pod) %(old)s
    unless on (cluster, namespace, pod) (kube_pod_container_status_waiting_reason == 1 or kube_pod_status_unschedulable == 1)
  )
  or label_replace(max by (cluster, namespace, pod, region) ((time() - kube_pod_deletion_timestamp{region=~"$region"}) > bool 300) == 1, "problem", "Stuck deleting", "", "")
||| % { platformWaiting: waitingWith('!~"%s"' % customerReasons), old: oldPods });

// The same issues with the pod's age as the value.
local withAge(issues) = '(%s) * on (cluster, namespace, pod) group_left () (time() - %s)' % [issues, podCreated];

local issueCount(issues) = 'count(count by (cluster, namespace, pod) (%s)) or vector(0)' % issues;
local issueCountBy(issues) = 'count by (region, runtime) (count by (cluster, namespace, pod, region, runtime) (%s)) or %s' % [issues, zeroByRegionRuntime];

local restartsOver(window) = forInstances(
  'sum by (cluster, namespace, pod, region) (increase(kube_pod_container_status_restarts_total{region=~"$region"}[%s]))' % window
);
local restartRate = forInstances(
  'sum by (cluster, namespace, pod, region) (rate(kube_pod_container_status_restarts_total{region=~"$region"}[$__rate_interval]))'
);

// Restarts over the range with the reason the container last terminated,
// where the runtime reports one.
local lastTerminated = 'max by (cluster, namespace, pod, reason) (kube_pod_container_status_last_terminated_reason)';
local restartsWithReason = |||
  ((%(restarts)s) > 0) * on (cluster, namespace, pod) group_left (reason) %(last)s
  or
  (((%(restarts)s) > 0) unless on (cluster, namespace, pod) %(last)s)
||| % { restarts: restartsOver('$__range'), last: lastTerminated };

// ---------------------------------------------------------------------------
// Compute hosts
// ---------------------------------------------------------------------------

local hosts = 'max by (cluster, node) (host:compute_host:info{region=~"$region"})';

// Carried over hostLookback so a host that stopped reporting keeps its place.
local hostCapacity(resource) = 'max by (cluster, region, node) (last_over_time(host:compute_capacity:sum{resource="%s", region=~"$region"}[%s]))' % [resource, hostLookback];
local hostCpu = hostCapacity('cpu');
local hostMemoryTotal = hostCapacity('memory');
local hostCpuUsed(window) = 'sum by (cluster, region, node) (rate(node_cpu_seconds_total{mode!="idle"}[%s]) and on (cluster, node) %s)' % [window, hosts];
local hostMemoryUsed = 'max by (cluster, region, node) ((node_memory_MemTotal_bytes - node_memory_MemAvailable_bytes) and on (cluster, node) %s)' % hosts;

local committedBy(resource) = 'host:compute_committed:sum{resource="%s", region=~"$region"}' % resource;

local committed(resource) = 'sum by (cluster, region, node) (%s)' % committedBy(resource);

// How many more default-size instances fit on each host, by the scarcer of
// CPU and memory.
local headroomSlots = |||
  floor(clamp_min(
    ((%(cpuFree)s) <= on (cluster, region, node) (%(memFree)s))
    or
    (%(memFree)s),
    0
  ))
||| % {
  cpuFree: '(%s - %s) / %d' % [hostCpu, committed('cpu'), slotCpu],
  memFree: '(%s - %s) / %d' % [hostMemoryTotal, committed('memory'), slotMemoryBytes],
};

// Time until committed capacity reaches the host total at the rate it grew
// over the time range, in seconds. A host already at its total reads zero;
// one whose commitments did not grow has no value.
local timeUntilFull(resource, total) = |||
  (clamp_min(%(total)s - %(committed)s, 0) / (deriv((%(committed)s)[$__range:$__interval]) > 0))
  or
  (((%(total)s - %(committed)s) <= 0) * 0)
||| % { total: total, committed: committed(resource) };

local computeNodes = |||
  (
    %s
    or
    kube_node_info{node=~"kraftlet-.*", region=~"$region"}
  )
||| % hosts;
local computeNodeReady = |||
  max by (region, node) (
    kube_node_status_condition{condition="Ready", status="true", region=~"$region"}
    and on (cluster, node) %s
  )
||| % computeNodes;

local providersAvailable = 'max by (region, deployment) (kube_deployment_status_replicas_unavailable{namespace=~"kata-system|unikraft-system", deployment=~"kata-provider|unikraft-provider", region=~"$region"} == bool 0)';
local runtimeDaemonSetsAvailable = 'max by (region, daemonset) (kube_daemonset_status_number_unavailable{namespace=~"unikraft-system|kata-runtime-install", region=~"$region"} == bool 0)';

local controllerFilter = 'job="compute-metrics"';

// ---------------------------------------------------------------------------
// Query helpers
// ---------------------------------------------------------------------------

local qRange(expr, legend) =
  prometheus.new(ds, expr)
  + prometheus.withLegendFormat(legend);

local qInstant(expr, legend) =
  qRange(expr, legend)
  + prometheus.withInstant(true);

local qTable(expr, refId='A') =
  prometheus.new(ds, expr)
  + prometheus.withInstant(true)
  + prometheus.withFormat('table')
  + prometheus.withRefId(refId);

// ---------------------------------------------------------------------------
// Colours and thresholds
// ---------------------------------------------------------------------------

local hostTotalColor = '#8e8e8e';

// The same runtime is the same colour in every panel.
local seriesColors = [
  { matcher: { id: 'byName', options: s.name }, properties: [{ id: 'color', value: { mode: 'fixed', fixedColor: s.color } }] }
  for s in [
    { name: 'general-purpose', color: 'blue' },
    { name: 'unikernel', color: 'purple' },
    { name: 'system', color: hostTotalColor },
  ]
];

local alarm = [{ color: 'green', value: null }, { color: 'red', value: 1 }];
local attention = [{ color: 'green', value: null }, { color: 'orange', value: 1 }];
local neutral = [{ color: 'blue', value: null }];

// Table cells colour only their text, and only when something needs a look.
local alarmText = [{ color: 'text', value: null }, { color: 'red', value: 1 }];
local utilisationText = [{ color: 'text', value: null }, { color: 'orange', value: 0.8 }, { color: 'red', value: 0.95 }];
local headroomText = [{ color: 'red', value: null }, { color: 'orange', value: 1 }, { color: 'text', value: 4 }];
local fullMapping = [{ type: 'value', options: { '0': { text: 'full', color: 'red', index: 0 } } }];
local timeLeftText = [{ color: 'red', value: null }, { color: 'orange', value: 14 * 86400 }, { color: 'text', value: 30 * 86400 }];

// ---------------------------------------------------------------------------
// Panel builders
// ---------------------------------------------------------------------------

local statPanel(title, description, target, gridPos, thresholdSteps, unit='short') =
  stat.new(title)
  + stat.panelOptions.withDescription(description)
  + stat.panelOptions.withGridPos(gridPos.h, gridPos.w, gridPos.x, gridPos.y)
  + stat.queryOptions.withDatasource('prometheus', ds)
  + stat.queryOptions.withTargets([target])
  + stat.options.withColorMode('value')
  + stat.options.withGraphMode('none')
  + stat.options.withJustifyMode('center')
  + stat.options.withTextMode('value')
  + stat.options.reduceOptions.withCalcs(['lastNotNull'])
  + stat.options.reduceOptions.withFields('')
  + stat.options.reduceOptions.withValues(false)
  + stat.standardOptions.withUnit(unit)
  + stat.standardOptions.withDecimals(0)
  + stat.standardOptions.color.withMode('thresholds')
  + stat.standardOptions.thresholds.withMode('absolute')
  + stat.standardOptions.thresholds.withSteps(thresholdSteps);

local tsPanel(title, description, targets, gridPos, unit, stacked=false, overrides=[]) =
  timeSeries.new(title)
  + timeSeries.panelOptions.withDescription(description)
  + timeSeries.panelOptions.withGridPos(gridPos.h, gridPos.w, gridPos.x, gridPos.y)
  + timeSeries.queryOptions.withDatasource('prometheus', ds)
  + timeSeries.queryOptions.withTargets(targets)
  + timeSeries.standardOptions.withUnit(unit)
  + timeSeries.standardOptions.withMin(0)
  + timeSeries.options.legend.withDisplayMode('list')
  + timeSeries.options.legend.withPlacement('bottom')
  + timeSeries.options.tooltip.withMode('multi')
  + timeSeries.options.tooltip.withSort('desc')
  + timeSeries.fieldConfig.defaults.custom.withDrawStyle('line')
  + timeSeries.fieldConfig.defaults.custom.withLineWidth(1)
  + timeSeries.fieldConfig.defaults.custom.withFillOpacity(if stacked then 25 else 10)
  + timeSeries.fieldConfig.defaults.custom.withShowPoints('never')
  + timeSeries.fieldConfig.defaults.custom.stacking.withMode(if stacked then 'normal' else 'none')
  + timeSeries.standardOptions.withOverrides(seriesColors + overrides);

// columns lists the label columns to show, in order. values lists the value
// columns: with one target the value field is "Value", with several it is
// "Value #<refId>". Every other column is hidden. Small summary tables show
// every row; long lists paginate.
local tablePanel(title, description, targets, gridPos, columns, values=[], noValue='None', paginate=false, sortBy=null, order=null) =
  local fields = if order == null then [c.label for c in columns] + [v.field for v in values] else order;
  table.new(title)
  + table.panelOptions.withDescription(description)
  + table.panelOptions.withGridPos(gridPos.h, gridPos.w, gridPos.x, gridPos.y)
  + table.queryOptions.withDatasource('prometheus', ds)
  + table.queryOptions.withTargets(targets)
  + table.queryOptions.withTransformations(
    (if std.length(targets) > 1 then [{ id: 'merge', options: {} }] else [])
    + [
      { id: 'filterFieldsByName', options: { include: { names: fields } } },
      {
        id: 'organize',
        options: {
          indexByName: { [fields[i]]: i for i in std.range(0, std.length(fields) - 1) },
          renameByName: { [c.label]: c.name for c in columns } + { [v.field]: v.name for v in values },
        },
      },
    ]
  )
  + table.standardOptions.withNoValue(noValue)
  + table.standardOptions.withOverrides([
    {
      matcher: { id: 'byName', options: v.name },
      properties: [{ id: 'unit', value: std.get(v, 'unit', 'short') }, { id: 'decimals', value: std.get(v, 'decimals', 0) }]
                  + (if std.objectHas(v, 'noValue') then [{ id: 'noValue', value: v.noValue }] else [])
                  + (if std.objectHas(v, 'mappings') then [{ id: 'mappings', value: v.mappings }] else [])
                  + (if std.objectHas(v, 'width') then [{ id: 'custom.width', value: v.width }] else [])
                  + (if std.objectHas(v, 'thresholds') then [
                       { id: 'custom.cellOptions', value: { type: 'color-text' } },
                       { id: 'color', value: { mode: 'thresholds' } },
                       { id: 'thresholds', value: { mode: 'absolute', steps: v.thresholds } },
                     ] else []),
    }
    for v in values
  ] + [
    { matcher: { id: 'byName', options: c.name }, properties: [{ id: 'custom.width', value: c.width }] }
    for c in columns
    if std.objectHas(c, 'width')
  ])
  + table.options.withCellHeight('sm')
  + table.options.footer.withEnablePagination(paginate)
  + (if sortBy == null then {} else table.options.withSortBy([sortBy]));

local stateTimelinePanel(title, description, targets, gridPos, mappings) =
  stateTimeline.new(title)
  + stateTimeline.panelOptions.withDescription(description)
  + stateTimeline.panelOptions.withGridPos(gridPos.h, gridPos.w, gridPos.x, gridPos.y)
  + stateTimeline.queryOptions.withDatasource('prometheus', ds)
  + stateTimeline.queryOptions.withTargets(targets)
  + stateTimeline.options.withMergeValues(true)
  + stateTimeline.options.withShowValue('never')
  + stateTimeline.options.withRowHeight(0.8)
  + stateTimeline.options.legend.withShowLegend(false)
  + stateTimeline.standardOptions.color.withMode('thresholds')
  + stateTimeline.standardOptions.thresholds.withMode('absolute')
  + stateTimeline.standardOptions.thresholds.withSteps([{ color: 'red', value: null }, { color: 'green', value: 1 }])
  + stateTimeline.standardOptions.withMappings([{ type: 'value', options: mappings }]);

local rowPanel(title, id, y, collapsed=false, panels=[]) =
  row.new(title)
  + row.withId(id)
  + row.withCollapsed(collapsed)
  + row.withPanels(panels)
  + { gridPos: { h: 1, w: 24, x: 0, y: y } };

// ---------------------------------------------------------------------------
// Variables
// ---------------------------------------------------------------------------

local datasourceVar =
  var.datasource.new('datasource', 'prometheus')
  + var.datasource.generalOptions.withLabel('Data Source')
  + var.datasource.generalOptions.withCurrent('Service Metrics', 'SERVICE_METRICS')
  + var.datasource.generalOptions.showOnDashboard.withLabelAndValue()
  + var.datasource.withRegex('');

local regionVar =
  var.query.new('region')
  + var.query.generalOptions.withLabel('region')
  + var.query.generalOptions.withCurrent('All', '$__all')
  + var.query.withDatasource('prometheus', ds)
  // Only regions with a compute host, so cells without compute get no panels.
  + var.query.queryTypes.withLabelValues(label='region', metric='kubelet_node_name{compute_datumapis_com_runtime!=""}')
  + var.query.refresh.onTime()
  + var.query.withSort(1)
  + var.query.selectionOptions.withMulti(true)
  + var.query.selectionOptions.withIncludeAll(true)
  + { allValue: '.*' };

local runtimeVar =
  var.custom.new('runtime', ['general-purpose', 'unikernel'])
  + var.custom.generalOptions.withLabel('runtime class')
  + var.custom.generalOptions.withCurrent('All', '$__all')
  + var.custom.generalOptions.withDescription('general-purpose runs on Kata; unikernel runs on Unikraft. Both share the compute hosts, so capacity always shows both.')
  + var.custom.selectionOptions.withMulti(true)
  + var.custom.selectionOptions.withIncludeAll(true)
  + { allValue: '.*' };

// Defaults to the projects datum-cloud/qa-synthetics deploys canary workloads
// into (production, staging).
local excludeProjectsVar =
  var.textbox.new('exclude_projects', 'project-l6g7z|project-8b278')
  + var.textbox.generalOptions.withLabel('exclude projects')
  + var.textbox.generalOptions.withDescription('Regex of project names to leave out of instance panels, such as synthetic test projects. Capacity always counts every project. Clear it to include everything.');

// ---------------------------------------------------------------------------
// Health: is the platform OK, and where not?
// ---------------------------------------------------------------------------

local statPlatformIssues = statPanel(
  'Platform issues',
  'Instances the platform is failing to run: unschedulable, stuck deleting, failing to create or start their container, or pending for more than ten minutes without a customer-side cause.',
  qInstant(issueCount(platformIssues), 'platform'),
  { h: 4, w: 5, x: 0, y: 1 },
  alarm,
);

local statNodesNotReady = statPanel(
  'Compute nodes not Ready',
  'Compute hosts and their Unikraft virtual nodes that are not Ready, including hosts that stopped reporting in the last day.',
  qInstant('count((%s) == 0) or vector(0)' % computeNodeReady, 'not ready'),
  { h: 4, w: 5, x: 5, y: 1 },
  alarm,
);

local statComponentsDown = statPanel(
  'Runtime components down',
  'Provider controllers and per-host runtime DaemonSets with pods unavailable. See Runtime platform below.',
  qInstant('count((%s) == 0 or (%s) == 0) or vector(0)' % [providersAvailable, runtimeDaemonSetsAvailable], 'down'),
  { h: 4, w: 5, x: 10, y: 1 },
  alarm,
);

local statNotReadyWithoutPod = statPanel(
  'Not Ready, no pod',
  'Instances that are not Ready and have no pod yet, such as those stuck provisioning or waiting on quota. Suspended instances are left out. See Not Ready in Instances below for reasons.',
  qInstant('count(%s) or vector(0)' % notReadyWithoutPod, 'no pod'),
  { h: 4, w: 5, x: 15, y: 1 },
  attention,
);

local statCustomerIssues = statPanel(
  'Customer issues',
  'Instances that cannot run because of the customer: image pull failures, crashing applications, invalid container configuration.',
  qInstant(issueCount(customerIssues), 'customer'),
  { h: 4, w: 4, x: 20, y: 1 },
  neutral,
);

local tableHealthByRegion = tablePanel(
  'By region and runtime',
  'Instance pods per region and runtime class, and how many cannot run because of a platform or a customer issue.',
  [
    qTable('count by (region, runtime) (%s)' % instancePods, 'A'),
    qTable(issueCountBy(platformIssues), 'B'),
    qTable(issueCountBy(customerIssues), 'C'),
  ],
  { h: 9, w: 12, x: 0, y: 5 },
  [
    { label: 'region', name: 'Region' },
    { label: 'runtime', name: 'Runtime class' },
  ],
  [
    { field: 'Value #A', name: 'Instances' },
    { field: 'Value #B', name: 'Platform', thresholds: alarmText },
    { field: 'Value #C', name: 'Customer' },
  ],
  sortBy={ displayName: 'Region', desc: false },
);

// Fixed widths for the short columns leave the instance name the rest.
local regionColumn = { label: 'region', name: 'Region', width: 105 };
local runtimeColumn = { label: 'runtime', name: 'Runtime class', width: 125 };
local projectColumn = { label: 'project', name: 'Project', width: 130 };

local issueColumns = [
  regionColumn,
  runtimeColumn,
  projectColumn,
  { label: 'pod', name: 'Instance' },
  { label: 'problem', name: 'Problem', width: 125 },
];

local tablePlatformIssues = tablePanel(
  'Platform issues',
  'Each instance the platform is failing to run, why, and how old its pod is.',
  [qTable(withAge(platformIssues))],
  { h: 9, w: 12, x: 12, y: 5 },
  issueColumns,
  [{ field: 'Value', name: 'Age', unit: 's', decimals: 1, width: 85 }],
  noValue='No platform issues',
  paginate=true,
  sortBy={ displayName: 'Age', desc: true },
);

// ---------------------------------------------------------------------------
// End-to-end: can a customer deploy and use a workload, and how long
// does it take?
// ---------------------------------------------------------------------------

// datum-cloud/qa-synthetics deploys a workload in every location and runtime
// class every 20 minutes from outside the platform, then checks each step a
// customer depends on and destroys it again. Each run pushes one sample per
// series, timed from the deploy request.
local probeSelector(metric, extra='') =
  '%s{check="scenario-compute-workload-lifecycle"%s}' % [metric, extra];

// The probe labels results with the city code it deploys to (region="dfw").
// Until it reports the region, map city to region from the probe's own
// instances, whose names carry both: <runtime>-<city>-<hash>-<placement>-<region>-<index>.
// Values with no mapping pass through unchanged, then the region filter
// applies.
local probeCityRegions = |||
  group by (region, location) (
    label_replace(
      label_replace(
        count_over_time(datum_cloud_compute_instance_info{name=~"(general-purpose|unikernel)-[a-z]{3}-[0-9a-f]{8}-.+"}[$__range]),
        "region", "$1", "name", "(?:general-purpose|unikernel)-([a-z]{3})-[0-9a-f]{8}-.+"
      ),
      "location", "$1", "name", ".*-([a-z]+-[a-z]+-[0-9]+)-[0-9]+"
    )
  )
|||;
local probeInRegion(expr) = |||
  label_match(
    label_replace(
      ((%(expr)s) * on (region) group_left (location) %(map)s)
      or
      ((%(expr)s) unless on (region) %(map)s),
      "region", "$1", "location", "(.+)"
    ),
    "region", "$region"
  )
||| % { expr: expr, map: probeCityRegions };
local probeRuntime(expr) = |||
  label_match(label_replace(%s, "runtime", "$1", "runtime_class", "(.*)"), "runtime", "$runtime")
||| % expr;

// Share of runs in the time range where a step passed. A step skipped because
// an earlier one failed is not counted for it.
local stepPassRate(stage, passed) =
  local runs(result) = 'sum by (region, runtime) (%s)' % probeRuntime('count_over_time(%s[$__range])' % probeSelector('qa_compute_lifecycle_stage_result', ', stage="%s", result%s' % [stage, result]));
  '(%(passed)s or %(tried)s * 0) / %(tried)s' % { passed: runs('=~"%s"' % passed), tried: runs('!="skipped"') };

local steps = [
  { stage: 'deploy', passed: 'deployed', name: 'Deploy' },
  { stage: 'dns', passed: 'resolved', name: 'DNS' },
  { stage: 'http', passed: 'success|warning', name: 'HTTP' },
  { stage: 'outbound_ipv4', passed: 'success', name: 'IPv4 out' },
  { stage: 'outbound_ipv6', passed: 'success', name: 'IPv6 out' },
  { stage: 'destroy', passed: 'destroyed', name: 'Destroy' },
];
local stepRefs = ['A', 'B', 'C', 'D', 'E', 'F'];

local servingSeconds = probeSelector('qa_compute_lifecycle_stage_seconds', ', stage="http_stable"');
local servingQuantile(q) = 'max by (region, runtime) (%s)' % probeRuntime('quantile_over_time(%s, %s[$__range])' % [q, servingSeconds]);

local tableJourney = tablePanel(
  'Can a customer deploy and use a workload, and how fast?',
  'Per region and runtime class, over probe runs in the time range: the share of runs where each step passed (the workload deployed, its hostname resolved, it served HTTP, it reached the internet over IPv4 and IPv6 (IPv4 out, IPv6 out), it was destroyed again), then how long it took from the deploy request until the workload served HTTP reliably.',
  [qTable(probeInRegion(stepPassRate(steps[i].stage, steps[i].passed)), stepRefs[i]) for i in std.range(0, std.length(steps) - 1)]
  + [qTable(probeInRegion(servingQuantile(0.5)), 'G'), qTable(probeInRegion(servingQuantile(0.95)), 'H')],
  { h: 8, w: 16, x: 0, y: 15 },
  [
    { label: 'region', name: 'Region', width: 110 },
    { label: 'runtime', name: 'Runtime class', width: 130 },
  ],
  [
    { field: 'Value #' + stepRefs[i], name: steps[i].name, unit: 'percentunit', decimals: 1, width: 85, thresholds: [{ color: 'red', value: null }, { color: 'orange', value: 0.95 }, { color: 'green', value: 0.99 }], noValue: 'not run' }
    for i in std.range(0, std.length(steps) - 1)
  ] + [
    { field: 'Value #G', name: 'Serving p50', unit: 's', decimals: 1, width: 100 },
    { field: 'Value #H', name: 'Serving p95', unit: 's', decimals: 1 },
  ],
  noValue='No probe runs',
  sortBy={ displayName: 'Region', desc: false },
);

// Runs come every 20 minutes, so each point covers the runs in the three
// hours before it.
local tsServing = tsPanel(
  'Time to serving, p95',
  'Slowest 5% of probe runs over the preceding three hours: time from the deploy request until the workload answered HTTP reliably.',
  [qRange(probeInRegion('max by (region, runtime) (%s)' % probeRuntime('quantile_over_time(0.95, %s[3h])' % servingSeconds)), '{{region}} {{runtime}}')],
  { h: 8, w: 8, x: 16, y: 15 },
  's',
)
+ timeSeries.queryOptions.withInterval('20m')
+ timeSeries.fieldConfig.defaults.custom.withFillOpacity(0);

// ---------------------------------------------------------------------------
// Capacity: is there room?
// ---------------------------------------------------------------------------

local tableHosts = tablePanel(
  'Compute hosts',
  'Per host: share of the host\'s CPU and memory committed to instances and system pods; peak share actually used over the time range; headroom, the number of default-size instances (1 vCPU, 2 GiB) that still fit; and how long until committed CPU and memory reach the host total at the growth rate over the time range.',
  [
    qTable('%s / %s' % [committed('cpu'), hostCpu], 'A'),
    qTable('max_over_time((%s)[$__range:5m]) / %s' % [hostCpuUsed('5m'), hostCpu], 'B'),
    qTable('%s / %s' % [committed('memory'), hostMemoryTotal], 'C'),
    qTable('max_over_time((%s)[$__range:5m]) / %s' % [hostMemoryUsed, hostMemoryTotal], 'D'),
    qTable(headroomSlots, 'E'),
    qTable(timeUntilFull('cpu', hostCpu), 'F'),
    qTable(timeUntilFull('memory', hostMemoryTotal), 'G'),
  ],
  { h: 6, w: 24, x: 0, y: 24 },
  [
    { label: 'region', name: 'Region', width: 120 },
    { label: 'node', name: 'Host', width: 300 },
  ],
  [
    { field: 'Value #A', name: 'CPU committed', unit: 'percentunit', thresholds: utilisationText },
    { field: 'Value #B', name: 'CPU peak', unit: 'percentunit', decimals: 1, thresholds: utilisationText },
    { field: 'Value #C', name: 'Memory committed', unit: 'percentunit', thresholds: utilisationText },
    { field: 'Value #D', name: 'Memory peak', unit: 'percentunit', decimals: 1, thresholds: utilisationText },
    { field: 'Value #E', name: 'Headroom', thresholds: headroomText },
    { field: 'Value #F', name: 'CPU full in', unit: 's', decimals: 1, thresholds: timeLeftText, noValue: 'not growing', mappings: fullMapping },
    { field: 'Value #G', name: 'Memory full in', unit: 's', decimals: 1, thresholds: timeLeftText, noValue: 'not growing', mappings: fullMapping },
  ],
  sortBy={ displayName: 'Region', desc: false },
);

local hostLines = [
  {
    matcher: { id: 'byName', options: 'host total' },
    properties: [
      { id: 'custom.stacking', value: { mode: 'none' } },
      { id: 'custom.fillOpacity', value: 0 },
      { id: 'custom.lineStyle', value: { fill: 'dash', dash: [10, 10] } },
      { id: 'color', value: { mode: 'fixed', fixedColor: hostTotalColor } },
    ],
  },
  {
    matcher: { id: 'byName', options: 'used' },
    properties: [
      { id: 'custom.stacking', value: { mode: 'none' } },
      { id: 'custom.fillOpacity', value: 0 },
      { id: 'custom.lineWidth', value: 2 },
      { id: 'color', value: { mode: 'fixed', fixedColor: 'text' } },
    ],
  },
];

// One panel per region: capacity cannot move between hosts in different
// regions, so a fleet-wide sum would hide a full host.
local perRegion(panel) =
  panel
  + timeSeries.panelOptions.withRepeat('region')
  + timeSeries.panelOptions.withRepeatDirection('h')
  + timeSeries.panelOptions.withMaxPerRow(3);

local tsCommittedCpu = perRegion(tsPanel(
  'CPU committed vs host: $region',
  'CPU committed on the region\'s compute host by owner, stacked, against the host\'s total and actual use.',
  [
    qRange('sum by (owner) (%s)' % committedBy('cpu'), '{{owner}}'),
    qRange('sum(%s)' % hostCpu, 'host total'),
    qRange('sum(%s)' % hostCpuUsed('$__rate_interval'), 'used'),
  ],
  { h: 8, w: 8, x: 0, y: 30 },
  'suffix: cores',
  stacked=true,
  overrides=hostLines,
));

local tsCommittedMemory = perRegion(tsPanel(
  'Memory committed vs host: $region',
  'Memory committed on the region\'s compute host by owner, stacked, against the host\'s total and actual use.',
  [
    qRange('sum by (owner) (%s)' % committedBy('memory'), '{{owner}}'),
    qRange('sum(%s)' % hostMemoryTotal, 'host total'),
    qRange('sum(%s)' % hostMemoryUsed, 'used'),
  ],
  { h: 8, w: 8, x: 0, y: 38 },
  'bytes',
  stacked=true,
  overrides=hostLines,
));

// ---------------------------------------------------------------------------
// Demand: where is it going?
// ---------------------------------------------------------------------------

local createdInstances = '%s unless on (%s) %s' % [instancesInRange, instanceKey, instancesAtStart];
local deletedInstances = '%s unless on (%s) %s' % [instancesInRange, instanceKey, instancesByKey];
local newProjects = '%s unless on (%s) %s' % [projectsOf(instancesByKey), projectKey, projectsOf(instancesAtStart)];
local goneProjects = '%s unless on (%s) %s' % [projectsOf(instancesAtStart), projectKey, projectsOf(instancesByKey)];

local statCreated = statPanel(
  'Instances created',
  'Instances created in the time range, including any already deleted again.',
  qInstant('count(%s) or vector(0)' % createdInstances, 'created'),
  { h: 3, w: 4, x: 0, y: 47 },
  neutral,
);

local statDeleted = statPanel(
  'Instances deleted',
  'Instances that existed in the time range and are gone now.',
  qInstant('count(%s) or vector(0)' % deletedInstances, 'deleted'),
  { h: 3, w: 4, x: 0, y: 50 },
  neutral,
);

local statNewProjects = statPanel(
  'New projects',
  'Projects with instances now that had none at the start of the time range.',
  qInstant('count(%s) or vector(0)' % newProjects, 'new'),
  { h: 3, w: 4, x: 0, y: 53 },
  neutral,
);

local statGoneProjects = statPanel(
  'Projects that left',
  'Projects that had instances at the start of the time range and have none now.',
  qInstant('count(%s) or vector(0)' % goneProjects, 'left'),
  { h: 3, w: 4, x: 0, y: 56 },
  neutral,
);

local tsInstances = tsPanel(
  'Instances by runtime',
  'Stacked, so the top of the stack is the total.',
  [qRange('count by (runtime) (%s) or %s' % [instancesByKey, runtimeZeros], '{{runtime}}')],
  { h: 12, w: 7, x: 4, y: 47 },
  'short',
  stacked=true,
);

local tsInstancesByRegion = tsPanel(
  'Instances by region',
  'Stacked, so the top of the stack is the total.',
  [qRange('count by (location) (%s)' % withLocation(instancesByKey), '{{location}}')],
  { h: 12, w: 7, x: 11, y: 47 },
  'short',
  stacked=true,
);

local tsProjects = tsPanel(
  'Projects by runtime',
  'Projects with at least one instance. A project using both runtime classes is counted under each.',
  [qRange('count by (runtime) (count by (%s, runtime) (%s)) or %s' % [projectKey, instancesByKey, runtimeZeros], '{{runtime}}')],
  { h: 12, w: 6, x: 18, y: 47 },
  'short',
);

// ---------------------------------------------------------------------------
// Instances (collapsed)
// ---------------------------------------------------------------------------

local tableCustomerIssues = tablePanel(
  'Customer issues',
  'Each instance that cannot run because of the customer, and why.',
  [qTable(customerIssues)],
  { h: 9, w: 12, x: 0, y: 60 },
  issueColumns,
  noValue='No customer issues',
  paginate=true,
  sortBy={ displayName: 'Region', desc: false },
);

local tableRestarts = tablePanel(
  'Most restarted',
  'Instances that restarted in the time range, with the reason the container last terminated. OOMKilled means the instance ran out of memory.',
  [qTable('round(%s)' % restartsWithReason)],
  { h: 9, w: 12, x: 12, y: 60 },
  [
    regionColumn,
    runtimeColumn,
    projectColumn,
    { label: 'pod', name: 'Instance' },
    { label: 'reason', name: 'Last termination', width: 140 },
  ],
  [{ field: 'Value', name: 'Restarts' }],
  noValue='No restarts',
  paginate=true,
  sortBy={ displayName: 'Restarts', desc: true },
);

local tableNotReady = tablePanel(
  'Not Ready',
  'Instances without a Ready=True condition and the reason on that condition, including those blocked before a pod exists.',
  [qTable(notReady)],
  { h: 9, w: 12, x: 0, y: 69 },
  [
    regionColumn { label: 'location' },
    runtimeColumn,
    projectColumn { label: 'milo.project.name' },
    { label: 'name', name: 'Instance' },
    { label: 'reason', name: 'Reason', width: 140 },
  ],
  noValue='All instances Ready',
  paginate=true,
  sortBy={ displayName: 'Region', desc: false },
);

local tableDeploymentsNotReady = tablePanel(
  'Deployments not ready',
  'Workload deployments (one per workload and location) without ReplicasReady=True.',
  [qTable(deploymentsNotReady)],
  { h: 9, w: 12, x: 12, y: 69 },
  [
    { label: 'milo.project.name', name: 'Project' },
    { label: 'name', name: 'Deployment' },
  ],
  noValue='All deployments ready',
  paginate=true,
);

local tsRestarts = tsPanel(
  'Restarts per minute',
  'Container restarts across instances, by runtime class.',
  [qRange('sum by (runtime) (%s) * 60 or %s' % [restartRate, runtimeZeros], '{{runtime}}')],
  { h: 8, w: 24, x: 0, y: 78 },
  'short',
);

// ---------------------------------------------------------------------------
// Runtime platform (collapsed)
// ---------------------------------------------------------------------------

local stNodes = stateTimelinePanel(
  'Compute nodes Ready',
  'Red means the host (Kata) or its kraftlet virtual node (Unikraft) is not Ready, or stopped reporting.',
  [qRange(computeNodeReady, '{{region}} / {{node}}')],
  { h: 8, w: 12, x: 0, y: 61 },
  { '0': { text: 'not ready', color: 'red', index: 0 }, '1': { text: 'ready', color: 'green', index: 1 } },
);

local stComponents = stateTimelinePanel(
  'Runtime components',
  'Red means a provider controller or per-host runtime DaemonSet has pods unavailable.',
  [
    qRange(providersAvailable, '{{region}} / {{deployment}}'),
    qRange(runtimeDaemonSetsAvailable, '{{region}} / {{daemonset}}'),
  ],
  { h: 8, w: 12, x: 12, y: 61 },
  { '0': { text: 'unavailable', color: 'red', index: 0 }, '1': { text: 'available', color: 'green', index: 1 } },
);

// ---------------------------------------------------------------------------
// Controllers (collapsed)
// ---------------------------------------------------------------------------

local tsReconcileErrors = tsPanel(
  'Reconcile errors',
  'Reconcile errors per second for each compute controller.',
  [qRange('sum by (controller) (rate(controller_runtime_reconcile_errors_total{%s}[$__rate_interval]))' % controllerFilter, '{{controller}}')],
  { h: 8, w: 8, x: 0, y: 62 },
  'ops',
);

local tsReconcileLatency = tsPanel(
  'Reconcile duration p99',
  'How long one reconcile takes, 99th percentile, for each compute controller.',
  [qRange('histogram_quantile(0.99, sum by (controller, le) (rate(controller_runtime_reconcile_time_seconds_bucket{%s}[$__rate_interval])))' % controllerFilter, '{{controller}}')],
  { h: 8, w: 8, x: 8, y: 62 },
  's',
);

local tsQueueDepth = tsPanel(
  'Work queue depth',
  'Items waiting in each compute controller work queue. A queue that keeps growing means the controller is falling behind.',
  [qRange('max by (name) (workqueue_depth{%s})' % controllerFilter, '{{name}}')],
  { h: 8, w: 8, x: 16, y: 62 },
  'short',
);

// ---------------------------------------------------------------------------
// Dashboard assembly
// ---------------------------------------------------------------------------

g.dashboard.new('Compute / Overview')
+ g.dashboard.withUid('compute-overview')
+ g.dashboard.withDescription('Operating and scaling compute across the general-purpose (Kata) and unikernel (Unikraft) runtime classes: health, startup, host capacity and demand.')
+ g.dashboard.withTags(['compute'])
+ g.dashboard.withEditable(true)
+ g.dashboard.withRefresh('1m')
+ g.dashboard.withSchemaVersion(39)
+ g.dashboard.graphTooltip.withSharedCrosshair()
+ g.dashboard.withLinks([
  g.dashboard.link.dashboards.new('Compute dashboards', ['compute'])
  + g.dashboard.link.dashboards.options.withAsDropdown(true)
  + g.dashboard.link.dashboards.options.withIncludeVars(true)
  + g.dashboard.link.dashboards.options.withKeepTime(true),
])
+ g.dashboard.time.withFrom('now-24h')
+ g.dashboard.time.withTo('now')
+ g.dashboard.withVariables([
  datasourceVar,
  regionVar,
  runtimeVar,
  excludeProjectsVar,
])
+ g.dashboard.withPanels(
  panels=[
    rowPanel('Health', 100, 0),
    statPlatformIssues,
    statNodesNotReady,
    statComponentsDown,
    statNotReadyWithoutPod,
    statCustomerIssues,
    tableHealthByRegion,
    tablePlatformIssues,

    rowPanel('End-to-end', 200, 14),
    tableJourney,
    tsServing,

    rowPanel('Capacity', 300, 23),
    tableHosts,
    tsCommittedCpu,
    tsCommittedMemory,

    rowPanel('Demand', 400, 46),
    statCreated,
    statDeleted,
    statNewProjects,
    statGoneProjects,
    tsInstances,
    tsInstancesByRegion,
    tsProjects,

    rowPanel('Instances', 500, 59, collapsed=true, panels=[
      tableCustomerIssues,
      tableRestarts,
      tableNotReady,
      tableDeploymentsNotReady,
      tsRestarts,
    ]),
    rowPanel('Runtime platform', 600, 60, collapsed=true, panels=[
      stNodes,
      stComponents,
    ]),
    rowPanel('Controllers', 700, 61, collapsed=true, panels=[
      tsReconcileErrors,
      tsReconcileLatency,
      tsQueueDepth,
    ]),
  ],
  setPanelIDs=true,
)
