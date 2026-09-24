// SPDX-License-Identifier: AGPL-3.0-only

package deploy

import (
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/cmd/compute/util"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
)

// configuredWorkload is a workload as the control plane returns it after a
// deploy that set environment variables, imported a ConfigMap and a Secret,
// mounted both, and labelled the workload: defaulted interface, recorded
// runtime class, resource version, and the paired volumes and attachments an
// applied spec always carries.
func configuredWorkload() *computev1alpha.Workload {
	appPath := "/etc/app"
	tlsPath := "/etc/tls"

	return &computev1alpha.Workload{
		ObjectMeta: metav1.ObjectMeta{
			Name:            testWorkload,
			Namespace:       util.ResourceNamespace,
			UID:             types.UID("11111111-2222-3333-4444-555555555555"),
			ResourceVersion: "4218",
			Labels:          map[string]string{"team": "platform", "tier": "api"},
		},
		Spec: computev1alpha.WorkloadSpec{
			Template: computev1alpha.InstanceTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"team": "platform", "tier": "api"},
				},
				Spec: computev1alpha.InstanceSpec{
					Runtime: computev1alpha.InstanceRuntimeSpec{
						Resources: computev1alpha.InstanceRuntimeResources{InstanceType: "datumcloud/d1-standard-2"},
						Class:     classGeneralPurpose,
						Sandbox: &computev1alpha.SandboxRuntime{
							Containers: []computev1alpha.SandboxContainer{{
								Name:  "app",
								Image: testImage,
								Env: []corev1.EnvVar{
									{Name: "LOG_LEVEL", Value: "info"},
									{Name: "ALLOWED_ORIGINS", Value: "a.example,b.example"},
								},
								EnvFrom: []computev1alpha.EnvFromSource{
									{ConfigMapRef: &computev1alpha.ConfigMapEnvSource{Name: "app-config"}},
									{Prefix: "SECRET_", SecretRef: &computev1alpha.SecretEnvSource{Name: "api-keys"}},
								},
								VolumeAttachments: []computev1alpha.VolumeAttachment{
									{Name: "app-files", MountPath: &appPath},
									{Name: "tls-cert", MountPath: &tlsPath},
								},
								Ports: []computev1alpha.NamedPort{{Name: httpPortName, Port: 8080}},
							}},
						},
					},
					NetworkInterfaces: []computev1alpha.InstanceNetworkInterface{{
						Name:          "eth0",
						Network:       networkingv1alpha.NetworkRef{Name: defaultNetworkName},
						IPFamilies:    []networkingv1alpha.IPFamily{networkingv1alpha.IPv6Protocol},
						ReclaimPolicy: "Delete",
					}},
					Volumes: []computev1alpha.InstanceVolume{
						{
							Name: "app-files",
							VolumeSource: computev1alpha.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{Name: "app-files"},
								},
							},
						},
						{
							Name: "tls-cert",
							VolumeSource: computev1alpha.VolumeSource{
								Secret: &corev1.SecretVolumeSource{SecretName: "tls-cert"},
							},
						},
					},
				},
			},
			Placements: []computev1alpha.WorkloadPlacement{{
				Name:          "default",
				ScaleSettings: computev1alpha.HorizontalScaleSettings{MinReplicas: 1},
			}},
		},
	}
}

// editsFrom parses the flags the way the command does, so the tests exercise
// the same path a user's command line takes.
func editsFrom(t *testing.T, args ...string) *configEdits {
	t.Helper()
	cmd, opts := command()
	if err := cmd.Flags().Parse(append([]string{testWorkload, imageFlag}, args...)); err != nil {
		t.Fatalf("parsing flags: %v", err)
	}
	edits, err := parseConfigEdits(opts)
	if err != nil {
		t.Fatalf("parsing config flags %v: %v", args, err)
	}
	return edits
}

