// SPDX-License-Identifier: AGPL-3.0-only

package deploy

import (
	"fmt"
	"maps"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apimachineryvalidation "k8s.io/apimachinery/pkg/api/validation"
	utilvalidation "k8s.io/apimachinery/pkg/util/validation"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

// sourceKind distinguishes the two things a flag can reference. It exists
// because the two are not interchangeable on the wire: a configMap volume
// names its source with `name` and a secret volume with `secretName`, and an
// envFrom entry sets either configMapRef or secretRef but never both.
type sourceKind string

const (
	sourceConfigMap sourceKind = "configMap"
	sourceSecret    sourceKind = "secret"
)

// flagName returns the flag that produces this kind of reference, so an error
// can name the flag the user actually typed.
func (k sourceKind) flagName(prefix string) string {
	if k == sourceSecret {
		return prefix + "secret"
	}
	return prefix + "configmap"
}

// removalSuffix marks an entry for removal instead of assignment. It follows
// 'kubectl set env', where KEY- deletes KEY, and is spelled the same way for
// every flag here so one convention covers env, sources, mounts, and labels.
const removalSuffix = "-"

// envEdit is one --env token.
type envEdit struct {
	name   string
	value  string
	remove bool
}

// envFromEdit is one --env-from-configmap or --env-from-secret token. A
// removal drops every entry referencing the name, whatever prefix it carries,
// because the user names the source and not the entry.
type envFromEdit struct {
	kind   sourceKind
	name   string
	prefix string
	remove bool
}

// mountEdit is one --configmap or --secret token.
type mountEdit struct {
	kind      sourceKind
	name      string
	mountPath string
	remove    bool
}

// labelEdit is one --label token.
type labelEdit struct {
	key    string
	value  string
	remove bool
}

// configEdits is everything the env, source, mount, and label flags asked for
// in a single invocation, after syntax and self-consistency checks. It is a
// list of edits rather than a finished spec because the flags describe changes
// to whatever the workload already carries, not a replacement for it.
type configEdits struct {
	env     []envEdit
	envFrom []envFromEdit
	mounts  []mountEdit
	labels  []labelEdit
}

// containerConfig is the env, mounts, and labels a flag-driven deploy writes,
// after the flags have been merged over what the workload already has.
//
// Volumes and attachments travel together: validation rejects a volume that no
// container attaches, so the two lists are built in one place and stay the
// same length and order.
type containerConfig struct {
	env               []corev1.EnvVar
	envFrom           []computev1alpha.EnvFromSource
	volumes           []computev1alpha.InstanceVolume
	attachments       []computev1alpha.VolumeAttachment
	workloadLabels    map[string]string
	templateLabels    map[string]string
	mountedSourceKind map[string]sourceKind
}

// parseConfigEdits turns the raw flag values into edits, rejecting malformed
// input and input that contradicts itself within one invocation.
//
// It runs before the client is built and before the Apply prompt: a typo in a
// mount path costs a message, not a half-applied workload or an answered
// prompt that is then thrown away.
func parseConfigEdits(opts *options) (*configEdits, error) {
	edits := &configEdits{}

	if err := parseEnv(opts.env, edits); err != nil {
		return nil, err
	}
	if err := parseEnvFrom(opts.envFromConfigMap, sourceConfigMap, edits); err != nil {
		return nil, err
	}
	if err := parseEnvFrom(opts.envFromSecret, sourceSecret, edits); err != nil {
		return nil, err
	}
	if err := parseMounts(opts.configMaps, sourceConfigMap, edits); err != nil {
		return nil, err
	}
	if err := parseMounts(opts.secrets, sourceSecret, edits); err != nil {
		return nil, err
	}
	if err := parseLabels(opts.labels, edits); err != nil {
		return nil, err
	}
	return edits, nil
}

func parseEnv(values []string, edits *configEdits) error {
	seen := map[string]bool{}
	for _, raw := range values {
		edit := envEdit{}
		if name, ok := strings.CutSuffix(raw, removalSuffix); ok && !strings.Contains(raw, "=") {
			edit = envEdit{name: name, remove: true}
		} else {
			name, value, found := strings.Cut(raw, "=")
			if !found {
				return fmt.Errorf("invalid --env %q: expected KEY=VALUE to set a variable, or KEY- to remove one", raw)
			}
			edit = envEdit{name: name, value: value}
		}

		if edit.name == "" {
			return fmt.Errorf("invalid --env %q: the variable name is empty, expected KEY=VALUE or KEY-", raw)
		}
		if msgs := utilvalidation.IsCIdentifier(edit.name); len(msgs) > 0 {
			return fmt.Errorf("invalid --env %q: %s must be a valid environment variable name (%s)",
				raw, edit.name, strings.Join(msgs, "; "))
		}
		// Two tokens naming one variable have no defined outcome — setting and
		// removing it in the same breath least of all — so the ambiguity is
		// reported rather than resolved by flag order.
		if seen[edit.name] {
			return fmt.Errorf("--env %s is given more than once: name each variable once per deploy", edit.name)
		}
		seen[edit.name] = true
		edits.env = append(edits.env, edit)
	}
	return nil
}

func parseEnvFrom(values []string, kind sourceKind, edits *configEdits) error {
	flag := kind.flagName("--env-from-")
	type entry struct{ name, prefix string }
	seen := map[entry]bool{}
	removed := map[string]bool{}

	for _, raw := range values {
		if name, ok := strings.CutSuffix(raw, removalSuffix); ok && !strings.Contains(raw, ":") {
			if name == "" {
				return fmt.Errorf("invalid %s %q: the name is empty, expected NAME- to remove a source", flag, raw)
			}
			if err := validateSourceName(flag, raw, name); err != nil {
				return err
			}
			if removed[name] {
				return fmt.Errorf("%s %s- is given more than once", flag, name)
			}
			removed[name] = true
			edits.envFrom = append(edits.envFrom, envFromEdit{kind: kind, name: name, remove: true})
			continue
		}

		name, prefix, _ := strings.Cut(raw, ":")
		if name == "" {
			return fmt.Errorf("invalid %s %q: the name is empty, expected NAME or NAME:PREFIX_", flag, raw)
		}
		if err := validateSourceName(flag, raw, name); err != nil {
			return err
		}
		// The prefix is prepended to every key the source contributes, so the
		// result has to be a legal variable name before the prefix is even
		// joined to a key.
		if prefix != "" {
			if msgs := utilvalidation.IsCIdentifier(prefix); len(msgs) > 0 {
				return fmt.Errorf("invalid %s %q: the prefix %q must be a valid environment variable name prefix (%s), for example %s:APP_",
					flag, raw, prefix, strings.Join(msgs, "; "), name)
			}
		}
		// A source may legitimately appear twice under different prefixes, so
		// only an exact repeat is a mistake.
		if seen[entry{name, prefix}] {
			return fmt.Errorf("%s %s is given more than once", flag, raw)
		}
		seen[entry{name, prefix}] = true
		edits.envFrom = append(edits.envFrom, envFromEdit{kind: kind, name: name, prefix: prefix})
	}
	return nil
}

func parseMounts(values []string, kind sourceKind, edits *configEdits) error {
	flag := kind.flagName("--")
	seen := map[string]bool{}
	paths := map[string]string{}

	for _, raw := range values {
		if name, ok := strings.CutSuffix(raw, removalSuffix); ok && !strings.Contains(raw, ":") {
			if name == "" {
				return fmt.Errorf("invalid %s %q: the name is empty, expected NAME- to remove a mount", flag, raw)
			}
			if err := validateSourceName(flag, raw, name); err != nil {
				return err
			}
			if seen[name] {
				return fmt.Errorf("%s names %s more than once", flag, name)
			}
			seen[name] = true
			edits.mounts = append(edits.mounts, mountEdit{kind: kind, name: name, remove: true})
			continue
		}

		name, mountPath, found := strings.Cut(raw, ":")
		if !found {
			return fmt.Errorf("invalid %s %q: expected NAME:/mount/path to mount, or NAME- to remove a mount", flag, raw)
		}
		if name == "" {
			return fmt.Errorf("invalid %s %q: the name is empty, expected NAME:/mount/path", flag, raw)
		}
		if err := validateSourceName(flag, raw, name); err != nil {
			return err
		}
		if mountPath == "" {
			return fmt.Errorf("invalid %s %q: the mount path is empty, expected %s:/mount/path", flag, raw, name)
		}
		if !strings.HasPrefix(mountPath, "/") {
			return fmt.Errorf("invalid %s %q: the mount path %q must be absolute, for example %s:/etc/%s", flag, raw, mountPath, name, name)
		}
		if seen[name] {
			return fmt.Errorf("%s names %s more than once: a source is mounted at one path per deploy", flag, name)
		}
		seen[name] = true
		if other, ok := paths[mountPath]; ok {
			return fmt.Errorf("%s mounts both %s and %s at %s: each mount needs its own path", flag, other, name, mountPath)
		}
		paths[mountPath] = name
		edits.mounts = append(edits.mounts, mountEdit{kind: kind, name: name, mountPath: mountPath})
	}
	return nil
}

func parseLabels(values []string, edits *configEdits) error {
	seen := map[string]bool{}
	for _, raw := range values {
		edit := labelEdit{}
		if key, ok := strings.CutSuffix(raw, removalSuffix); ok && !strings.Contains(raw, "=") {
			edit = labelEdit{key: key, remove: true}
		} else {
			key, value, found := strings.Cut(raw, "=")
			if !found {
				return fmt.Errorf("invalid --label %q: expected key=value to set a label, or key- to remove one", raw)
			}
			edit = labelEdit{key: key, value: value}
		}

		if edit.key == "" {
			return fmt.Errorf("invalid --label %q: the label key is empty, expected key=value or key-", raw)
		}
		if msgs := utilvalidation.IsQualifiedName(edit.key); len(msgs) > 0 {
			return fmt.Errorf("invalid --label %q: the key %q is not valid (%s)", raw, edit.key, strings.Join(msgs, "; "))
		}
		if msgs := utilvalidation.IsValidLabelValue(edit.value); len(msgs) > 0 {
			return fmt.Errorf("invalid --label %q: the value %q is not valid (%s)", raw, edit.value, strings.Join(msgs, "; "))
		}
		if seen[edit.key] {
			return fmt.Errorf("--label %s is given more than once: name each label once per deploy", edit.key)
		}
		seen[edit.key] = true
		edits.labels = append(edits.labels, edit)
	}
	return nil
}

// validateSourceName holds every ConfigMap and Secret reference to a DNS
// label. That is stricter than a Kubernetes object name, but it is what the
// server requires of an envFrom source, and a mount's generated volume takes
// its name from the same string.
func validateSourceName(flag, raw, name string) error {
	msgs := apimachineryvalidation.NameIsDNSLabel(name, false)
	if len(msgs) == 0 {
		return nil
	}
	return fmt.Errorf("invalid %s %q: the name %q is not valid (%s)", flag, raw, name, strings.Join(msgs, "; "))
}

// resolveContainerConfig merges this deploy's edits over what the workload
// already carries.
//
// Carrying forward is the whole point. The flag path rewrites the workload
// spec wholesale, so anything not restated here is dropped: without this, a
// routine 'deploy api --image=…:v2' would silently strip the workload's
// environment, unmount its config, and drop the labels other tooling selects
// on. That is the same reasoning that carries --http-port forward, and the
// consequences are just as invisible until something stops working. Removal is
// only ever explicit, through the KEY- form.
//
// Removing something the workload does not have is not an error: a deploy
// script that clears a variable must be runnable twice.
func resolveContainerConfig(existing *computev1alpha.Workload, creating bool, edits *configEdits) (*containerConfig, error) {
	cfg := &containerConfig{
		workloadLabels:    map[string]string{},
		templateLabels:    map[string]string{},
		mountedSourceKind: map[string]sourceKind{},
	}

	if !creating {
		container := firstSandboxContainer(existing)
		if container != nil {
			cfg.env = append(cfg.env, container.Env...)
			cfg.envFrom = append(cfg.envFrom, container.EnvFrom...)
			// Only volumes this container attaches survive. A flag deploy
			// writes exactly one container, so a volume attached by some other
			// container would come back unattached, which validation rejects.
			cfg.volumes, cfg.attachments = pairedVolumes(existing.Spec.Template.Spec.Volumes, container.VolumeAttachments)
		}
		// Workload and template labels are kept apart rather than unioned. The
		// template's labels feed the instance template hash, so folding
		// server-applied workload labels into them would roll every instance
		// for a label nobody set here.
		cfg.workloadLabels = copyStringMap(existing.Labels)
		cfg.templateLabels = copyStringMap(existing.Spec.Template.Labels)
	}

	for _, v := range cfg.volumes {
		if kind := volumeSourceKind(v); kind != "" {
			cfg.mountedSourceKind[v.Name] = kind
		}
	}

	applyEnvEdits(cfg, edits.env)
	applyEnvFromEdits(cfg, edits.envFrom)
	if err := applyMountEdits(cfg, edits.mounts); err != nil {
		return nil, err
	}
	applyLabelEdits(cfg, edits.labels)

	return cfg, nil
}

func applyEnvEdits(cfg *containerConfig, edits []envEdit) {
	for _, e := range edits {
		if e.remove {
			cfg.env = removeFunc(cfg.env, func(v corev1.EnvVar) bool { return v.Name == e.name })
			continue
		}
		// A set replaces in place so that the order a user first declared
		// their variables in survives every later redeploy.
		replaced := false
		for i := range cfg.env {
			if cfg.env[i].Name == e.name {
				cfg.env[i] = corev1.EnvVar{Name: e.name, Value: e.value}
				replaced = true
				break
			}
		}
		if !replaced {
			cfg.env = append(cfg.env, corev1.EnvVar{Name: e.name, Value: e.value})
		}
	}
}

func applyEnvFromEdits(cfg *containerConfig, edits []envFromEdit) {
	for _, e := range edits {
		if e.remove {
			cfg.envFrom = removeFunc(cfg.envFrom, func(s computev1alpha.EnvFromSource) bool {
				return envFromName(s) == e.name && envFromKind(s) == e.kind
			})
			continue
		}
		source := computev1alpha.EnvFromSource{Prefix: e.prefix}
		if e.kind == sourceSecret {
			source.SecretRef = &computev1alpha.SecretEnvSource{Name: e.name}
		} else {
			source.ConfigMapRef = &computev1alpha.ConfigMapEnvSource{Name: e.name}
		}
		// Prefixes distinguish entries, so a repeat of the same source under
		// the same prefix is the one that is already there.
		exists := false
		for _, s := range cfg.envFrom {
			if envFromKind(s) == e.kind && envFromName(s) == e.name && s.Prefix == e.prefix {
				exists = true
				break
			}
		}
		if !exists {
			cfg.envFrom = append(cfg.envFrom, source)
		}
	}
}

// applyMountEdits writes each mount as a volume and the attachment that
// carries it into the container, always as a pair: a volume nothing attaches
// is rejected by validation, and an attachment naming no volume is rejected
// too.
//
// The volume's name is the name of the ConfigMap or Secret it projects. A
// ConfigMap and a Secret of the same name would therefore claim one volume
// name, which only a manifest can settle, so that case is refused here.
func applyMountEdits(cfg *containerConfig, edits []mountEdit) error {
	for _, m := range edits {
		if m.remove {
			cfg.volumes = removeFunc(cfg.volumes, func(v computev1alpha.InstanceVolume) bool { return v.Name == m.name })
			cfg.attachments = removeFunc(cfg.attachments, func(a computev1alpha.VolumeAttachment) bool { return a.Name == m.name })
			delete(cfg.mountedSourceKind, m.name)
			continue
		}

		if kind, ok := cfg.mountedSourceKind[m.name]; ok && kind != m.kind {
			return fmt.Errorf(
				"cannot mount %s %q: the workload already mounts a %s of that name, and both would generate a volume named %q. "+
					"Name the volumes explicitly with a manifest: datumctl compute deploy -f workload.yaml",
				m.kind, m.name, kind, m.name)
		}

		volume := computev1alpha.InstanceVolume{Name: m.name}
		if m.kind == sourceSecret {
			// A secret volume names its source with `secretName`, while a
			// configMap volume names it with `name`. The two fields are not
			// interchangeable.
			volume.Secret = &corev1.SecretVolumeSource{SecretName: m.name}
		} else {
			volume.ConfigMap = &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: m.name},
			}
		}
		mountPath := m.mountPath
		attachment := computev1alpha.VolumeAttachment{Name: m.name, MountPath: &mountPath}

		if i := indexFunc(cfg.volumes, func(v computev1alpha.InstanceVolume) bool { return v.Name == m.name }); i >= 0 {
			cfg.volumes[i] = volume
		} else {
			cfg.volumes = append(cfg.volumes, volume)
		}
		if i := indexFunc(cfg.attachments, func(a computev1alpha.VolumeAttachment) bool { return a.Name == m.name }); i >= 0 {
			cfg.attachments[i] = attachment
		} else {
			cfg.attachments = append(cfg.attachments, attachment)
		}
		cfg.mountedSourceKind[m.name] = m.kind
	}

	// Checked against the merged result, not just this deploy's flags: a new
	// mount can collide with a path the workload already uses, and the guest
	// would then see only one of the two.
	paths := map[string]string{}
	for _, a := range cfg.attachments {
		if a.MountPath == nil {
			continue
		}
		if other, ok := paths[*a.MountPath]; ok {
			return fmt.Errorf(
				"%q and %q are both mounted at %s: pick a different path, or unmount one with --configmap %s- or --secret %s-",
				other, a.Name, *a.MountPath, other, other)
		}
		paths[*a.MountPath] = a.Name
	}
	return nil
}

