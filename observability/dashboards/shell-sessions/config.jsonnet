// Instance shell sessions dashboard
//
// One view of the shell-session feature from both ends: what projects see
// (sessions created, open, ended, how long connecting takes) and what the
// cells serving them see (agent reachability, per-agent load, claim outcomes,
// unconfirmed cleanups). The session metrics come from the management-plane
// session controller; the agent metrics come from the shell agents on each
// cell. Both are labelled so the same $location / $cell selection narrows the
// two halves together.
//
// Usage:
//   jsonnet -J vendor config.jsonnet > dashboard.json
// Or:
//   task dashboards:build

local g = import 'github.com/grafana/grafonnet/gen/grafonnet-v11.4.0/main.libsonnet';

local prometheus    = g.query.prometheus;
local loki          = g.query.loki;
local stat          = g.panel.stat;
local timeSeries    = g.panel.timeSeries;
local stateTimeline = g.panel.stateTimeline;
local logs          = g.panel.logs;
local text          = g.panel.text;
local row           = g.panel.row;
local var           = g.dashboard.variable;

local ds = '${datasource}';

// The platform Loki datasource. Its UID is fixed by the infra repo's
// GrafanaDatasource, so the logs panel can bind to it without a variable.
local logsDs = { type: 'loki', uid: 'service-logs' };

// Reasons that mean the platform, not the requester, ended the session before
// it could be used. Everything else (Completed, Expired, Disconnected, Revoked,
// NotConnected, Invalid, ...) is either a normal end or a request the
// requester can fix.
local platformFailureReasons = 'Unavailable|AgentLost|AgentShutdown';

// ---------------------------------------------------------------------------
// Query helpers
// ---------------------------------------------------------------------------

local qRange(expr, legend) =
  prometheus.new(ds, expr)
  + prometheus.withLegendFormat(legend);

local qInstant(expr, legend) =
  qRange(expr, legend)
  + prometheus.withInstant(true);

// ---------------------------------------------------------------------------
// Panel builders
// ---------------------------------------------------------------------------

local statPanel(title, description, target, gridPos, unit, thresholdSteps) =
  stat.new(title)
  + stat.panelOptions.withDescription(description)
  + stat.panelOptions.withGridPos(gridPos.h, gridPos.w, gridPos.x, gridPos.y)
  + stat.queryOptions.withDatasource('prometheus', ds)
  + stat.queryOptions.withTargets([target])
  + stat.options.withColorMode('value')
  + stat.options.withGraphMode('area')
  + stat.options.withJustifyMode('center')
  + stat.options.withTextMode('value')
  + stat.options.reduceOptions.withCalcs(['lastNotNull'])
  + stat.options.reduceOptions.withFields('')
  + stat.options.reduceOptions.withValues(false)
  + stat.standardOptions.withUnit(unit)
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
  + timeSeries.standardOptions.withOverrides(overrides);

local rowPanel(title, id, y) =
  row.new(title)
  + row.withId(id)
  + row.withCollapsed(false)
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

local multiVar(name, label, metric, valueLabel, description) =
  var.query.new(name)
  + var.query.generalOptions.withLabel(label)
  + var.query.generalOptions.withCurrent('All', '$__all')
  + var.query.generalOptions.withDescription(description)
  + var.query.withDatasource('prometheus', ds)
  + var.query.queryTypes.withLabelValues(label=valueLabel, metric=metric)
  + var.query.refresh.onTime()
  + var.query.withSort(1)
  + var.query.selectionOptions.withMulti(true)
  + var.query.selectionOptions.withIncludeAll(true)
  + { allValue: '.*' };

local projectVar = multiVar(
  'project', 'project', 'compute_shell_sessions_created_total', 'project',
  'Only the session counters carry a project label; the cell and agent panels ignore this filter.'
);
local locationVar = multiVar(
  'location', 'location', 'compute_shell_sessions_created_total', 'location',
  'Location the session was bound to. Narrows every session panel.'
);
local cellVar = multiVar(
  'cell', 'cell', 'compute_shell_agent_endpoint_reachable', 'cell',
  'Cell whose shell agents to show. Narrows the cell and agent panels only.'
);