// configFrom resolves the flags against a workload, failing the test on an
// error the case did not expect.
func configFrom(t *testing.T, existing *computev1alpha.Workload, creating bool, args ...string) *containerConfig {
	t.Helper()
	cfg, err := resolveContainerConfig(existing, creating, editsFrom(t, args...))
	if err != nil {
		t.Fatalf("resolving %v: %v", args, err)
	}
	return cfg
}

// TestParseConfigEditsSyntax covers the set and remove form of every flag,
// including the values a StringSlice would have torn in half at a comma.
func TestParseConfigEditsSyntax(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want configEdits
	}{
		{
			name: "env set",
			args: []string{"--env=LOG_LEVEL=debug"},
			want: configEdits{env: []envEdit{{name: "LOG_LEVEL", value: "debug"}}},
		},
		{
			name: "env value keeps its commas",
			args: []string{"--env=ALLOWED_ORIGINS=a.example,b.example"},
			want: configEdits{env: []envEdit{{name: "ALLOWED_ORIGINS", value: "a.example,b.example"}}},
		},
		{
			name: "env value may be empty",
			args: []string{"--env=LOG_LEVEL="},
			want: configEdits{env: []envEdit{{name: "LOG_LEVEL"}}},
		},
		{
			name: "env value may contain an equals sign",
			args: []string{"--env=DSN=postgres://h/db?a=b"},
			want: configEdits{env: []envEdit{{name: "DSN", value: "postgres://h/db?a=b"}}},
		},
		{
			name: "env value may end in a dash",
			args: []string{"--env=FLAGS=--verbose"},
			want: configEdits{env: []envEdit{{name: "FLAGS", value: "--verbose"}}},
		},
		{
			name: "env remove",
			args: []string{"--env=LOG_LEVEL-"},
			want: configEdits{env: []envEdit{{name: "LOG_LEVEL", remove: true}}},
		},
		{
			name: "env repeats",
			args: []string{"--env=A=1", "--env=B=2"},
			want: configEdits{env: []envEdit{{name: "A", value: "1"}, {name: "B", value: "2"}}},
		},
		{
			name: "env from configmap",
			args: []string{"--env-from-configmap=app-config"},
			want: configEdits{envFrom: []envFromEdit{{kind: sourceConfigMap, name: "app-config"}}},
		},
		{
			name: "env from secret with a prefix",
			args: []string{"--env-from-secret=api-keys:SECRET_"},
			want: configEdits{envFrom: []envFromEdit{{kind: sourceSecret, name: "api-keys", prefix: "SECRET_"}}},
		},
		{
			name: "env from the same configmap under two prefixes",
			args: []string{"--env-from-configmap=app:A_", "--env-from-configmap=app:B_"},
			want: configEdits{envFrom: []envFromEdit{
				{kind: sourceConfigMap, name: "app", prefix: "A_"},
				{kind: sourceConfigMap, name: "app", prefix: "B_"},
			}},
		},
		{
			name: "env from remove",
			args: []string{"--env-from-secret=api-keys-"},
			want: configEdits{envFrom: []envFromEdit{{kind: sourceSecret, name: "api-keys", remove: true}}},
		},
		{
			name: "configmap mount",
			args: []string{"--configmap=app-files:/etc/app"},
			want: configEdits{mounts: []mountEdit{{kind: sourceConfigMap, name: "app-files", mountPath: "/etc/app"}}},
		},
		{
			name: "secret mount",
			args: []string{"--secret=tls-cert:/etc/tls"},
			want: configEdits{mounts: []mountEdit{{kind: sourceSecret, name: "tls-cert", mountPath: "/etc/tls"}}},
		},
		{
			name: "mount path keeps its commas",
			args: []string{"--configmap=app-files:/etc/a,b"},
			want: configEdits{mounts: []mountEdit{{kind: sourceConfigMap, name: "app-files", mountPath: "/etc/a,b"}}},
		},
		{
			name: "mount remove",
			args: []string{"--configmap=app-files-"},
			want: configEdits{mounts: []mountEdit{{kind: sourceConfigMap, name: "app-files", remove: true}}},
		},
		{
			name: "label set",
			args: []string{"--label=team=platform"},
			want: configEdits{labels: []labelEdit{{key: "team", value: "platform"}}},
		},
		{
			name: "qualified label key",
			args: []string{"--label=example.com/team=platform"},
			want: configEdits{labels: []labelEdit{{key: "example.com/team", value: "platform"}}},
		},
		{
			name: "label remove",
			args: []string{"--label=team-"},
			want: configEdits{labels: []labelEdit{{key: "team", remove: true}}},
		},
		{
			name: "no config flags is no edits",
			args: nil,
			want: configEdits{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := editsFrom(t, tc.args...)
			if !reflect.DeepEqual(*got, tc.want) {
				t.Fatalf("parsed %v as %+v, want %+v", tc.args, *got, tc.want)
			}
		})
	}
}

