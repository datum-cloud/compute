package validation

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"google.golang.org/protobuf/proto"
	authorizationv1 "k8s.io/api/authorization/v1"
	k8scorev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
)

func TestValidateWorkloads(t *testing.T) {
	scenarios := map[string]struct {
		workload         *computev1alpha.Workload
		expectedErrors   field.ErrorList
		opts             WorkloadValidationOptions
		interceptorFuncs *interceptor.Funcs
	}{
		"basic fields create": {
			workload: &computev1alpha.Workload{},
			expectedErrors: field.ErrorList{
				field.NotSupported(field.NewPath("spec.template.spec.runtime.resources"), "", []string{}),
				field.Required(field.NewPath("spec.template.spec.runtime"), ""),
				field.Required(field.NewPath("spec.template.spec.networkInterfaces"), ""),
				field.Required(field.NewPath("spec.placements"), ""),
			},
		},
		"missing cityCode": {
			workload: MakeSandboxWorkload(
				"test",
				func(w *computev1alpha.Workload) {
					w.Spec.Placements[0].CityCodes = []string{}
				},
			),
			expectedErrors: field.ErrorList{
				field.Required(field.NewPath("spec.placements[0].cityCodes"), ""),
			},
		},
		"invalid cityCode": {
			workload: MakeSandboxWorkload(
				"test",
				func(w *computev1alpha.Workload) {
					w.Spec.Placements[0].CityCodes = []string{"TEST"}
				},
			),
			expectedErrors: field.ErrorList{
				field.NotSupported(field.NewPath("spec.placements[0].cityCodes[0]"), "TEST", []string{}),
			},
		},
		"missing placement name": {
			workload: MakeSandboxWorkload(
				"test",
				func(w *computev1alpha.Workload) {
					w.Spec.Placements[0].Name = ""
				},
			),
			expectedErrors: field.ErrorList{
				field.Required(field.NewPath("spec.placements[0].name"), ""),
			},
		},
		"invalid placement name": {
			workload: MakeSandboxWorkload(
				"test",
				func(w *computev1alpha.Workload) {
					w.Spec.Placements[0].Name = "#@$@#$@"
				},
			),
			expectedErrors: field.ErrorList{
				field.Invalid(field.NewPath("spec.placements[0].name"), "", ""),
			},
		},
		"invalid minReplicas": {
			workload: MakeSandboxWorkload(
				"test",
				func(w *computev1alpha.Workload) {
					w.Spec.Placements[0].ScaleSettings.MinReplicas = -1
				},
			),
			expectedErrors: field.ErrorList{
				field.Invalid(field.NewPath("spec.placements[0].scaleSettings.minReplicas"), "", ""),
			},
		},
		"minReplicas too large": {
			workload: MakeSandboxWorkload(
				"test",
				func(w *computev1alpha.Workload) {
					w.Spec.Placements[0].ScaleSettings.MinReplicas = 9999
				},
			),
			expectedErrors: field.ErrorList{
				field.Invalid(field.NewPath("spec.placements[0].scaleSettings.minReplicas"), "", ""),
			},
		},
		"maxReplicas missing scaling metrics": {
			workload: MakeSandboxWorkload(
				"test",
				func(w *computev1alpha.Workload) {
					w.Spec.Placements[0].ScaleSettings.MaxReplicas = proto.Int32(2)
				},
			),
			expectedErrors: field.ErrorList{
				field.Required(field.NewPath("spec.placements[0].scaleSettings.metrics"), ""),
			},
		},
		"scaling metric missing resource": {
			workload: MakeSandboxWorkload(
				"test",
				func(w *computev1alpha.Workload) {
					w.Spec.Placements[0].ScaleSettings.MaxReplicas = proto.Int32(2)
					w.Spec.Placements[0].ScaleSettings.Metrics = []computev1alpha.MetricSpec{
						{},
					}
				},
			),
			expectedErrors: field.ErrorList{
				field.Required(field.NewPath("spec.placements[0].scaleSettings.metrics[0].resource"), ""),
			},
		},
		"invalid resource name and missing target in scaling metric": {
			workload: MakeSandboxWorkload(
				"test",
				func(w *computev1alpha.Workload) {
					w.Spec.Placements[0].ScaleSettings.MaxReplicas = proto.Int32(2)
					w.Spec.Placements[0].ScaleSettings.Metrics = []computev1alpha.MetricSpec{
						{
							Resource: &computev1alpha.ResourceMetricSource{
								Name: "invalid",
							},
						},
					}
				},
			),
			expectedErrors: field.ErrorList{
				field.NotSupported(field.NewPath("spec.placements[0].scaleSettings.metrics[0].resource.name"), "", []string{}),
				field.Required(field.NewPath("spec.placements[0].scaleSettings.metrics[0].resource.target"), ""),
			},
		},
		"too many metric target values": {
			workload: MakeSandboxWorkload(
				"test",
				func(w *computev1alpha.Workload) {
					w.Spec.Placements[0].ScaleSettings.MaxReplicas = proto.Int32(2)
					w.Spec.Placements[0].ScaleSettings.Metrics = []computev1alpha.MetricSpec{
						{
							Resource: &computev1alpha.ResourceMetricSource{
								Name: "cpu",
								Target: computev1alpha.MetricTarget{
									Value:              resource.NewQuantity(50, resource.DecimalSI),
									AverageValue:       resource.NewQuantity(50, resource.DecimalSI),
									AverageUtilization: proto.Int32(50),
								},
							},
						},
					}
				},
			),
			expectedErrors: field.ErrorList{
				field.Forbidden(field.NewPath("spec.placements[0].scaleSettings.metrics[0].resource.target.averageValue"), ""),
				field.Forbidden(field.NewPath("spec.placements[0].scaleSettings.metrics[0].resource.target.averageUtilization"), ""),
			},
		},
		"invalid metric target value": {
			workload: MakeSandboxWorkload(
				"test",
				func(w *computev1alpha.Workload) {
					w.Spec.Placements[0].ScaleSettings.MaxReplicas = proto.Int32(2)
					w.Spec.Placements[0].ScaleSettings.Metrics = []computev1alpha.MetricSpec{
						{
							Resource: &computev1alpha.ResourceMetricSource{
								Name: "cpu",
								Target: computev1alpha.MetricTarget{
									Value: resource.NewQuantity(-1, resource.DecimalSI),
								},
							},
						},
					}
				},
			),
			expectedErrors: field.ErrorList{
				field.Invalid(field.NewPath("spec.placements[0].scaleSettings.metrics[0].resource.target.value"), "", ""),
			},
		},
		"invalid metric target averageValue": {
			workload: MakeSandboxWorkload(
				"test",
				func(w *computev1alpha.Workload) {
					w.Spec.Placements[0].ScaleSettings.MaxReplicas = proto.Int32(2)
					w.Spec.Placements[0].ScaleSettings.Metrics = []computev1alpha.MetricSpec{
						{
							Resource: &computev1alpha.ResourceMetricSource{
								Name: "cpu",
								Target: computev1alpha.MetricTarget{
									AverageValue: resource.NewQuantity(-1, resource.DecimalSI),
								},
							},
						},
					}
				},
			),
			expectedErrors: field.ErrorList{
				field.Invalid(field.NewPath("spec.placements[0].scaleSettings.metrics[0].resource.target.averageValue"), "", ""),
			},
		},
		"invalid metric target averageUtilization": {
			workload: MakeSandboxWorkload(
				"test",
				func(w *computev1alpha.Workload) {
					w.Spec.Placements[0].ScaleSettings.MaxReplicas = proto.Int32(2)
					w.Spec.Placements[0].ScaleSettings.Metrics = []computev1alpha.MetricSpec{
						{
							Resource: &computev1alpha.ResourceMetricSource{
								Name: "cpu",
								Target: computev1alpha.MetricTarget{
									AverageUtilization: proto.Int32(0),
								},
							},
						},
					}
				},
			),
			expectedErrors: field.ErrorList{
				field.Invalid(field.NewPath("spec.placements[0].scaleSettings.metrics[0].resource.target.averageUtilization"), "", ""),
			},
		},
		"multiple runtimes": {
			workload: MakeSandboxWorkload(
				"test",
				func(w *computev1alpha.Workload) {
					w.Spec.Template.Spec.Runtime.VirtualMachine = &computev1alpha.VirtualMachineRuntime{}
					w.Spec.Template.Annotations = map[string]string{
						computev1alpha.SSHKeysAnnotation: "user:ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAILPbDbsv9fgEnam9iJ5b51Na/WieeiKCJRC0+m7fRwPk vscode@42aafaf8293e",
					}
				},
			),
			expectedErrors: field.ErrorList{
				field.Forbidden(field.NewPath("spec.template.spec.runtime.virtualMachine"), ""),
			},
		},
		"vm requires ssh key": {
			workload: MakeVMWorkload(
				"test",
				func(w *computev1alpha.Workload) {
					delete(w.Spec.Template.Annotations, computev1alpha.SSHKeysAnnotation)
				},
			),
			expectedErrors: field.ErrorList{
				field.Required(field.NewPath("spec.template.annotations").Key("compute.datumapis.com/ssh-keys"), ""),
			},
		},
		"invalid ssh key annotation format": {
			workload: MakeVMWorkload(
				"test",
				func(w *computev1alpha.Workload) {
					w.Spec.Template.Annotations = map[string]string{
						computev1alpha.SSHKeysAnnotation: "invalid",
					}
				},
			),
			expectedErrors: field.ErrorList{
				field.Invalid(field.NewPath("spec.template.annotations").Key("compute.datumapis.com/ssh-keys").Index(0), "", ""),
			},
		},
		"ssh key missing username and has bad public key": {
			workload: MakeVMWorkload(
				"test",
				func(w *computev1alpha.Workload) {
					w.Spec.Template.Annotations = map[string]string{
						computev1alpha.SSHKeysAnnotation: ":invalid",
					}
				},
			),
			expectedErrors: field.ErrorList{
				field.Required(field.NewPath("spec.template.annotations").Key("compute.datumapis.com/ssh-keys").Index(0), ""),
				field.Invalid(field.NewPath("spec.template.annotations").Key("compute.datumapis.com/ssh-keys").Index(0), "", ""),
			},
		},
		"good ssh key": {
			workload: MakeVMWorkload(
				"test",
			),
			expectedErrors: field.ErrorList{},
		},
		"invalid boot volume source": {
			workload: MakeVMWorkload(
				"test",
				func(w *computev1alpha.Workload) {
					w.Spec.Template.Spec.Volumes[0].Disk.Template.Spec.Resources = &computev1alpha.DiskResourceRequirements{
						Requests: k8scorev1.ResourceList{
							k8scorev1.ResourceStorage: resource.MustParse("10Gi"),
						},
					}
					w.Spec.Template.Spec.Volumes[0].Disk.Template.Spec.Populator = &computev1alpha.DiskPopulator{
						Filesystem: &computev1alpha.FilesystemDiskPopulator{
							Type: "ext4",
						},
					}
				},
			),
			expectedErrors: field.ErrorList{
				field.Required(field.NewPath("spec.template.spec.runtime.virtualMachine.volumeAttachments[0].name"), ""),
			},
		},
		"populator resources do not match requested resources": {
			workload: MakeVMWorkload(
				"test",
				func(w *computev1alpha.Workload) {
					w.Spec.Template.Spec.Volumes[0].Disk.Template.Spec.Resources = &computev1alpha.DiskResourceRequirements{
						Requests: k8scorev1.ResourceList{
							k8scorev1.ResourceStorage: resource.MustParse("10Gi"),
						},
					}
				},
			),
			expectedErrors: field.ErrorList{
				field.Invalid(field.NewPath("spec.template.spec.volumes[0].disk.template.spec.resources.requests[storage]"), "", ""),
			},
		},
		"disk volume too small": {
			workload: MakeVMWorkload(
				"test",
				func(w *computev1alpha.Workload) {
					w.Spec.Template.Spec.Runtime.VirtualMachine.VolumeAttachments = append(
						w.Spec.Template.Spec.Runtime.VirtualMachine.VolumeAttachments,
						computev1alpha.VolumeAttachment{
							Name: "vol",
						},
					)
					w.Spec.Template.Spec.Volumes = append(w.Spec.Template.Spec.Volumes, computev1alpha.InstanceVolume{
						Name: "vol",
						VolumeSource: computev1alpha.VolumeSource{
							Disk: &computev1alpha.DiskTemplateVolumeSource{
								Template: &computev1alpha.DiskTemplateVolumeSourceTemplate{
									Spec: computev1alpha.DiskSpec{
										Type: "pd-standard",
										Resources: &computev1alpha.DiskResourceRequirements{
											Requests: k8scorev1.ResourceList{
												k8scorev1.ResourceStorage: resource.MustParse("1Gi"),
											},
										},
									},
								},
							},
						},
					})
				},
			),
			expectedErrors: field.ErrorList{
				field.Invalid(field.NewPath("spec.template.spec.volumes[1].disk.template.spec.resources.requests[storage]"), "", ""),
			},
		},
		"disk volume too large": {
			workload: MakeVMWorkload(
				"test",
				func(w *computev1alpha.Workload) {
					w.Spec.Template.Spec.Runtime.VirtualMachine.VolumeAttachments = append(
						w.Spec.Template.Spec.Runtime.VirtualMachine.VolumeAttachments,
						computev1alpha.VolumeAttachment{
							Name: "vol",
						},
					)
					w.Spec.Template.Spec.Volumes = append(w.Spec.Template.Spec.Volumes, computev1alpha.InstanceVolume{
						Name: "vol",
						VolumeSource: computev1alpha.VolumeSource{
							Disk: &computev1alpha.DiskTemplateVolumeSource{
								Template: &computev1alpha.DiskTemplateVolumeSourceTemplate{
									Spec: computev1alpha.DiskSpec{
										Type: "pd-standard",
										Resources: &computev1alpha.DiskResourceRequirements{
											Requests: k8scorev1.ResourceList{
												k8scorev1.ResourceStorage: resource.MustParse("1Pi"),
											},
										},
									},
								},
							},
						},
					})
				},
			),
			expectedErrors: field.ErrorList{
				field.Invalid(field.NewPath("spec.template.spec.volumes[1].disk.template.spec.resources.requests[storage]"), "", ""),
			},
		},
		"disk volume not 1Gi increment": {
			workload: MakeVMWorkload(
				"test",
				func(w *computev1alpha.Workload) {
					w.Spec.Template.Spec.Runtime.VirtualMachine.VolumeAttachments = append(
						w.Spec.Template.Spec.Runtime.VirtualMachine.VolumeAttachments,
						computev1alpha.VolumeAttachment{
							Name: "vol",
						},
					)
					w.Spec.Template.Spec.Volumes = append(w.Spec.Template.Spec.Volumes, computev1alpha.InstanceVolume{
						Name: "vol",
						VolumeSource: computev1alpha.VolumeSource{
							Disk: &computev1alpha.DiskTemplateVolumeSource{
								Template: &computev1alpha.DiskTemplateVolumeSourceTemplate{
									Spec: computev1alpha.DiskSpec{
										Type: "pd-standard",
										Resources: &computev1alpha.DiskResourceRequirements{
											Requests: k8scorev1.ResourceList{
												k8scorev1.ResourceStorage: resource.MustParse("10.5Gi"),
											},
										},
									},
								},
							},
						},
					})
				},
			),
			expectedErrors: field.ErrorList{
				field.Invalid(field.NewPath("spec.template.spec.volumes[1].disk.template.spec.resources.requests[storage]"), "", ""),
			},
		},
		"invalid volume names": {
			workload: MakeSandboxWorkload(
				"test",
				func(w *computev1alpha.Workload) {
					volumeSource := computev1alpha.VolumeSource{
						Disk: &computev1alpha.DiskTemplateVolumeSource{
							Template: &computev1alpha.DiskTemplateVolumeSourceTemplate{
								Spec: computev1alpha.DiskSpec{
									Type: "pd-standard",
									Resources: &computev1alpha.DiskResourceRequirements{
										Requests: k8scorev1.ResourceList{
											k8scorev1.ResourceStorage: resource.MustParse("10Gi"),
										},
									},
								},
							},
						},
					}
					w.Spec.Template.Spec.Volumes = []computev1alpha.InstanceVolume{
						{
							Name:         "Not valid and also a duplicate",
							VolumeSource: volumeSource,
						},
						{
							Name:         "Not valid and also a duplicate",
							VolumeSource: volumeSource,
						},
					}
				},
			),
			expectedErrors: field.ErrorList{
				field.Invalid(field.NewPath("spec.template.spec.volumes[0].name"), "", ""),
				field.Required(field.NewPath("spec.template.spec.volumes[0].name"), ""),
				field.Invalid(field.NewPath("spec.template.spec.volumes[1].name"), "", ""),
				field.Duplicate(field.NewPath("spec.template.spec.volumes[1].name"), ""),
			},
		},
		"invalid volume attachments": {
			workload: MakeSandboxWorkload(
				"test",
				func(w *computev1alpha.Workload) {
					volumeSource := computev1alpha.VolumeSource{
						Disk: &computev1alpha.DiskTemplateVolumeSource{
							Template: &computev1alpha.DiskTemplateVolumeSourceTemplate{
								Spec: computev1alpha.DiskSpec{
									Type: "pd-standard",
									Resources: &computev1alpha.DiskResourceRequirements{
										Requests: k8scorev1.ResourceList{
											k8scorev1.ResourceStorage: resource.MustParse("10Gi"),
										},
									},
									Populator: &computev1alpha.DiskPopulator{
										Filesystem: &computev1alpha.FilesystemDiskPopulator{
											Type: "ext4",
										},
									},
								},
							},
						},
					}
					w.Spec.Template.Spec.Runtime.Sandbox.Containers[0].VolumeAttachments = []computev1alpha.VolumeAttachment{
						{
							Name:      "duplicate-mount-path",
							MountPath: proto.String("/mount1"),
						},
						{
							Name:      "duplicate-mount-path",
							MountPath: proto.String("/mount1"),
						},
						{
							Name: "missing-volume",
						},
					}
					w.Spec.Template.Spec.Volumes = []computev1alpha.InstanceVolume{
						{
							Name:         "duplicate-mount-path",
							VolumeSource: volumeSource,
						},
					}
				},
			),
			expectedErrors: field.ErrorList{
				field.Duplicate(field.NewPath("spec.template.spec.runtime.sandbox.containers[0].volumeAttachments[1].mountPath"), ""),
				field.NotFound(field.NewPath("spec.template.spec.runtime.sandbox.containers[0].volumeAttachments[2].name"), ""),
			},
		},
		"invalid ports": {
			workload: MakeSandboxWorkload(
				"test",
				func(w *computev1alpha.Workload) {
					w.Spec.Template.Spec.Runtime.Sandbox.Containers[0].Ports = []computev1alpha.NamedPort{
						{
							// Missing name, invalid port number
						},
						{
							Name: "must-be-shorter-than-15-characters",
							Port: 80,
						},
					}
				},
			),
			expectedErrors: field.ErrorList{
				field.Required(field.NewPath("spec.template.spec.runtime.sandbox.containers[0].ports[0].name"), ""),
				field.Invalid(field.NewPath("spec.template.spec.runtime.sandbox.containers[0].ports[0].port"), "", ""),
				field.Invalid(field.NewPath("spec.template.spec.runtime.sandbox.containers[0].ports[1].name"), "", ""),
			},
		},
		"network use denied": {
			workload: MakeSandboxWorkload("test"),
			interceptorFuncs: &interceptor.Funcs{
				Create: func(ctx context.Context, client client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
					if sar, ok := obj.(*authorizationv1.SubjectAccessReview); ok {
						if sar.Spec.ResourceAttributes.Name == "default" &&
							sar.Spec.ResourceAttributes.Group == networkingv1alpha.GroupVersion.Group &&
							sar.Spec.ResourceAttributes.Version == networkingv1alpha.GroupVersion.Version &&
							sar.Spec.ResourceAttributes.Resource == "networks" {
							sar.Status.Allowed = false
						}
					}
					return client.Create(ctx, obj, opts...)
				},
			},
			expectedErrors: field.ErrorList{
				field.Forbidden(field.NewPath("spec.template.spec.networkInterfaces[0].network"), ""),
			},
		},
	}

	initObjs := []client.Object{
		&networkingv1alpha.Network{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "default",
				Name:      "default",
			},
		},
	}

	scheme := k8sruntime.NewScheme()
	utilruntime.Must(computev1alpha.AddToScheme(scheme))
	utilruntime.Must(networkingv1alpha.AddToScheme(scheme))
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithInterceptorFuncs(interceptor.Funcs{
			Create: func(ctx context.Context, client client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				if sar, ok := obj.(*authorizationv1.SubjectAccessReview); ok {
					// The fake client only allow a resource to be created without a name
					sar.GenerateName = "sar-"
				}
				return client.Create(ctx, obj, opts...)
			},
		}).
		WithObjects(initObjs...).
		Build()

	for name, scenario := range scenarios {
		scenario.opts.Context = context.Background()
		scenario.opts.Workload = scenario.workload
		c := fakeClient

		if scenario.interceptorFuncs != nil {
			c = interceptor.NewClient(c, *scenario.interceptorFuncs)
		}

		scenario.opts.Client = interceptor.NewClient(
			c,
			interceptor.Funcs{
				Create: func(ctx context.Context, client client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
					if sar, ok := obj.(*authorizationv1.SubjectAccessReview); ok {
						sar.Status.Allowed = true
					}
					return client.Create(ctx, obj, opts...)
				},
			},
		)

		if len(scenario.opts.ValidCityCodes) == 0 {
			scenario.opts.ValidCityCodes = []string{"DFW"}
		}

		t.Run(name, func(t *testing.T) {
			errs := ValidateWorkloadCreate(scenario.workload, scenario.opts)

			delta := cmp.Diff(scenario.expectedErrors, errs, cmpopts.IgnoreFields(field.Error{}, "BadValue", "Detail"))
			if delta != "" {
				t.Errorf("Testcase %s - expected errors '%v', got '%v', diff: '%v'", name, scenario.expectedErrors, errs, delta)
			}
		})
	}
}