local sessionUidVar =
  var.textbox.new('session_uid', '')
  + var.textbox.generalOptions.withLabel('session UID')
  + var.textbox.generalOptions.withDescription('Paste a session UID from a project activity event to filter the log panel to that session.');

// ---------------------------------------------------------------------------
// Header
// ---------------------------------------------------------------------------

local headerText =
  text.new('')
  + text.panelOptions.withGridPos(3, 24, 0, 0)
  + text.options.withMode('markdown')
  + text.options.withContent(|||
    **Tracing one session:** a project's `SessionStarted` / `SessionEnded` activity events carry the session UID and the instance name; the Instance's status names the cell that runs it. Search that cell's agent logs by UID with `kubectl logs -n compute-shell-system deploy/exec-agent | grep <session UID>`, or paste the UID into the *session UID* box above to filter the controller logs below.
  |||);

// ---------------------------------------------------------------------------
// Project and location
// ---------------------------------------------------------------------------

local sessionFilter = 'project=~"$project", location=~"$location"';
local locationFilter = 'location=~"$location"';

local statOpen = statPanel(
  'Sessions open',
  'Sessions in any non-terminal phase across the selected projects and locations.',
  qRange('sum(compute_shell_sessions_open{%s}) or vector(0)' % sessionFilter, 'open'),
  { h: 4, w: 6, x: 0, y: 4 },
  'short',
  [{ color: 'blue', value: null }],
);

local statCreated24h = statPanel(
  'Created (24h)',
  'Sessions created in the last 24 hours.',
  qRange('sum(increase(compute_shell_sessions_created_total{%s}[24h])) or vector(0)' % sessionFilter, 'created'),
  { h: 4, w: 6, x: 6, y: 4 },
  'short',
  [{ color: 'blue', value: null }],
);

local statConnectP95 = statPanel(
  'p95 time to first prompt (1h)',
  'Time from session creation to a client connected, 95th percentile over the last hour. The alert threshold is 5s.',
  qRange('histogram_quantile(0.95, sum by (le) (rate(compute_shell_session_connect_seconds_bucket{%s}[1h])))' % locationFilter, 'p95'),
  { h: 4, w: 6, x: 12, y: 4 },
  's',
  [{ color: 'green', value: null }, { color: 'orange', value: 3 }, { color: 'red', value: 5 }],
);

local statFailureRatio = statPanel(
  'Platform failure ratio (24h)',
  'Share of sessions in the last 24 hours that the platform ended before they could be used (%s), out of all sessions that ended.' % platformFailureReasons,
  qRange(|||
    (
      sum(increase(compute_shell_sessions_ended_total{%(loc)s, reason=~"%(reasons)s"}[24h])) or vector(0)
    )
    /
    clamp_min(sum(increase(compute_shell_sessions_ended_total{%(loc)s}[24h])), 1)
  ||| % { loc: locationFilter, reasons: platformFailureReasons }, 'failure ratio'),
  { h: 4, w: 6, x: 18, y: 4 },
  'percentunit',
  [{ color: 'green', value: null }, { color: 'orange', value: 0.01 }, { color: 'red', value: 0.05 }],
);

local tsCreatedByLocation = tsPanel(
  'Sessions created by location',
  'Rate of session creation per location.',
  [qRange('sum by (location) (rate(compute_shell_sessions_created_total{%s}[5m]))' % sessionFilter, '{{location}}')],
  { h: 8, w: 12, x: 0, y: 8 },
  'reqps',
);