// TestParseConfigEditsRejects is the client-side validation matrix. Every one
// of these fails before a client is built, so a typo costs a message rather
// than a partly configured workload.
func TestParseConfigEditsRejects(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr []string
	}{
		{
			name:    "env without a value",
			args:    []string{"--env=LOG_LEVEL"},
			wantErr: []string{"--env", "LOG_LEVEL", "KEY=VALUE", "KEY-"},
		},
		{
			name:    "env with an empty name",
			args:    []string{"--env==debug"},
			wantErr: []string{"--env", "name is empty"},
		},
		{
			name:    "env name is not a variable name",
			args:    []string{"--env=log-level=debug"},
			wantErr: []string{"--env", "log-level", "valid environment variable name"},
		},
		{
			name:    "env named twice",
			args:    []string{"--env=A=1", "--env=A=2"},
			wantErr: []string{"--env A", "more than once"},
		},
		{
			name:    "env set and removed in one deploy",
			args:    []string{"--env=A=1", "--env=A-"},
			wantErr: []string{"--env A", "more than once"},
		},
		{
			name:    "env from with an empty name",
			args:    []string{"--env-from-configmap=:PREFIX_"},
			wantErr: []string{"--env-from-configmap", "name is empty"},
		},
		{
			name:    "env from name is not a DNS label",
			args:    []string{"--env-from-secret=Api_Keys"},
			wantErr: []string{"--env-from-secret", "Api_Keys", "not valid"},
		},
		{
			name:    "env from prefix is not a C identifier",
			args:    []string{"--env-from-configmap=app-config:my-prefix-"},
			wantErr: []string{"--env-from-configmap", "my-prefix-", "app-config:APP_"},
		},
		{
			name:    "env from repeated exactly",
			args:    []string{"--env-from-configmap=app", "--env-from-configmap=app"},
			wantErr: []string{"--env-from-configmap app", "more than once"},
		},
		{
			name:    "mount without a path",
			args:    []string{"--configmap=app-files"},
			wantErr: []string{"--configmap", "NAME:/mount/path", "NAME-"},
		},
		{
			name:    "mount with an empty name",
			args:    []string{"--secret=:/etc/tls"},
			wantErr: []string{"--secret", "name is empty"},
		},
		{
			name:    "mount with an empty path",
			args:    []string{"--configmap=app-files:"},
			wantErr: []string{"--configmap", "mount path is empty"},
		},
		{
			name:    "relative mount path",
			args:    []string{"--configmap=app-files:etc/app"},
			wantErr: []string{"--configmap", "etc/app", "must be absolute"},
		},
		{
			name:    "mount name is not a DNS label",
			args:    []string{"--secret=TLS_Cert:/etc/tls"},
			wantErr: []string{"--secret", "TLS_Cert", "not valid"},
		},
		{
			name:    "one source mounted at two paths",
			args:    []string{"--configmap=app-files:/etc/a", "--configmap=app-files:/etc/b"},
			wantErr: []string{"--configmap", "app-files", "more than once"},
		},
		{
			name:    "two sources at one path",
			args:    []string{"--configmap=a:/etc/app", "--configmap=b:/etc/app"},
			wantErr: []string{"--configmap", "/etc/app", "own path"},
		},
		{
			name:    "label without a value",
			args:    []string{"--label=team"},
			wantErr: []string{"--label", "key=value", "key-"},
		},
		{
			name:    "label with an empty key",
			args:    []string{"--label==platform"},
			wantErr: []string{"--label", "key is empty"},
		},
		{
			name:    "label key is not qualified",
			args:    []string{"--label=a/b/c=platform"},
			wantErr: []string{"--label", "not valid"},
		},
		{
			name:    "label value is not a label value",
			args:    []string{"--label=team=-platform"},
			wantErr: []string{"--label", "-platform", "not valid"},
		},
		{
			name:    "label named twice",
			args:    []string{"--label=team=a", "--label=team=b"},
			wantErr: []string{"--label team", "more than once"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd, opts := command()
			if err := cmd.Flags().Parse(append([]string{testWorkload, imageFlag}, tc.args...)); err != nil {
				t.Fatalf("parsing flags: %v", err)
			}
			_, err := parseConfigEdits(opts)
			if err == nil {
				t.Fatalf("parsing %v must fail", tc.args)
			}
			for _, want := range tc.wantErr {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error does not mention %q: %v", want, err)
				}
			}
			// The same input must fail through the command's own validation,
			// which is what stands between a user and an apply.
			if err := validateFlags(cmd, opts); err == nil {
				t.Errorf("validateFlags accepted %v", tc.args)
			}
		})
	}
}

