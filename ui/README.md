# UI

Frontend code for the compute service, split by which side of the
[Portal Plugin System](https://github.com/datum-cloud/cloud-portal/blob/main/docs/enhancements/portal-plugin-system.md)
it belongs to:

- **[`consumer/`](./consumer)** — portal plugins: standalone Module Federation
  remotes that the cloud portal discovers and loads at runtime to render
  compute's UI inside the portal shell. See
  [`consumer/workloads`](./consumer/workloads) for the reference plugin.
- **`provider/`** — reserved for compute-authored UI building blocks (e.g.
  shared components or data hooks) meant to be consumed by other services'
  plugins or by cloud-portal itself, rather than rendered directly. Empty for
  now; add code here once there's a concrete cross-service UI dependency.