local tsOpenByPhase = tsPanel(
  'Sessions open by phase',
  'Open sessions by lifecycle phase. Sessions piling up in pending point at cells not claiming; piling up in ready means clients are not connecting within their window.',
  [qRange('sum by (phase) (compute_shell_sessions_open{%s})' % sessionFilter, '{{phase}}')],
  { h: 8, w: 12, x: 12, y: 8 },
  'short',
  stacked=true,
);

local tsEndsByReason = tsPanel(
  'Sessions ended by reason',
  'Rate of session ends per terminal reason. Completed, Expired, Disconnected and Revoked are normal; Unavailable and AgentLost mean the platform failed the session.',
  [qRange('sum by (reason) (rate(compute_shell_sessions_ended_total{%s}[5m]))' % locationFilter, '{{reason}}')],
  { h: 8, w: 8, x: 0, y: 16 },
  'reqps',
  stacked=true,
  overrides=[
    {
      matcher: { id: 'byRegexp', options: '(%s)' % platformFailureReasons },
      properties: [{ id: 'color', value: { fixedColor: 'red', mode: 'fixed' } }],
    },
  ],
);

local tsConnectLatency = tsPanel(
  'Time to first prompt',
  'Session creation to client connected, by percentile across the selected locations.',
  [
    qRange('histogram_quantile(0.50, sum by (le) (rate(compute_shell_session_connect_seconds_bucket{%s}[5m])))' % locationFilter, 'p50'),
    qRange('histogram_quantile(0.95, sum by (le) (rate(compute_shell_session_connect_seconds_bucket{%s}[5m])))' % locationFilter, 'p95'),
    qRange('histogram_quantile(0.99, sum by (le) (rate(compute_shell_session_connect_seconds_bucket{%s}[5m])))' % locationFilter, 'p99'),
  ],
  { h: 8, w: 8, x: 8, y: 16 },
  's',
);

local tsDuration = tsPanel(
  'Session duration',
  'How long sessions lasted once connected, by percentile across the selected locations.',
  [
    qRange('histogram_quantile(0.50, sum by (le) (rate(compute_shell_session_duration_seconds_bucket{%s}[5m])))' % locationFilter, 'p50'),
    qRange('histogram_quantile(0.95, sum by (le) (rate(compute_shell_session_duration_seconds_bucket{%s}[5m])))' % locationFilter, 'p95'),
  ],
  { h: 8, w: 8, x: 16, y: 16 },
  's',
);

// ---------------------------------------------------------------------------
// Cell and agent
// ---------------------------------------------------------------------------

local cellFilter = 'cell=~"$cell"';

local stEndpointReachable =
  stateTimeline.new('Agent endpoint reachable')
  + stateTimeline.panelOptions.withDescription('Whether each shell agent can reach its cell tunnel endpoint. An agent that cannot reach the endpoint cannot serve sessions; a cell with no reachable agent fails every session bound to it with Unavailable.')
  + stateTimeline.panelOptions.withGridPos(7, 24, 0, 25)
  + stateTimeline.queryOptions.withDatasource('prometheus', ds)
  + stateTimeline.queryOptions.withTargets([
    qRange('max by (cell, agent) (compute_shell_agent_endpoint_reachable{%s})' % cellFilter, '{{cell}} / {{agent}}'),
  ])
  + stateTimeline.options.withMergeValues(true)
  + stateTimeline.options.withShowValue('never')
  + stateTimeline.options.withRowHeight(0.8)
  + stateTimeline.options.legend.withDisplayMode('list')
  + stateTimeline.options.legend.withPlacement('bottom')
  + stateTimeline.standardOptions.color.withMode('thresholds')
  + stateTimeline.standardOptions.thresholds.withMode('absolute')
  + stateTimeline.standardOptions.thresholds.withSteps([{ color: 'red', value: null }, { color: 'green', value: 1 }])
  + stateTimeline.standardOptions.withMappings([
    { type: 'value', options: { '0': { text: 'unreachable', color: 'red', index: 0 }, '1': { text: 'reachable', color: 'green', index: 1 } } },
  ]);