// TestConfigFlagsRejectedWithManifest keeps the manifest path out of reach of
// the flag path: a manifest states its own configuration, and a flag that
// half-edited it would leave neither source authoritative.
func TestConfigFlagsRejectedWithManifest(t *testing.T) {
	tests := []struct {
		arg       string
		wantField string
	}{
		{arg: "--env=A=1", wantField: "containers[].env"},
		{arg: "--env-from-configmap=app", wantField: "envFrom[].configMapRef"},
		{arg: "--env-from-secret=api-keys", wantField: "envFrom[].secretRef"},
		{arg: "--configmap=app-files:/etc/app", wantField: "volumes[].configMap"},
		{arg: "--secret=tls-cert:/etc/tls", wantField: "volumes[].secret"},
		{arg: "--label=team=platform", wantField: "spec.template.metadata.labels"},
	}

	for _, tc := range tests {
		t.Run(tc.arg, func(t *testing.T) {
			cmd, opts := command()
			if err := cmd.Flags().Parse([]string{"-f", "workload.yaml", tc.arg}); err != nil {
				t.Fatalf("parsing flags: %v", err)
			}
			err := validateFlags(cmd, opts)
			if err == nil {
				t.Fatalf("%s with -f must be refused", tc.arg)
			}
			flag, _, _ := strings.Cut(tc.arg, "=")
			for _, want := range []string{flag + " cannot be combined with -f", tc.wantField} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error does not mention %q: %v", want, err)
				}
			}
		})
	}
}

