package instancecontrol

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"go.datum.net/compute/api/v1alpha"
)

const (
	testContainerName = "app"
	testImage         = "ghcr.io/example/app:v1"
	testInstanceType  = "datumcloud/d1-standard-2"
)

func minimalTemplate() v1alpha.InstanceTemplateSpec {
	return v1alpha.InstanceTemplateSpec{
		Spec: v1alpha.InstanceSpec{
			Runtime: v1alpha.InstanceRuntimeSpec{
				Resources: v1alpha.InstanceRuntimeResources{InstanceType: testInstanceType},
				Sandbox: &v1alpha.SandboxRuntime{Containers: []v1alpha.SandboxContainer{{
					Name:  testContainerName,
					Image: testImage,
				}}},
			},
		},
	}
}

func fullTemplate() v1alpha.InstanceTemplateSpec {
	return v1alpha.InstanceTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "web"}},
		Spec: v1alpha.InstanceSpec{
			Runtime: v1alpha.InstanceRuntimeSpec{
				Resources: v1alpha.InstanceRuntimeResources{InstanceType: testInstanceType},
				Sandbox: &v1alpha.SandboxRuntime{Containers: []v1alpha.SandboxContainer{{
					Name:  testContainerName,
					Image: testImage,
					Env: []corev1.EnvVar{
						{Name: "PLAIN", Value: "x"},
						{Name: "FROM_CM", ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{Name: "cfg"}, Key: "k",
						}}},
						{Name: "FROM_SECRET", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{Name: "sec"}, Key: "k",
						}}},
					},
					Resources: &v1alpha.ContainerResourceRequirements{
						Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("512Mi")},
						Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("250m")},
					},
					VolumeAttachments: []v1alpha.VolumeAttachment{{Name: "config", MountPath: ptr.To("/etc/app")}},
					Ports:             []v1alpha.NamedPort{{Name: "http", Port: 8080, Protocol: ptr.To(corev1.ProtocolTCP)}},
				}}},
			},
			Volumes: []v1alpha.InstanceVolume{
				{Name: "config", VolumeSource: v1alpha.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: "cfg"},
					Items:                []corev1.KeyToPath{{Key: "app.yaml", Path: "app.yaml"}},
				}}},
				{Name: "creds", VolumeSource: v1alpha.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "sec"}}},
			},
		},
	}
}

func TestComputeHash_Golden(t *testing.T) {
	tests := []struct {
		name     string
		template func() v1alpha.InstanceTemplateSpec
		want     string
	}{
		{
			name:     "minimal sandbox template",
			template: minimalTemplate,
			want:     "59548f985b",
		},
		{
			name: "env, resources, port protocol, no volumes",
			template: func() v1alpha.InstanceTemplateSpec {
				template := fullTemplate()
				template.Spec.Volumes = nil
				return template
			},
			want: "dd66f7c5f",
		},
		{
			name:     "env, resources, port protocol, ConfigMap and Secret volumes",
			template: fullTemplate,
			want:     "67b6d7c9f4",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.want, ComputeHash(test.template()),
				"the template hash changed for an unchanged template; every running instance with this template would be recreated")
		})
	}
}

func TestComputeHash_ResourceQuantityStringCache(t *testing.T) {
	template := fullTemplate()

	stringified := fullTemplate()
	limits := stringified.Spec.Runtime.Sandbox.Containers[0].Resources.Limits
	memory := limits[corev1.ResourceMemory]
	_ = memory.String()
	limits[corev1.ResourceMemory] = memory

	assert.Equal(t, ComputeHash(template), ComputeHash(stringified))
}

func TestTemplateHashes(t *testing.T) {
	hashes := HashTemplate(fullTemplate())

	changed := fullTemplate()
	changed.Spec.Runtime.Sandbox.Containers[0].Image = "ghcr.io/example/app:v2"
	changedHashes := HashTemplate(changed)

	assert.NotEqual(t, hashes.Current, hashes.Legacy)

	tests := []struct {
		name         string
		stored       string
		wantMatches  bool
		wantIsLegacy bool
	}{
		{name: "current hash", stored: hashes.Current, wantMatches: true, wantIsLegacy: false},
		{name: "legacy hash", stored: hashes.Legacy, wantMatches: true, wantIsLegacy: true},
		{name: "current hash of another template", stored: changedHashes.Current, wantMatches: false, wantIsLegacy: false},
		{name: "legacy hash of another template", stored: changedHashes.Legacy, wantMatches: false, wantIsLegacy: false},
		{name: "empty", stored: "", wantMatches: false, wantIsLegacy: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.wantMatches, hashes.Matches(test.stored))
			assert.Equal(t, test.wantIsLegacy, hashes.IsLegacy(test.stored))
		})
	}
}
