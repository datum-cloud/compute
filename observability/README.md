# Observability sources

Grafana dashboards for the compute service are written in
[grafonnet](https://grafana.github.io/grafonnet/) and rendered to JSON. The
committed JSON under `config/components/dashboards/` is a build artifact that
ships in the compute OCI kustomize bundle; edit the source here and rebuild.
Never edit the rendered JSON by hand: CI re-renders it and fails on any
difference.

Alert rules are plain `VMRule` manifests under `config/components/alerts/`,
with a runbook per alert under `docs/runbooks/`.

## Layout

```
observability/dashboards/<name>/
├── config.jsonnet          # the dashboard
├── jsonnetfile.json        # grafonnet dependency
├── jsonnetfile.lock.json   # pinned to the same grafonnet commit the infra repo uses
└── Taskfile.yaml           # install (jb) + build (render into config/components/dashboards/)
```

`vendor/` directories are gitignored and re-fetched by `install`.

## Build

```sh
task install-jsonnet-tools   # jsonnet + jb, once per machine
task dashboards:install      # fetch grafonnet into vendor/
task dashboards:build        # render config/components/dashboards/*.json
hack/validate-dashboard-drift
```

## Add a dashboard

1. Copy an existing `observability/dashboards/<name>/` directory and edit
   `config.jsonnet`. Keep the `$datasource` variable and the
   `Platform / Compute` folder so the dashboard sits with the others.
2. Point the new Taskfile's `OUT` at `config/components/dashboards/<name>.json`
   and add an `includes` entry plus a line in `dashboards:build` and
   `dashboards:install` in the root `Taskfile.yaml`.
3. Add a `GrafanaDashboard` and a `configMapGenerator` entry in
   `config/components/dashboards/`, and the JSON path to `GENERATED_PATHS` in
   `hack/validate-dashboard-drift`.