// TestCarriesConfigForward is the behaviour the whole feature turns on: a
// routine image bump must not silently empty the container's environment,
// unmount its config, or drop the labels other tooling selects on.
func TestCarriesConfigForward(t *testing.T) {
	existing := configuredWorkload()
	cfg := configFrom(t, existing, false)

	container := existing.Spec.Template.Spec.Runtime.Sandbox.Containers[0]
	if !reflect.DeepEqual(cfg.env, container.Env) {
		t.Errorf("env after a redeploy = %+v, want %+v", cfg.env, container.Env)
	}
	if !reflect.DeepEqual(cfg.envFrom, container.EnvFrom) {
		t.Errorf("envFrom after a redeploy = %+v, want %+v", cfg.envFrom, container.EnvFrom)
	}
	if !reflect.DeepEqual(cfg.volumes, existing.Spec.Template.Spec.Volumes) {
		t.Errorf("volumes after a redeploy = %+v, want %+v", cfg.volumes, existing.Spec.Template.Spec.Volumes)
	}
	if !reflect.DeepEqual(cfg.attachments, container.VolumeAttachments) {
		t.Errorf("volume attachments after a redeploy = %+v, want %+v", cfg.attachments, container.VolumeAttachments)
	}
	if !reflect.DeepEqual(cfg.workloadLabels, existing.Labels) {
		t.Errorf("workload labels after a redeploy = %v, want %v", cfg.workloadLabels, existing.Labels)
	}
	if !reflect.DeepEqual(cfg.templateLabels, existing.Spec.Template.Labels) {
		t.Errorf("template labels after a redeploy = %v, want %v", cfg.templateLabels, existing.Spec.Template.Labels)
	}
}

// TestCreateStartsEmpty covers the other half: a workload that does not exist
// has nothing to carry, so only the flags given decide what it gets.
func TestCreateStartsEmpty(t *testing.T) {
	cfg := configFrom(t, workload(), true, "--env=LOG_LEVEL=debug")

	want := []corev1.EnvVar{{Name: "LOG_LEVEL", Value: "debug"}}
	if !reflect.DeepEqual(cfg.env, want) {
		t.Errorf("env on create = %+v, want %+v", cfg.env, want)
	}
	if len(cfg.volumes) != 0 || len(cfg.attachments) != 0 || len(cfg.workloadLabels) != 0 {
		t.Errorf("create carried something forward: volumes=%+v attachments=%+v labels=%v",
			cfg.volumes, cfg.attachments, cfg.workloadLabels)
	}
}

// TestMergeOverExisting checks that a flag edits the workload's configuration
// rather than replacing it, and that a variable already set keeps its position
// so the order a user declared them in survives every redeploy.
func TestMergeOverExisting(t *testing.T) {
	cfg := configFrom(t, configuredWorkload(), false, "--env=LOG_LEVEL=debug", "--env=REGION=us-east-1")

	want := []corev1.EnvVar{
		{Name: "LOG_LEVEL", Value: "debug"},
		{Name: "ALLOWED_ORIGINS", Value: "a.example,b.example"},
		{Name: "REGION", Value: "us-east-1"},
	}
	if !reflect.DeepEqual(cfg.env, want) {
		t.Fatalf("env after an edit = %+v, want %+v", cfg.env, want)
	}
}

// TestExplicitRemoval covers the KEY- form for each flag, which is the only
// way anything is ever taken away.
func TestExplicitRemoval(t *testing.T) {
	cfg := configFrom(t, configuredWorkload(), false,
		"--env=LOG_LEVEL-",
		"--env-from-secret=api-keys-",
		"--configmap=app-files-",
		"--label=tier-",
	)

	if len(cfg.env) != 1 || cfg.env[0].Name != "ALLOWED_ORIGINS" {
		t.Errorf("env after removing LOG_LEVEL = %+v, want only ALLOWED_ORIGINS", cfg.env)
	}
	if len(cfg.envFrom) != 1 || envFromName(cfg.envFrom[0]) != "app-config" {
		t.Errorf("envFrom after removing api-keys = %+v, want only app-config", cfg.envFrom)
	}
	if len(cfg.volumes) != 1 || cfg.volumes[0].Name != "tls-cert" {
		t.Errorf("volumes after unmounting app-files = %+v, want only tls-cert", cfg.volumes)
	}
	if len(cfg.attachments) != 1 || cfg.attachments[0].Name != "tls-cert" {
		t.Errorf("attachments after unmounting app-files = %+v, want only tls-cert", cfg.attachments)
	}
	wantLabels := map[string]string{"team": "platform"}
	if !reflect.DeepEqual(cfg.templateLabels, wantLabels) || !reflect.DeepEqual(cfg.workloadLabels, wantLabels) {
		t.Errorf("labels after removing tier = workload %v, template %v, want %v",
			cfg.workloadLabels, cfg.templateLabels, wantLabels)
	}
}

