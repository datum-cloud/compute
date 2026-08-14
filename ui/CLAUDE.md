# ui/consumer and ui/provider — portal plugin conventions

Both plugins here (`ui/consumer` for cloud-portal, `ui/provider` for
staff-portal) are Module Federation remotes rendered directly into a host
portal's DOM at runtime. Two things follow from that which aren't obvious
from reading either plugin's own source in isolation:

## Prefer datum-ui components over hand-rolled ones

Before writing a custom component (tabs, tables, empty states, code/YAML
viewers, stat rows, etc.), check `datum-ui/packages/datum-ui/src/components/`
(base/ and features/) for an existing one — `tabs`, `table`,
`empty-content` (the real "coming soon"/empty-state component), and
`code-editor` (read-only YAML/JSON display) all already exist there and
should be used instead of reimplementing the same thing with plain divs.

Only import `@datum-cloud/datum-ui/<subpath>` names that the target host's
`federation-host.ts` actually lists in its shared config — cloud-portal and
staff-portal each curate their own subset, and it differs between them.
Anything outside that list still works, it just bundles its own copy in the
plugin's remote instead of sharing the host's singleton instance.

## Neither plugin has a Tailwind build of its own

There's no `tailwind.config`/`postcss.config`/`tailwindcss` package in either
`ui/consumer` or `ui/provider`, and no CSS file of their own — a plugin's
JSX renders into the *host's* DOM, so a Tailwind class only takes visual
effect if that exact class string already happens to exist in the host's own
compiled CSS. Common/simple utilities (`flex`, `grid`, `gap-3`, `grid-cols-2`,
etc.) usually coincidentally exist there because most real apps use them
somewhere. **Arbitrary-value classes almost never do** —
`grid-cols-[minmax(0,1fr)_90px]`, `min-w-[200px]`, `tracking-[0.03em]`,
`max-h-[600px]`, etc. — and they fail *silently*: no error, the class is just
absent from the host's stylesheet, so the layout collapses or the style is
simply missing.

**Use datum-ui components first** (their own classes ship in datum-ui's
compiled CSS, so they're guaranteed to work). If a genuinely custom layout is
unavoidable, use an inline `style={{...}}` for anything beyond a handful of
extremely common utility classes — don't reach for a bracketed arbitrary
value.