// Inspired by https://github.com/kubernetes/kubernetes/blob/79cca2786e037d8c8ae7fe856c5ae158b100ce71/pkg/api/pod/testing/make.go

type Tweak func(*computev1alpha.Workload)

// MakeSandboxWorkload returns a sandbox runtime workload that will pass
// validation. By default this produces a workload with a single container and
// single placement.
func MakeSandboxWorkload(name string, tweaks ...Tweak) *computev1alpha.Workload {
	workload := &computev1alpha.Workload{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: metav1.NamespaceDefault,
		},
		Spec: computev1alpha.WorkloadSpec{
			Template: computev1alpha.InstanceTemplateSpec{
				Spec: computev1alpha.InstanceSpec{
					NetworkInterfaces: []computev1alpha.InstanceNetworkInterface{
						{
							Network: networkingv1alpha.NetworkRef{
								Name: "default",
							},
						},
					},
					Runtime: computev1alpha.InstanceRuntimeSpec{
						Resources: computev1alpha.InstanceRuntimeResources{
							InstanceType: "datumcloud/d1-standard-2",
						},
						Sandbox: &computev1alpha.SandboxRuntime{
							Containers: []computev1alpha.SandboxContainer{
								{
									Name:  "container1",
									Image: "registry.tld/image:tag",
								},
							},
						},
					},
				},
			},
			Placements: []computev1alpha.WorkloadPlacement{
				{
					Name:      "placement1",
					CityCodes: []string{"DFW"},
					ScaleSettings: computev1alpha.HorizontalScaleSettings{
						MinReplicas: 1,
					},
				},
			},
		},
	}

	for _, tweak := range tweaks {
		tweak(workload)
	}

	return workload
}