// TestRemovingWhatIsNotThere keeps a deploy script runnable twice: clearing a
// variable that is already gone is a no-op, not a failure.
func TestRemovingWhatIsNotThere(t *testing.T) {
	cfg := configFrom(t, workload(), true, "--env=NOPE-", "--configmap=nope-", "--label=nope-", "--env-from-secret=nope-")

	if len(cfg.env) != 0 || len(cfg.volumes) != 0 || len(cfg.attachments) != 0 ||
		len(cfg.envFrom) != 0 || len(cfg.templateLabels) != 0 {
		t.Fatalf("removing absent entries produced %+v", cfg)
	}
}

// TestMountSourceAsymmetry pins the field each kind of volume names its source
// with: a configMap uses `name` and a secret uses `secretName`, and the two
// are not interchangeable.
func TestMountSourceAsymmetry(t *testing.T) {
	cfg := configFrom(t, workload(), true, "--configmap=app-files:/etc/app", "--secret=tls-cert:/etc/tls")

	appPath := "/etc/app"
	tlsPath := "/etc/tls"
	wantVolumes := []computev1alpha.InstanceVolume{
		{
			Name: "app-files",
			VolumeSource: computev1alpha.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: "app-files"},
				},
			},
		},
		{
			Name: "tls-cert",
			VolumeSource: computev1alpha.VolumeSource{
				Secret: &corev1.SecretVolumeSource{SecretName: "tls-cert"},
			},
		},
	}
	wantAttachments := []computev1alpha.VolumeAttachment{
		{Name: "app-files", MountPath: &appPath},
		{Name: "tls-cert", MountPath: &tlsPath},
	}

	if !reflect.DeepEqual(cfg.volumes, wantVolumes) {
		t.Errorf("volumes = %+v, want %+v", cfg.volumes, wantVolumes)
	}
	if !reflect.DeepEqual(cfg.attachments, wantAttachments) {
		t.Errorf("attachments = %+v, want %+v", cfg.attachments, wantAttachments)
	}
}

// TestEveryVolumeIsAttached guards the pairing validation insists on: a volume
// no container attaches is rejected by the server, so the two lists are always
// written together.
func TestEveryVolumeIsAttached(t *testing.T) {
	cases := [][]string{
		{"--configmap=app-files:/etc/app"},
		{"--secret=tls-cert:/etc/tls"},
		{"--configmap=a:/etc/a", "--secret=b:/etc/b", "--configmap=c:/etc/c"},
		{"--configmap=app-files-"},
		{"--secret=tls-cert:/srv/tls"},
	}

	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			for _, cfg := range []*containerConfig{
				configFrom(t, workload(), true, args...),
				configFrom(t, configuredWorkload(), false, args...),
			} {
				attached := map[string]bool{}
				for _, a := range cfg.attachments {
					attached[a.Name] = true
				}
				if len(cfg.volumes) != len(cfg.attachments) {
					t.Fatalf("%d volumes but %d attachments: %+v / %+v",
						len(cfg.volumes), len(cfg.attachments), cfg.volumes, cfg.attachments)
				}
				for _, v := range cfg.volumes {
					if !attached[v.Name] {
						t.Errorf("volume %q is not attached", v.Name)
					}
					if v.ConfigMap == nil && v.Secret == nil {
						t.Errorf("volume %q has no source", v.Name)
					}
				}
				for _, a := range cfg.attachments {
					if a.MountPath == nil || *a.MountPath == "" {
						t.Errorf("attachment %q has no mount path", a.Name)
					}
				}
			}
		})
	}
}