func applyLabelEdits(cfg *containerConfig, edits []labelEdit) {
	for _, e := range edits {
		if e.remove {
			delete(cfg.workloadLabels, e.key)
			delete(cfg.templateLabels, e.key)
			continue
		}
		cfg.workloadLabels[e.key] = e.value
		cfg.templateLabels[e.key] = e.value
	}
}

// firstSandboxContainer returns the container a flag-driven deploy owns, or
// nil for a workload that has no sandbox — a virtual machine, or a workload
// that has never been applied.
func firstSandboxContainer(w *computev1alpha.Workload) *computev1alpha.SandboxContainer {
	sandbox := w.Spec.Template.Spec.Runtime.Sandbox
	if sandbox == nil || len(sandbox.Containers) == 0 {
		return nil
	}
	return &sandbox.Containers[0]
}

// pairedVolumes returns the volumes the container attaches, together with
// those attachments, dropping either half of a pair whose other half is
// missing.
func pairedVolumes(volumes []computev1alpha.InstanceVolume, attachments []computev1alpha.VolumeAttachment) ([]computev1alpha.InstanceVolume, []computev1alpha.VolumeAttachment) {
	var keptVolumes []computev1alpha.InstanceVolume
	var keptAttachments []computev1alpha.VolumeAttachment
	for _, a := range attachments {
		i := indexFunc(volumes, func(v computev1alpha.InstanceVolume) bool { return v.Name == a.Name })
		if i < 0 {
			continue
		}
		keptVolumes = append(keptVolumes, volumes[i])
		keptAttachments = append(keptAttachments, a)
	}
	return keptVolumes, keptAttachments
}