// MakeVMWorkload returns a VM runtime workload that will pass validation. By
// default this produces a workload single placement.
func MakeVMWorkload(name string, tweaks ...Tweak) *computev1alpha.Workload {
	workload := &computev1alpha.Workload{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: metav1.NamespaceDefault,
		},
		Spec: computev1alpha.WorkloadSpec{
			Template: computev1alpha.InstanceTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						computev1alpha.SSHKeysAnnotation: "user:ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAILPbDbsv9fgEnam9iJ5b51Na/WieeiKCJRC0+m7fRwPk vscode@42aafaf8293e",
					},
				},
				Spec: computev1alpha.InstanceSpec{
					NetworkInterfaces: []computev1alpha.InstanceNetworkInterface{
						{
							Network: networkingv1alpha.NetworkRef{
								Name: "default",
							},
						},
					},
					Runtime: computev1alpha.InstanceRuntimeSpec{
						Resources: computev1alpha.InstanceRuntimeResources{
							InstanceType: "datumcloud/d1-standard-2",
						},
						VirtualMachine: &computev1alpha.VirtualMachineRuntime{
							VolumeAttachments: []computev1alpha.VolumeAttachment{
								{
									Name: "boot",
								},
							},
						},
					},
					Volumes: []computev1alpha.InstanceVolume{
						{
							Name: "boot",
							VolumeSource: computev1alpha.VolumeSource{
								Disk: &computev1alpha.DiskTemplateVolumeSource{
									Template: &computev1alpha.DiskTemplateVolumeSourceTemplate{
										Spec: computev1alpha.DiskSpec{
											Type: "pd-standard",
											Populator: &computev1alpha.DiskPopulator{
												Image: &computev1alpha.ImageDiskPopulator{
													Name: "datumcloud/ubuntu-2204-lts",
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
			Placements: []computev1alpha.WorkloadPlacement{
				{
					Name:      "placement1",
					CityCodes: []string{"DFW"},
					ScaleSettings: computev1alpha.HorizontalScaleSettings{
						MinReplicas: 1,
					},
				},
			},
		},
	}

	for _, tweak := range tweaks {
		tweak(workload)
	}

	return workload
}