// TestMountRemountKeepsThePair covers changing where an existing mount lands:
// the volume stays, the attachment moves, and nothing is duplicated.
func TestMountRemountKeepsThePair(t *testing.T) {
	cfg := configFrom(t, configuredWorkload(), false, "--secret=tls-cert:/srv/tls")

	if len(cfg.volumes) != 2 || len(cfg.attachments) != 2 {
		t.Fatalf("remount changed the volume count: %+v / %+v", cfg.volumes, cfg.attachments)
	}
	for _, a := range cfg.attachments {
		if a.Name == "tls-cert" && *a.MountPath != "/srv/tls" {
			t.Errorf("tls-cert mounted at %s, want /srv/tls", *a.MountPath)
		}
	}
}

// TestResolveContainerConfigRejects covers the checks that need the workload
// in hand, and so cannot be made while parsing the flags alone.
func TestResolveContainerConfigRejects(t *testing.T) {
	tests := []struct {
		name     string
		existing *computev1alpha.Workload
		creating bool
		args     []string
		wantErr  []string
	}{
		{
			name:     "a configmap and a secret of one name collide",
			existing: workload(),
			creating: true,
			args:     []string{"--configmap=shared:/etc/cm", "--secret=shared:/etc/secret"},
			wantErr:  []string{`"shared"`, "volume named", "manifest"},
		},
		{
			name:     "a mount collides with the kind already mounted",
			existing: configuredWorkload(),
			args:     []string{"--secret=app-files:/etc/app"},
			wantErr:  []string{`"app-files"`, "configMap", "manifest"},
		},
		{
			name:     "a new mount collides with a path already in use",
			existing: configuredWorkload(),
			args:     []string{"--configmap=extra:/etc/tls"},
			wantErr:  []string{"/etc/tls", "tls-cert", "extra"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := resolveContainerConfig(tc.existing, tc.creating, editsFrom(t, tc.args...))
			if err == nil {
				t.Fatalf("resolving %v must fail", tc.args)
			}
			for _, want := range tc.wantErr {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error does not mention %q: %v", want, err)
				}
			}
		})
	}
}

// TestUnattachedVolumesAreDropped covers a workload whose volume no container
// attaches any more. A flag deploy writes one container, so carrying such a
// volume forward would produce a spec the server rejects outright, taking the
// deploy down with it.
func TestUnattachedVolumesAreDropped(t *testing.T) {
	existing := configuredWorkload()
	existing.Spec.Template.Spec.Volumes = append(existing.Spec.Template.Spec.Volumes,
		computev1alpha.InstanceVolume{
			Name: "orphan",
			VolumeSource: computev1alpha.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: "orphan"},
				},
			},
		})

	cfg := configFrom(t, existing, false)
	for _, v := range cfg.volumes {
		if v.Name == "orphan" {
			t.Fatalf("an unattached volume was carried forward: %+v", cfg.volumes)
		}
	}
}

// TestPlanConfigLines checks the plan states what the workload will have after
// the apply, carried-forward entries included, since that is what the Apply
// prompt is asking about.
func TestPlanConfigLines(t *testing.T) {
	lines := strings.Join(planConfigLines(configFrom(t, configuredWorkload(), false, "--env=REGION=us-east-1")), "\n")

	for _, want := range []string{
		"Environment:", "3 variables", "LOG_LEVEL", "ALLOWED_ORIGINS", "REGION",
		"Environment from:", "configMap/app-config", "secret/api-keys as SECRET_*",
		"Mounts:", "configMap/app-files → /etc/app", "secret/tls-cert → /etc/tls",
		"Labels:", "team=platform", "tier=api",
	} {
		if !strings.Contains(lines, want) {
			t.Errorf("plan does not mention %q:\n%s", want, lines)
		}
	}
}

// TestPlanConfigLinesEmpty keeps the plan quiet for a workload with nothing
// configured, so the common deploy does not grow three empty lines.
func TestPlanConfigLinesEmpty(t *testing.T) {
	if lines := planConfigLines(configFrom(t, workload(), true)); len(lines) != 0 {
		t.Fatalf("plan for an unconfigured workload = %v, want no lines", lines)
	}
}
