package validation

import (
	"context"
	"strings"
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
	"go.datum.net/compute/pkg/runtimeclass"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
	locationsv1alpha1 "go.miloapis.com/locations/api/v1alpha1"
)

const (
	testCPUResource        = "cpu"
	testVolName            = "vol"
	testDuplicateMountPath = "duplicate-mount-path"
	testDefaultNamespace   = "default"
	testCityCodeDFW        = "DFW"
	testImageNoRegistry    = "nginx:latest"
	testImageQualified     = "ghcr.io/acme/api:1.4.2"
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
		"location selector by city code": {
			workload: MakeSandboxWorkload(
				"test",
				func(w *computev1alpha.Workload) {
					w.Spec.Placements[0].Locations = nil
					w.Spec.Placements[0].LocationSelector = &metav1.LabelSelector{
						MatchLabels: map[string]string{locationsv1alpha1.TopologyCityCodeKey: testCityCodeDFW},
					}
				},
			),
			expectedErrors: field.ErrorList{},
		},
		"location selector by expression": {
			workload: MakeSandboxWorkload(
				"test",
				func(w *computev1alpha.Workload) {
					w.Spec.Placements[0].Locations = nil
					w.Spec.Placements[0].LocationSelector = &metav1.LabelSelector{
						MatchExpressions: []metav1.LabelSelectorRequirement{{
							Key:      locationsv1alpha1.TopologyCityCodeKey,
							Operator: metav1.LabelSelectorOpIn,
							Values:   []string{testCityCodeDFW, "ORD"},
						}},
					}
				},
			),
			expectedErrors: field.ErrorList{},
		},
		"location selector matching no ready location": {
			workload: MakeSandboxWorkload(
				"test",
				func(w *computev1alpha.Workload) {
					w.Spec.Placements[0].Locations = nil
					w.Spec.Placements[0].LocationSelector = &metav1.LabelSelector{
						MatchLabels: map[string]string{locationsv1alpha1.TopologyCityCodeKey: "LHR"},
					}
				},
			),
			expectedErrors: field.ErrorList{
				field.Invalid(field.NewPath("spec.placements[0].locationSelector"), "", ""),
			},
		},
		"empty location selector": {
			workload: MakeSandboxWorkload(
				"test",
				func(w *computev1alpha.Workload) {
					w.Spec.Placements[0].Locations = nil
					w.Spec.Placements[0].LocationSelector = &metav1.LabelSelector{}
				},
			),
			expectedErrors: field.ErrorList{
				field.Required(field.NewPath("spec.placements[0].locationSelector"), ""),
			},
		},
		"malformed location selector": {
			workload: MakeSandboxWorkload(
				"test",
				func(w *computev1alpha.Workload) {
					w.Spec.Placements[0].Locations = nil
					w.Spec.Placements[0].LocationSelector = &metav1.LabelSelector{
						MatchExpressions: []metav1.LabelSelectorRequirement{{
							Key:      locationsv1alpha1.TopologyCityCodeKey,
							Operator: metav1.LabelSelectorOpIn,
						}},
					}
				},
			),
			expectedErrors: field.ErrorList{
				field.Required(field.NewPath("spec.placements[0].locationSelector.matchExpressions[0].values"), ""),
			},
		},
		"city codes are deprecated": {
			workload: MakeSandboxWorkload(
				"test",
				func(w *computev1alpha.Workload) {
					w.Spec.Placements[0].CityCodes = []string{testCityCodeDFW}
				},
			),
			expectedErrors: field.ErrorList{
				field.Forbidden(field.NewPath("spec.placements[0].cityCodes"), ""),
			},
		},
		"location selector together with locations": {
			workload: MakeSandboxWorkload(
				"test",
				func(w *computev1alpha.Workload) {
					w.Spec.Placements[0].LocationSelector = &metav1.LabelSelector{
						MatchLabels: map[string]string{locationsv1alpha1.TopologyCityCodeKey: testCityCodeDFW},
					}
				},
			),
			expectedErrors: field.ErrorList{
				field.Forbidden(field.NewPath("spec.placements[0].locationSelector"), ""),
			},
		},
		"missing location": {
			workload: MakeSandboxWorkload(
				"test",
				func(w *computev1alpha.Workload) {
					w.Spec.Placements[0].Locations = []locationsv1alpha1.LocationReference{}
				},
			),
			expectedErrors: field.ErrorList{
				field.Required(field.NewPath("spec.placements[0].locations"), ""),
			},
		},
		"invalid location": {
			workload: MakeSandboxWorkload(
				"test",
				func(w *computev1alpha.Workload) {
					w.Spec.Placements[0].Locations = []locationsv1alpha1.LocationReference{{Name: "TEST"}}
				},
			),
			expectedErrors: field.ErrorList{
				field.NotSupported(field.NewPath("spec.placements[0].locations[0].name"), "TEST", []string{}),
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
								Name: testCPUResource,
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
								Name: testCPUResource,
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
								Name: testCPUResource,
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
								Name: testCPUResource,
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
							Name: testVolName,
						},
					)
					w.Spec.Template.Spec.Volumes = append(w.Spec.Template.Spec.Volumes, computev1alpha.InstanceVolume{
						Name: testVolName,
						VolumeSource: computev1alpha.VolumeSource{
							Disk: &computev1alpha.DiskTemplateVolumeSource{
								Template: &computev1alpha.DiskTemplateVolumeSourceTemplate{
									Spec: computev1alpha.DiskSpec{
										Type: diskTypePDStandard,
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
							Name: testVolName,
						},
					)
					w.Spec.Template.Spec.Volumes = append(w.Spec.Template.Spec.Volumes, computev1alpha.InstanceVolume{
						Name: testVolName,
						VolumeSource: computev1alpha.VolumeSource{
							Disk: &computev1alpha.DiskTemplateVolumeSource{
								Template: &computev1alpha.DiskTemplateVolumeSourceTemplate{
									Spec: computev1alpha.DiskSpec{
										Type: diskTypePDStandard,
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
							Name: testVolName,
						},
					)
					w.Spec.Template.Spec.Volumes = append(w.Spec.Template.Spec.Volumes, computev1alpha.InstanceVolume{
						Name: testVolName,
						VolumeSource: computev1alpha.VolumeSource{
							Disk: &computev1alpha.DiskTemplateVolumeSource{
								Template: &computev1alpha.DiskTemplateVolumeSourceTemplate{
									Spec: computev1alpha.DiskSpec{
										Type: diskTypePDStandard,
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
									Type: diskTypePDStandard,
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
									Type: diskTypePDStandard,
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
							Name:      testDuplicateMountPath,
							MountPath: proto.String("/mount1"),
						},
						{
							Name:      testDuplicateMountPath,
							MountPath: proto.String("/mount1"),
						},
						{
							Name: "missing-volume",
						},
					}
					w.Spec.Template.Spec.Volumes = []computev1alpha.InstanceVolume{
						{
							Name:         testDuplicateMountPath,
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
		"bare image name has no registry": {
			workload: MakeSandboxWorkload(
				"test",
				func(w *computev1alpha.Workload) {
					w.Spec.Template.Spec.Runtime.Sandbox.Containers[0].Image = testImageNoRegistry
				},
			),
			expectedErrors: field.ErrorList{
				field.Invalid(field.NewPath("spec.template.spec.runtime.sandbox.containers[0].image"), "", ""),
			},
		},
		"namespaced image name has no registry": {
			workload: MakeSandboxWorkload(
				"test",
				func(w *computev1alpha.Workload) {
					w.Spec.Template.Spec.Runtime.Sandbox.Containers[0].Image = "myname/myapp:v1"
				},
			),
			expectedErrors: field.ErrorList{
				field.Invalid(field.NewPath("spec.template.spec.runtime.sandbox.containers[0].image"), "", ""),
			},
		},
		"qualified image name with no tag is valid": {
			workload: MakeSandboxWorkload(
				"test",
				func(w *computev1alpha.Workload) {
					w.Spec.Template.Spec.Runtime.Sandbox.Containers[0].Image = "ghcr.io/acme/api"
				},
			),
			expectedErrors: field.ErrorList{},
		},
		"localhost image name is valid": {
			workload: MakeSandboxWorkload(
				"test",
				func(w *computev1alpha.Workload) {
					w.Spec.Template.Spec.Runtime.Sandbox.Containers[0].Image = "localhost:5000/foo:bar"
				},
			),
			expectedErrors: field.ErrorList{},
		},
		"invalid image reference syntax": {
			workload: MakeSandboxWorkload(
				"test",
				func(w *computev1alpha.Workload) {
					w.Spec.Template.Spec.Runtime.Sandbox.Containers[0].Image = "ghcr.io/acme/UPPERCASE_REPO:v1"
				},
			),
			expectedErrors: field.ErrorList{
				field.Invalid(field.NewPath("spec.template.spec.runtime.sandbox.containers[0].image"), "", ""),
			},
		},
		"network use denied": {
			workload: MakeSandboxWorkload("test"),
			interceptorFuncs: &interceptor.Funcs{
				Create: func(ctx context.Context, client client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
					if sar, ok := obj.(*authorizationv1.SubjectAccessReview); ok {
						if sar.Spec.ResourceAttributes.Name == testDefaultNamespace &&
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
				Namespace: testDefaultNamespace,
				Name:      testDefaultNamespace,
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

		if len(scenario.opts.ValidLocations) == 0 {
			scenario.opts.ValidLocations = []string{testCityCodeDFW}
		}
		if scenario.opts.LocationTopologies == nil {
			scenario.opts.LocationTopologies = map[string]map[string]string{
				testCityCodeDFW: {locationsv1alpha1.TopologyCityCodeKey: testCityCodeDFW},
			}
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
								Name: testDefaultNamespace,
							},
						},
					},
					Runtime: computev1alpha.InstanceRuntimeSpec{
						Resources: computev1alpha.InstanceRuntimeResources{
							InstanceType: defaultInstanceType,
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
					Locations: []locationsv1alpha1.LocationReference{{Name: testCityCodeDFW}},
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
								Name: testDefaultNamespace,
							},
						},
					},
					Runtime: computev1alpha.InstanceRuntimeSpec{
						Resources: computev1alpha.InstanceRuntimeResources{
							InstanceType: defaultInstanceType,
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
											Type: diskTypePDStandard,
											Populator: &computev1alpha.DiskPopulator{
												Image: &computev1alpha.ImageDiskPopulator{
													Name: defaultImageName,
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
					Locations: []locationsv1alpha1.LocationReference{{Name: testCityCodeDFW}},
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

// TestValidateWorkloadSpecUpdate_RuntimeClassImmutable verifies that a workload
// cannot move between execution tiers in place. The only permitted transition
// fills in an absent class with the class the catalog marks as default, which
// is what the mutating webhook stamps. The default comes from the catalog, not
// from a name this package knows.
func TestValidateWorkloadSpecUpdate_RuntimeClassImmutable(t *testing.T) {
	classPath := field.NewPath("spec", "template", "spec", "runtime", "class")

	withClass := func(class string) computev1alpha.WorkloadSpec {
		return MakeSandboxWorkload("test", func(w *computev1alpha.Workload) {
			w.Spec.Template.Spec.Runtime.Class = class
		}).Spec
	}

	// undefaultedCatalog publishes tiers but marks none of them as default, so
	// no class can be filled in on an existing workload.
	undefaultedCatalog := runtimeclass.Catalog{
		makeRuntimeClass(testClassAzurite),
		makeRuntimeClass(testClassBasalt),
	}

	cases := map[string]struct {
		oldClass       string
		class          string
		catalog        runtimeclass.Catalog
		expectedErrors field.ErrorList
	}{
		"unchanged default": {
			oldClass: testClassAzurite,
			class:    testClassAzurite,
			catalog:  defaultCatalog(),
		},
		"unchanged non-default": {
			oldClass: testClassBasalt,
			class:    testClassBasalt,
			catalog:  defaultCatalog(),
		},
		"still unset": {catalog: defaultCatalog()},
		"unset gets defaulted": {
			class:   testClassAzurite,
			catalog: defaultCatalog(),
		},
		"unset jumps straight to a non-default tier": {
			class:   testClassBasalt,
			catalog: defaultCatalog(),
			expectedErrors: field.ErrorList{
				field.Forbidden(classPath, ""),
			},
		},
		"unset filled in where the catalog marks no default": {
			class:   testClassAzurite,
			catalog: undefaultedCatalog,
			expectedErrors: field.ErrorList{
				field.Forbidden(classPath, ""),
			},
		},
		"unset filled in with no catalog read at all": {
			class: testClassAzurite,
			expectedErrors: field.ErrorList{
				field.Forbidden(classPath, ""),
			},
		},
		"changed tier": {
			oldClass: testClassAzurite,
			class:    testClassBasalt,
			catalog:  defaultCatalog(),
			expectedErrors: field.ErrorList{
				field.Invalid(classPath, "", ""),
			},
		},
		"cleared": {
			oldClass: testClassBasalt,
			catalog:  defaultCatalog(),
			expectedErrors: field.ErrorList{
				field.Invalid(classPath, "", ""),
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			errs := validateWorkloadSpecUpdate(
				withClass(tc.class), withClass(tc.oldClass), field.NewPath("spec"),
				WorkloadValidationOptions{RuntimeClasses: tc.catalog},
			)

			delta := cmp.Diff(
				tc.expectedErrors, errs,
				cmpopts.IgnoreFields(field.Error{}, "BadValue", "Detail"),
				cmpopts.EquateEmpty(),
			)
			if delta != "" {
				t.Errorf("errors mismatch (-want +got):\n%s", delta)
			}
		})
	}
}

// TestValidateWorkloadSpecUpdate_RuntimeClassNamesTheDefault verifies that the
// rejection message names the class that may be filled in, read from the
// catalog.
func TestValidateWorkloadSpecUpdate_RuntimeClassNamesTheDefault(t *testing.T) {
	spec := MakeSandboxWorkload("test", func(w *computev1alpha.Workload) {
		w.Spec.Template.Spec.Runtime.Class = testClassBasalt
	}).Spec
	oldSpec := MakeSandboxWorkload("test").Spec

	errs := validateWorkloadSpecUpdate(spec, oldSpec, field.NewPath("spec"),
		WorkloadValidationOptions{RuntimeClasses: defaultCatalog()})
	if len(errs) != 1 {
		t.Fatalf("expected the tier change to be refused once, got: %v", errs)
	}
	if got := errs[0].Error(); !strings.Contains(got, `"`+testClassAzurite+`"`) {
		t.Errorf("refusal should name the class the workload already runs in, got: %s", got)
	}
}

// sandboxWorkload builds a minimal workload with the given sandbox containers,
// enough for validateWorkloadImages, which only reads spec.template.spec.runtime.sandbox.
func sandboxWorkload(containers ...computev1alpha.SandboxContainer) *computev1alpha.Workload {
	w := &computev1alpha.Workload{}
	w.Spec.Template.Spec.Runtime.Sandbox = &computev1alpha.SandboxRuntime{Containers: containers}
	return w
}

func TestValidateWorkloadImages(t *testing.T) {
	containersPath := field.NewPath("spec", "template", "spec", "runtime", "sandbox", "containers")

	cases := map[string]struct {
		workload       *computev1alpha.Workload
		oldWorkload    *computev1alpha.Workload
		expectedErrors field.ErrorList
	}{
		"create: image with no registry is rejected": {
			workload: sandboxWorkload(computev1alpha.SandboxContainer{Name: "c1", Image: testImageNoRegistry}),
			expectedErrors: field.ErrorList{
				field.Invalid(containersPath.Index(0).Child("image"), "", ""),
			},
		},
		"create: empty image is left to validateContainerCommon": {
			workload: sandboxWorkload(computev1alpha.SandboxContainer{Name: "c1"}),
		},
		"update: new container with empty image is left to validateContainerCommon": {
			workload:    sandboxWorkload(computev1alpha.SandboxContainer{Name: "c1"}),
			oldWorkload: sandboxWorkload(computev1alpha.SandboxContainer{Name: "c2", Image: testImageQualified}),
		},
		"update: unchanged bad image is not re-validated": {
			workload:    sandboxWorkload(computev1alpha.SandboxContainer{Name: "c1", Image: testImageNoRegistry}),
			oldWorkload: sandboxWorkload(computev1alpha.SandboxContainer{Name: "c1", Image: testImageNoRegistry}),
		},
		"update: rewriting to a different, still-bad image is rejected": {
			workload:    sandboxWorkload(computev1alpha.SandboxContainer{Name: "c1", Image: "other:latest"}),
			oldWorkload: sandboxWorkload(computev1alpha.SandboxContainer{Name: "c1", Image: testImageNoRegistry}),
			expectedErrors: field.ErrorList{
				field.Invalid(containersPath.Index(0).Child("image"), "", ""),
			},
		},
		"update: fixing the image is fine": {
			workload:    sandboxWorkload(computev1alpha.SandboxContainer{Name: "c1", Image: testImageQualified}),
			oldWorkload: sandboxWorkload(computev1alpha.SandboxContainer{Name: "c1", Image: testImageNoRegistry}),
		},
		"update: a container added alongside an unchanged one is still validated": {
			workload: sandboxWorkload(
				computev1alpha.SandboxContainer{Name: "c1", Image: testImageNoRegistry},
				computev1alpha.SandboxContainer{Name: "c2", Image: testImageNoRegistry},
			),
			oldWorkload: sandboxWorkload(computev1alpha.SandboxContainer{Name: "c1", Image: testImageNoRegistry}),
			expectedErrors: field.ErrorList{
				field.Invalid(containersPath.Index(1).Child("image"), "", ""),
			},
		},
		"update: renaming a container makes it a new entry, so its image is validated": {
			workload:    sandboxWorkload(computev1alpha.SandboxContainer{Name: "c2", Image: testImageNoRegistry}),
			oldWorkload: sandboxWorkload(computev1alpha.SandboxContainer{Name: "c1", Image: testImageNoRegistry}),
			expectedErrors: field.ErrorList{
				field.Invalid(containersPath.Index(0).Child("image"), "", ""),
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cmpErrs(t, tc.expectedErrors, validateWorkloadImages(tc.workload, tc.oldWorkload))
		})
	}
}

// TestValidateWorkloadUpdate_UnchangedImage exercises validateWorkloadImages
// through the real entry point, ValidateWorkloadUpdate, mirroring
// workload_controller.go's finalizer-only Update: the same spec, unqualified
// image included, written back verbatim.
func TestValidateWorkloadUpdate_UnchangedImage(t *testing.T) {
	scheme := k8sruntime.NewScheme()
	utilruntime.Must(computev1alpha.AddToScheme(scheme))
	utilruntime.Must(networkingv1alpha.AddToScheme(scheme))
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithInterceptorFuncs(interceptor.Funcs{
			Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				if sar, ok := obj.(*authorizationv1.SubjectAccessReview); ok {
					sar.GenerateName = "sar-"
					sar.Status.Allowed = true
				}
				return c.Create(ctx, obj, opts...)
			},
		}).
		WithObjects(&networkingv1alpha.Network{
			ObjectMeta: metav1.ObjectMeta{Namespace: testDefaultNamespace, Name: testDefaultNamespace},
		}).
		Build()

	oldWorkload := MakeSandboxWorkload("test", func(w *computev1alpha.Workload) {
		w.Spec.Template.Spec.Runtime.Sandbox.Containers[0].Image = testImageNoRegistry
	})
	opts := WorkloadValidationOptions{
		Client:         fakeClient,
		Context:        context.Background(),
		ValidLocations: []string{testCityCodeDFW},
	}

	t.Run("finalizer-style update leaving the image untouched is not rejected", func(t *testing.T) {
		newWorkload := oldWorkload.DeepCopy()
		o := opts
		o.Workload = newWorkload
		errs := ValidateWorkloadUpdate(newWorkload, oldWorkload, o)
		if len(errs) != 0 {
			t.Errorf("expected no errors, got: %v", errs)
		}
	})

	t.Run("rewriting the image to a still-unqualified value is rejected", func(t *testing.T) {
		newWorkload := oldWorkload.DeepCopy()
		newWorkload.Spec.Template.Spec.Runtime.Sandbox.Containers[0].Image = "other:latest"
		o := opts
		o.Workload = newWorkload
		errs := ValidateWorkloadUpdate(newWorkload, oldWorkload, o)
		wantErrs := field.ErrorList{
			field.Invalid(field.NewPath("spec.template.spec.runtime.sandbox.containers[0].image"), "", ""),
		}
		cmpErrs(t, wantErrs, errs)
	})
}