local tsAgentOpen = tsPanel(
  'Sessions open per agent',
  'Sessions each agent is currently serving. Agents run in pairs per cell, so a lopsided pair usually means one agent restarted or lost its endpoint.',
  [qRange('sum by (cell, agent) (compute_shell_agent_sessions_open{%s})' % cellFilter, '{{cell}} / {{agent}}')],
  { h: 8, w: 8, x: 0, y: 32 },
  'short',
);

local tsClaims = tsPanel(
  'Session claims by outcome',
  'Rate at which agents try to claim sessions, by outcome. A cell claiming nothing while sessions are created for its location is not seeing them.',
  [qRange('sum by (cell, outcome) (rate(compute_shell_agent_claims_total{%s}[5m]))' % cellFilter, '{{cell}} / {{outcome}}')],
  { h: 8, w: 8, x: 8, y: 32 },
  'reqps',
  stacked=true,
);

local tsCleanupUnconfirmed = tsPanel(
  'Cleanup unconfirmed',
  'Sessions whose cell did not confirm the command stopped within the cleanup timeout. Each one may have left a running process behind in an Instance.',
  [qRange('sum by (cell) (increase(compute_shell_agent_cleanup_unconfirmed_total{%s}[1h]))' % cellFilter, '{{cell}}')],
  { h: 8, w: 8, x: 16, y: 32 },
  'short',
  overrides=[
    { matcher: { id: 'byType', options: 'number' }, properties: [{ id: 'color', value: { fixedColor: 'orange', mode: 'fixed' } }] },
  ],
);

// ---------------------------------------------------------------------------
// Logs
// ---------------------------------------------------------------------------

local logsSession =
  logs.new('Session controller logs')
  + logs.panelOptions.withDescription('Management-plane session controller logs from the platform Loki, filtered by the session UID box. Cell agent logs are not shipped to this Loki; read them on the cell as described at the top.')
  + logs.panelOptions.withGridPos(10, 24, 0, 41)
  + logs.queryOptions.withDatasource('loki', logsDs.uid)
  + logs.queryOptions.withTargets([
    loki.new(logsDs.uid, '{namespace="compute-system"} |= "$session_uid" |~ "(?i)consolesession|shell"')
    + { datasource: logsDs },
  ])
  + logs.options.withShowTime(true)
  + logs.options.withWrapLogMessage(true)
  + logs.options.withEnableLogDetails(true)
  + logs.options.withSortOrder('Descending');

// ---------------------------------------------------------------------------
// Dashboard assembly
// ---------------------------------------------------------------------------

g.dashboard.new('Compute / Instance Shell Sessions')
+ g.dashboard.withUid('compute-shell-sessions')
+ g.dashboard.withDescription('Instance shell sessions from the project view (created, open, ended, time to first prompt) and the cell view (agent reachability, load, claims, cleanup).')
+ g.dashboard.withTags(['compute', 'shell-sessions'])
+ g.dashboard.withEditable(true)
+ g.dashboard.withRefresh('1m')
+ g.dashboard.withSchemaVersion(39)
+ g.dashboard.time.withFrom('now-6h')
+ g.dashboard.time.withTo('now')
+ g.dashboard.withVariables([
  datasourceVar,
  projectVar,
  locationVar,
  cellVar,
  sessionUidVar,
])
+ g.dashboard.withPanels(
  panels=[
    headerText,

    rowPanel('Project and location', 100, 3),
    statOpen,
    statCreated24h,
    statConnectP95,
    statFailureRatio,
    tsCreatedByLocation,
    tsOpenByPhase,
    tsEndsByReason,
    tsConnectLatency,
    tsDuration,

    rowPanel('Cell and agent', 200, 24),
    stEndpointReachable,
    tsAgentOpen,
    tsClaims,
    tsCleanupUnconfirmed,

    rowPanel('Logs', 300, 40),
    logsSession,
  ],
  setPanelIDs=true,
)