// volumeSourceKind reports which of the two flag-managed sources a volume
// projects, or empty for a volume this command does not manage, such as a
// disk.
func volumeSourceKind(v computev1alpha.InstanceVolume) sourceKind {
	switch {
	case v.ConfigMap != nil:
		return sourceConfigMap
	case v.Secret != nil:
		return sourceSecret
	}
	return ""
}

func envFromKind(s computev1alpha.EnvFromSource) sourceKind {
	if s.SecretRef != nil {
		return sourceSecret
	}
	if s.ConfigMapRef != nil {
		return sourceConfigMap
	}
	return ""
}

func envFromName(s computev1alpha.EnvFromSource) string {
	switch {
	case s.SecretRef != nil:
		return s.SecretRef.Name
	case s.ConfigMapRef != nil:
		return s.ConfigMapRef.Name
	}
	return ""
}

func indexFunc[T any](items []T, match func(T) bool) int {
	for i := range items {
		if match(items[i]) {
			return i
		}
	}
	return -1
}

func removeFunc[T any](items []T, match func(T) bool) []T {
	kept := items[:0]
	for _, item := range items {
		if !match(item) {
			kept = append(kept, item)
		}
	}
	if len(kept) == 0 {
		return nil
	}
	return kept
}

// nonEmptyLabels returns nil for a label set with nothing in it, so a workload
// that has never carried labels is not stamped with an empty map that shows up
// in workload.yaml and in every diff after it.
func nonEmptyLabels(labels map[string]string) map[string]string {
	if len(labels) == 0 {
		return nil
	}
	return labels
}

func copyStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	maps.Copy(out, in)
	return out
}

// planConfigLines renders the env, mount, and label lines of the plan summary.
//
// It describes the workload as it will be after the apply, carried-forward
// entries included, not only what this invocation's flags changed. A plan that
// showed only the changes would let a user approve an apply without seeing the
// configuration it is about to write.
func planConfigLines(cfg *containerConfig) []string {
	var lines []string

	if len(cfg.env) > 0 {
		names := make([]string, 0, len(cfg.env))
		for _, e := range cfg.env {
			names = append(names, e.Name)
		}
		lines = append(lines, planLine("Environment", fmt.Sprintf("%s (%s)", countOf(len(names), "variable"), strings.Join(names, ", "))))
	}

	if len(cfg.envFrom) > 0 {
		refs := make([]string, 0, len(cfg.envFrom))
		for _, s := range cfg.envFrom {
			ref := fmt.Sprintf("%s/%s", envFromKind(s), envFromName(s))
			if s.Prefix != "" {
				ref += " as " + s.Prefix + "*"
			}
			refs = append(refs, ref)
		}
		lines = append(lines, planLine("Environment from", strings.Join(refs, ", ")))
	}

	if len(cfg.attachments) > 0 {
		mounts := make([]string, 0, len(cfg.attachments))
		for _, a := range cfg.attachments {
			path := ""
			if a.MountPath != nil {
				path = *a.MountPath
			}
			kind := cfg.mountedSourceKind[a.Name]
			if kind == "" {
				kind = "volume"
			}
			mounts = append(mounts, fmt.Sprintf("%s/%s → %s", kind, a.Name, path))
		}
		lines = append(lines, planLine("Mounts", strings.Join(mounts, ", ")))
	}

	if len(cfg.templateLabels) > 0 {
		keys := make([]string, 0, len(cfg.templateLabels))
		for k := range cfg.templateLabels {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		pairs := make([]string, 0, len(keys))
		for _, k := range keys {
			pairs = append(pairs, fmt.Sprintf("%s=%s", k, cfg.templateLabels[k]))
		}
		lines = append(lines, planLine("Labels", strings.Join(pairs, ", ")))
	}

	return lines
}

// countOf renders a count with its noun pluralized, so the plan reads as a
// sentence rather than a field dump.
func countOf(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
