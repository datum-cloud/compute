package validation

import (
	"fmt"
	"path"
	"strings"

	"golang.org/x/crypto/ssh"
	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	apimachineryvalidation "k8s.io/apimachinery/pkg/api/validation"
	"k8s.io/apimachinery/pkg/util/sets"
	apimachineryutilvalidation "k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/referenceddata"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
)

// Validation constants for well-known string literals used across multiple
// validation functions.
const (
	// diskTypePDStandard is the only currently supported disk type.
	diskTypePDStandard = "pd-standard"

	// defaultImageName is the only currently supported container image.
	defaultImageName = "datumcloud/ubuntu-2204-lts"

	// defaultInstanceType is the only currently supported instance type.
	defaultInstanceType = "datumcloud/d1-standard-2"
)

func validateInstanceTemplate(
	template computev1alpha.InstanceTemplateSpec,
	fieldPath *field.Path,
	opts WorkloadValidationOptions,
) field.ErrorList {
	allErrs := field.ErrorList{}

	allErrs = append(allErrs, validateInstanceTemplateMetadata(template, fieldPath)...)
	allErrs = append(allErrs, validateInstanceSpec(template.Spec, fieldPath.Child("spec"), opts)...)

	return allErrs
}

func validateInstanceTemplateMetadata(template computev1alpha.InstanceTemplateSpec, fieldPath *field.Path) field.ErrorList {
	annotationsPath := fieldPath.Child("annotations")
	allErrs := field.ErrorList{}

	if template.Spec.Runtime.VirtualMachine != nil {
		sshAnnotationField := annotationsPath.Key(computev1alpha.SSHKeysAnnotation)
		// VMs require an SSH key to be provided in annotations right now
		if keys, ok := template.Annotations[computev1alpha.SSHKeysAnnotation]; !ok {
			allErrs = append(allErrs, field.Required(sshAnnotationField, ""))
		} else {
			for i, k := range strings.Split(strings.TrimSpace(keys), "\n") {
				keyField := sshAnnotationField.Index(i)

				parts := strings.SplitN(k, ":", 2)
				if len(parts) != 2 {
					allErrs = append(allErrs, field.Invalid(keyField, k, "must be in the format 'username:key"))
				} else {
					if len(parts[0]) == 0 {
						allErrs = append(allErrs, field.Required(keyField, "must provide a username"))
					}
					if _, _, _, _, err := ssh.ParseAuthorizedKey([]byte(parts[1])); err != nil {
						allErrs = append(allErrs, field.Invalid(keyField, parts[1], "must be a valid SSH public key"))
					}
				}
			}
		}
	}

	return allErrs
}

func validateInstanceSpec(
	spec computev1alpha.InstanceSpec,
	fieldPath *field.Path,
	opts WorkloadValidationOptions,
) field.ErrorList {
	allErrs := field.ErrorList{}

	volumes, volumeErrs := validateVolumes(spec, fieldPath)
	allErrs = append(allErrs, volumeErrs...)

	allErrs = append(allErrs, validateInstanceRuntimeSpec(spec.Runtime, volumes, fieldPath.Child("runtime"))...)
	allErrs = append(allErrs, validateInstanceNetworkInterfaces(spec.NetworkInterfaces, fieldPath.Child("networkInterfaces"), opts)...)
	allErrs = append(allErrs, validateReferencedDataAccess(spec, fieldPath, opts)...)

	return allErrs
}

// validateReferencedDataAccess issues a SubjectAccessReview for each unique
// ConfigMap and Secret referenced by the spec (volumes, env valueFrom, envFrom).
// The check mirrors the existing Network interface SAR pattern exactly —
// build from AdmissionRequest.UserInfo, deny if not allowed.
//
// References are collected and deduplicated by referenceddata.CollectFromSpec,
// which is the single source of truth for "what counts as a reference". Entries
// with both configMapRef and secretRef set are skipped (validateEnvFrom rejects
// them separately, so no SAR is needed for invalid entries).
func validateReferencedDataAccess(
	spec computev1alpha.InstanceSpec,
	fieldPath *field.Path,
	opts WorkloadValidationOptions,
) field.ErrorList {
	allErrs := field.ErrorList{}

	refs := referenceddata.CollectFromSpec(opts.Workload.Namespace, spec)
	if len(refs) == 0 {
		return nil
	}

	extra := make(map[string]authorizationv1.ExtraValue, len(opts.AdmissionRequest.UserInfo.Extra))
	for k, v := range opts.AdmissionRequest.UserInfo.Extra {
		extra[k] = authorizationv1.ExtraValue(v)
	}

	for _, ref := range refs {
		var resourceName string
		switch ref.Kind {
		case "ConfigMap":
			resourceName = "configmaps"
		case "Secret":
			resourceName = "secrets"
		default:
			continue
		}

		review := authorizationv1.SubjectAccessReview{
			Spec: authorizationv1.SubjectAccessReviewSpec{
				ResourceAttributes: &authorizationv1.ResourceAttributes{
					Verb:      "get",
					Group:     "",
					Version:   "v1",
					Resource:  resourceName,
					Name:      ref.Name,
					Namespace: opts.Workload.Namespace,
				},
				User:   opts.AdmissionRequest.UserInfo.Username,
				Groups: opts.AdmissionRequest.UserInfo.Groups,
				UID:    opts.AdmissionRequest.UserInfo.UID,
				Extra:  extra,
			},
		}

		refPath := fieldPath.Child(resourceName).Key(ref.Name)
		if err := opts.Client.Create(opts.Context, &review); err != nil {
			allErrs = append(allErrs, field.InternalError(refPath,
				fmt.Errorf("failed creating SubjectAccessReview for %s %s/%s access: %w",
					ref.Kind, opts.Workload.Namespace, ref.Name, err)))
		} else if !review.Status.Allowed {
			allErrs = append(allErrs, field.Forbidden(refPath,
				fmt.Sprintf("permission to get %s %q was denied", ref.Kind, ref.Name)))
		}
	}

	return allErrs
}

func validateInstanceNetworkInterfaces(
	networkInterfaces []computev1alpha.InstanceNetworkInterface,
	fieldPath *field.Path,
	opts WorkloadValidationOptions,
) field.ErrorList {
	allErrs := field.ErrorList{}

	if len(networkInterfaces) == 0 {
		allErrs = append(allErrs, field.Required(fieldPath, "must define at least one network interface"))
	}

	for i, networkInterface := range networkInterfaces {
		indexPath := fieldPath.Index(i)

		networkField := indexPath.Child("network")
		networkNameField := networkField.Child("name")
		for _, msg := range apimachineryvalidation.NameIsDNSLabel(networkInterface.Network.Name, false) {
			allErrs = append(allErrs, field.Invalid(networkNameField, networkInterface.Network, msg))
		}

		extra := make(map[string]authorizationv1.ExtraValue, len(opts.AdmissionRequest.UserInfo.Extra))
		for k, v := range opts.AdmissionRequest.UserInfo.Extra {
			extra[k] = authorizationv1.ExtraValue(v)
		}

		review := authorizationv1.SubjectAccessReview{
			Spec: authorizationv1.SubjectAccessReviewSpec{
				ResourceAttributes: &authorizationv1.ResourceAttributes{
					Verb:      "use",
					Group:     networkingv1alpha.GroupVersion.Group,
					Version:   networkingv1alpha.GroupVersion.Version,
					Resource:  "networks",
					Name:      networkInterface.Network.Name,
					Namespace: opts.Workload.Namespace,
				},
				User:   opts.AdmissionRequest.UserInfo.Username,
				Groups: opts.AdmissionRequest.UserInfo.Groups,
				UID:    opts.AdmissionRequest.UserInfo.UID,
				Extra:  extra,
			},
		}

		if err := opts.Client.Create(opts.Context, &review); err != nil {
			allErrs = append(allErrs, field.InternalError(networkField, fmt.Errorf("failed creating SubjectAccessReview for Network access: %w", err)))
		} else {
			if !review.Status.Allowed {
				allErrs = append(allErrs, field.Forbidden(networkField, "permission to use the network was denied"))
			}
		}

		// TODO(jreese) validate network policies
	}

	// TODO(jreese) validate no overlap in subnets that the interface will qualify
	// for.
	// See https://cloud.google.com/vpc/docs/create-use-multiple-interfaces
	// See https://cloud.google.com/compute/docs/reference/rest/v1/instances/insert
	//	- docs on networkInterfaces[].network

	return allErrs
}

func validateVolumes(spec computev1alpha.InstanceSpec, fieldPath *field.Path) (map[string]computev1alpha.VolumeSource, field.ErrorList) {
	allErrs := field.ErrorList{}
	allNames := sets.Set[string]{}
	volumeAttachments := sets.Set[string]{}

	if spec.Runtime.Sandbox != nil {
		for _, c := range spec.Runtime.Sandbox.Containers {
			for _, a := range c.VolumeAttachments {
				volumeAttachments.Insert(a.Name)
			}
		}
	}

	if spec.Runtime.VirtualMachine != nil {
		for _, a := range spec.Runtime.VirtualMachine.VolumeAttachments {
			volumeAttachments.Insert(a.Name)
		}
	}

	deviceNames := sets.Set[string]{}

	volumeMap := map[string]computev1alpha.VolumeSource{}
	volumesFieldPath := fieldPath.Child("volumes")

	for i, volume := range spec.Volumes {
		indexPath := volumesFieldPath.Index(i)
		nameField := indexPath.Child("name")

		deviceName := fmt.Sprintf("persistent-disk-%d", i)
		if volume.Disk != nil && volume.Disk.DeviceName != nil {
			deviceName = *volume.Disk.DeviceName
		}

		if deviceNames.Has(deviceName) {
			allErrs = append(allErrs, field.Duplicate(indexPath.Child("disk.deviceName"), deviceName))
		} else {
			deviceNames.Insert(deviceName)
		}

		allErrs = append(allErrs, validateVolumeSource(volume.VolumeSource, indexPath)...)

		if len(volume.Name) == 0 {
			allErrs = append(allErrs, field.Required(nameField, ""))
		} else {
			for _, msg := range apimachineryvalidation.NameIsDNSLabel(volume.Name, false) {
				allErrs = append(allErrs, field.Invalid(nameField, volume.Name, msg))
			}
		}

		if allNames.Has(volume.Name) {
			allErrs = append(allErrs, field.Duplicate(nameField, volume.Name))
		} else {
			allNames.Insert(volume.Name)
			volumeMap[volume.Name] = volume.VolumeSource

			if !volumeAttachments.Has(volume.Name) {
				allErrs = append(allErrs, field.Required(nameField, "volume must be attached at least 1 time"))
			}
		}

	}

	return volumeMap, allErrs
}

func validateVolumeSource(source computev1alpha.VolumeSource, fieldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}

	numSources := 0

	if source.Disk != nil {
		diskField := fieldPath.Child("disk")
		if numSources > 0 {
			allErrs = append(allErrs, field.Forbidden(diskField, "may not specify more than 1 volume source"))
		} else {
			numSources++
			allErrs = append(allErrs, validateDiskVolumeSource(source.Disk, diskField)...)
		}
	}

	if source.ConfigMap != nil {
		configMapField := fieldPath.Child("configMap")
		if numSources > 0 {
			allErrs = append(allErrs, field.Forbidden(configMapField, "may not specify more than 1 volume source"))
		} else {
			numSources++
			allErrs = append(allErrs, validateConfigMapVolumeSource(source.ConfigMap, configMapField)...)
		}
	}

	if source.Secret != nil {
		secretField := fieldPath.Child("secret")
		if numSources > 0 {
			allErrs = append(allErrs, field.Forbidden(secretField, "may not specify more than 1 volume source"))
		} else {
			numSources++
			allErrs = append(allErrs, validateSecretVolumeSource(source.Secret, secretField)...)
		}
	}

	if numSources == 0 {
		allErrs = append(allErrs, field.Required(fieldPath, "must specify a volume source"))
	}

	return allErrs
}

func validateDiskVolumeSource(diskSource *computev1alpha.DiskTemplateVolumeSource, fieldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}

	// TODO(jreese) validate device name

	diskTemplateField := fieldPath.Child("template")
	if diskSource.Template == nil {
		// In the future, we may permit defining a disk backed volume via other
		// methods, such as a name or selector
		allErrs = append(allErrs, field.Required(diskTemplateField, ""))
		return allErrs
	}

	// TODO(jreese) validate disk template metadata
	diskTemplate := diskSource.Template
	diskTemplateSpecField := diskTemplateField.Child("spec")

	// TODO(jrese) look up valid disk types
	if diskTemplate.Spec.Type != diskTypePDStandard {
		allErrs = append(allErrs, field.NotSupported(diskTemplateSpecField.Child("type"), diskTemplate.Spec.Type, []string{diskTypePDStandard}))
	}

	populatorResourceRequests, errs := validateDiskPopulator(diskTemplate.Spec.Populator, diskTemplateField.Child("populator"))
	if len(errs) > 0 {
		allErrs = append(allErrs, errs...)
	}

	// Some disk populators are capable of providing resource requests, such as
	// an image populator.
	//
	// A filesystem populator will result in a filesystem being laid out on the
	// device.
	//
	// No populator will result in raw blocks being provisioned, and may only be
	// attached as a device.

	// If no resources are provided, a populator that comes with size metadata
	// must be provided, such as an image populator.
	resourcesField := diskTemplateSpecField.Child("resources")
	if populatorResourceRequests == nil {
		if diskTemplate.Spec.Resources == nil {
			allErrs = append(allErrs, field.Required(resourcesField, "volume resource requests are required when not provided by populator"))
		} else {
			allErrs = append(allErrs, validateDiskResourceRequirements(diskTemplate.Spec.Resources, resourcesField)...)
		}
	} else {
		if diskTemplate.Spec.Resources != nil {
			if errs := validateDiskResourceRequirements(diskTemplate.Spec.Resources, resourcesField); len(errs) > 0 {
				allErrs = append(allErrs, errs...)
			} else if storageRequest, ok := diskTemplate.Spec.Resources.Requests[corev1.ResourceStorage]; ok {
				// Resource requests are valid, make sure they at least match what the
				// populator needs.
				if populatorRequest, ok := populatorResourceRequests[corev1.ResourceStorage]; !ok {
					allErrs = append(allErrs, field.InternalError(diskTemplateSpecField, fmt.Errorf("populator did not provide storage requests")))
				} else if storageRequest.Cmp(populatorRequest) == -1 {
					storageField := resourcesField.Child("requests").Key(string(corev1.ResourceStorage))

					allErrs = append(allErrs, field.Invalid(storageField, storageRequest.String(), fmt.Sprintf("must be greater than or equal to %s", populatorRequest.String())))
				}
			}
		}
	}
	return allErrs
}

var fileModeErrorMsg = "must be a number between 0 and 0777 (octal), both inclusive"

func validateConfigMapVolumeSource(configMapSource *corev1.ConfigMapVolumeSource, fldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}
	if len(configMapSource.Name) == 0 {
		allErrs = append(allErrs, field.Required(fldPath.Child("name"), ""))
	}

	configMapMode := configMapSource.DefaultMode
	if configMapMode != nil && (*configMapMode > 0777 || *configMapMode < 0) {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("defaultMode"), *configMapMode, fileModeErrorMsg))
	}

	for i, kp := range configMapSource.Items {
		itemPath := fldPath.Child("items").Index(i)
		allErrs = append(allErrs, validateKeyToPath(&kp, itemPath)...)
	}
	return allErrs
}

func validateSecretVolumeSource(secretSource *corev1.SecretVolumeSource, fldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}
	if len(secretSource.SecretName) == 0 {
		allErrs = append(allErrs, field.Required(fldPath.Child("secretName"), ""))
	}

	secretMode := secretSource.DefaultMode
	if secretMode != nil && (*secretMode > 0777 || *secretMode < 0) {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("defaultMode"), *secretMode, fileModeErrorMsg))
	}

	for i, kp := range secretSource.Items {
		itemPath := fldPath.Child("items").Index(i)
		allErrs = append(allErrs, validateKeyToPath(&kp, itemPath)...)
	}
	return allErrs
}

// validateKeyToPath validates a corev1.KeyToPath projection entry.
func validateKeyToPath(kp *corev1.KeyToPath, fldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}

	if len(kp.Key) == 0 {
		allErrs = append(allErrs, field.Required(fldPath.Child("key"), ""))
	}

	if len(kp.Path) == 0 {
		allErrs = append(allErrs, field.Required(fldPath.Child("path"), ""))
	} else {
		allErrs = append(allErrs, validateVolumeProjectionPath(kp.Path, fldPath.Child("path"))...)
	}

	if kp.Mode != nil && (*kp.Mode > 0777 || *kp.Mode < 0) {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("mode"), *kp.Mode, fileModeErrorMsg))
	}

	return allErrs
}

// validateVolumeProjectionPath validates that a volume key→path target path is
// safe: it must be relative, must not be absolute, and must not contain ".."
// path elements that would escape the volume mount directory.
func validateVolumeProjectionPath(p string, fldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}

	if path.IsAbs(p) {
		allErrs = append(allErrs, field.Invalid(fldPath, p, "must be a relative path"))
		return allErrs
	}

	// Clean the path and check for ".." escape.
	cleaned := path.Clean(p)
	if strings.HasPrefix(cleaned, "..") {
		allErrs = append(allErrs, field.Invalid(fldPath, p, "must not contain '..' path elements"))
	}

	return allErrs
}

const isNotPositiveErrorMsg string = `must be greater than zero`

// Validates that a Quantity is positive
//
// See: https://github.com/kubernetes/kubernetes/blob/f1e447b9d32ac325074380d239370cde02a6dbf7/pkg/apis/core/validation/validation.go#L352
func validatePositiveQuantityValue(value resource.Quantity, fldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}
	if value.Cmp(resource.Quantity{}) <= 0 {
		allErrs = append(allErrs, field.Invalid(fldPath, value.String(), isNotPositiveErrorMsg))
	}
	return allErrs
}

func validateDiskResourceRequirements(requirements *computev1alpha.DiskResourceRequirements, fieldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}

	if requirements.Requests == nil {
		allErrs = append(allErrs, field.Required(fieldPath.Child("requests"), ""))
	} else {
		storageField := fieldPath.Child("requests").Key(string(corev1.ResourceStorage))
		// Only support `storage` requests for now
		if storageRequest, ok := requirements.Requests[corev1.ResourceStorage]; !ok {
			allErrs = append(allErrs, field.Required(storageField, ""))
		} else if errs := validatePositiveQuantityValue(storageRequest, storageField); len(errs) > 0 {
			allErrs = append(allErrs, errs...)
		} else {
			// TODO(jreese) minimum storage size based on disk type
			if storageRequest.Cmp(resource.MustParse("10Gi")) == -1 {
				allErrs = append(allErrs, field.Invalid(storageField, storageRequest.String(), "storage requests must be at least 10Gi"))
			}

			// TODO(jreese) put limits on resource requests based on entitlements
			if storageRequest.Cmp(resource.MustParse("100Gi")) == 1 {
				allErrs = append(allErrs, field.Invalid(storageField, storageRequest.String(), "storage requests must not exceed 100Gi"))
			}

			if storageRequest.Value()%(1024*1024*1024) != 0 {
				allErrs = append(allErrs, field.Invalid(storageField, storageRequest.String(), "storage requests must be in increments of 1Gi"))
			}
		}
	}

	return allErrs
}

var supportedFilesystemTypes = sets.New("ext4")

func validateDiskPopulator(populator *computev1alpha.DiskPopulator, fieldPath *field.Path) (corev1.ResourceList, field.ErrorList) {
	allErrs := field.ErrorList{}

	if populator == nil {
		return nil, allErrs
	}

	var resourceRequests corev1.ResourceList

	numPopulators := 0

	if populator.Image != nil {
		imageField := fieldPath.Child("image")
		if numPopulators > 0 {
			allErrs = append(allErrs, field.Forbidden(imageField, "may not specify more than 1 disk populator"))
		} else {
			// TODO(jreese) get requests from image metadata
			resourceRequests = corev1.ResourceList{
				corev1.ResourceStorage: resource.MustParse("25Gi"),
			}

			// TODO(jreese) look up image
			imagePopulator := populator.Image
			if imagePopulator.Name != defaultImageName {
				allErrs = append(allErrs, field.NotSupported(imageField.Child("name"), imagePopulator.Name, []string{defaultImageName}))
			}
		}
	}

	if populator.Filesystem != nil {
		fsField := fieldPath.Child("filesystem")
		if numPopulators > 0 {
			allErrs = append(allErrs, field.Forbidden(fsField, "may not specify more than 1 disk populator"))
		} else if !supportedFilesystemTypes.Has(populator.Filesystem.Type) {
			allErrs = append(allErrs, field.NotSupported(fsField.Child("type"), populator.Filesystem.Type, sets.List(supportedFilesystemTypes)))
		}
	}

	return resourceRequests, allErrs
}

func validateInstanceRuntimeSpec(spec computev1alpha.InstanceRuntimeSpec, volumes map[string]computev1alpha.VolumeSource, fieldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}

	allErrs = append(allErrs, validateInstanceRuntimeResources(spec.Resources, fieldPath.Child("resources"))...)

	numRuntimes := 0

	if spec.Sandbox != nil {
		sandboxField := fieldPath.Child("sandbox")
		// Checking numRuntimes even though this is the first check in case code is
		// reorganized.
		if numRuntimes > 0 {
			allErrs = append(allErrs, field.Forbidden(sandboxField, "may not specify more than 1 runtime type"))
		} else {
			numRuntimes++
			allErrs = append(allErrs, validateSandboxRuntime(spec.Sandbox, volumes, sandboxField)...)
		}
	}

	if spec.VirtualMachine != nil {
		vmField := fieldPath.Child("virtualMachine")
		// Checking numRuntimes even though this is the first check in case code is
		// reorganized.
		if numRuntimes > 0 {
			allErrs = append(allErrs, field.Forbidden(vmField, "may not specify more than 1 runtime type"))
		} else {
			numRuntimes++
			allErrs = append(allErrs, validateVirtualMachineRuntime(spec.VirtualMachine, volumes, vmField)...)
		}
	}

	if numRuntimes == 0 {
		allErrs = append(allErrs, field.Required(fieldPath, "must specify a runtime type"))
	}

	return allErrs
}

func validateSandboxRuntime(sandbox *computev1alpha.SandboxRuntime, volumes map[string]computev1alpha.VolumeSource, fieldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}

	allErrs = append(allErrs, validateSandboxContainers(sandbox.Containers, volumes, fieldPath.Child("containers"))...)
	allErrs = append(allErrs, validateImagePullSecrets(sandbox.ImagePullSecrets, fieldPath.Child("imagePullSecrets"))...)

	return allErrs
}

func validateImagePullSecrets(imagePullSecrets []computev1alpha.LocalSecretReference, fieldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}

	for i, s := range imagePullSecrets {
		if len(s.Name) == 0 {
			allErrs = append(allErrs, field.Required(fieldPath.Index(i).Child("name"), ""))
		}
	}

	return allErrs
}

func validateSandboxContainers(
	containers []computev1alpha.SandboxContainer,
	volumes map[string]computev1alpha.VolumeSource,
	fieldPath *field.Path,
) field.ErrorList {
	allErrs := field.ErrorList{}

	if len(containers) == 0 {
		return append(allErrs, field.Required(fieldPath, "must define at least 1 container"))
	}

	allNames := sets.Set[string]{}
	for i, c := range containers {
		indexPath := fieldPath.Index(i)
		allErrs = append(allErrs, validateContainerCommon(c, volumes, indexPath)...)

		if allNames.Has(c.Name) {
			allErrs = append(allErrs, field.Duplicate(indexPath.Child("name"), c.Name))
		} else {
			allNames.Insert(c.Name)
		}
	}

	return allErrs
}

func validateContainerCommon(
	container computev1alpha.SandboxContainer,
	volumes map[string]computev1alpha.VolumeSource,
	fieldPath *field.Path,
) field.ErrorList {
	allErrs := field.ErrorList{}

	nameField := fieldPath.Child("name")
	if len(container.Name) == 0 {
		allErrs = append(allErrs, field.Required(nameField, ""))
	} else {
		for _, msg := range apimachineryvalidation.NameIsDNSLabel(container.Name, false) {
			allErrs = append(allErrs, field.Invalid(nameField, container.Name, msg))
		}
	}

	if len(container.Image) == 0 {
		allErrs = append(allErrs, field.Required(fieldPath.Child("image"), ""))

		// TODO(jreese) validate container image name, ensure it's fully qualified
	}

	if container.Resources != nil {
		// TODO(jreese) validate resource requirements
		// https://github.com/kubernetes/kubernetes/blob/f1e447b9d32ac325074380d239370cde02a6dbf7/pkg/apis/core/validation/validation.go#L6699
		allErrs = append(allErrs, field.Forbidden(fieldPath.Child("resources"), "not implemented"))
	}

	allErrs = append(allErrs, validateVolumeAttachments(container.VolumeAttachments, volumes, fieldPath.Child("volumeAttachments"))...)

	allErrs = append(allErrs, validateEnvFrom(container.EnvFrom, fieldPath.Child("envFrom"))...)

	// TODO(jreese) validate named ports are unique across all containers?
	allErrs = append(allErrs, validateNamedPorts(container.Ports, fieldPath.Child("ports"))...)

	return allErrs
}

// validateEnvFrom validates the envFrom field on a SandboxContainer.
// Each entry must reference exactly one of configMapRef or secretRef. The
// optional prefix must be a valid C_IDENTIFIER. Source names must satisfy
// DNS label constraints.
func validateEnvFrom(envFrom []computev1alpha.EnvFromSource, fldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}

	for i, ef := range envFrom {
		indexPath := fldPath.Index(i)

		// Validate prefix — must be empty or a valid C_IDENTIFIER.
		if ef.Prefix != "" {
			if errs := apimachineryutilvalidation.IsCIdentifier(ef.Prefix); len(errs) > 0 {
				allErrs = append(allErrs, field.Invalid(indexPath.Child("prefix"), ef.Prefix, strings.Join(errs, "; ")))
			}
		}

		numSources := 0

		if ef.ConfigMapRef != nil {
			numSources++
			refPath := indexPath.Child("configMapRef")
			if len(ef.ConfigMapRef.Name) == 0 {
				allErrs = append(allErrs, field.Required(refPath.Child("name"), ""))
			} else {
				for _, msg := range apimachineryvalidation.NameIsDNSLabel(ef.ConfigMapRef.Name, false) {
					allErrs = append(allErrs, field.Invalid(refPath.Child("name"), ef.ConfigMapRef.Name, msg))
				}
			}
		}

		if ef.SecretRef != nil {
			numSources++
			refPath := indexPath.Child("secretRef")
			if numSources > 1 {
				allErrs = append(allErrs, field.Forbidden(refPath, "may not specify more than 1 source per envFrom entry"))
			} else {
				if len(ef.SecretRef.Name) == 0 {
					allErrs = append(allErrs, field.Required(refPath.Child("name"), ""))
				} else {
					for _, msg := range apimachineryvalidation.NameIsDNSLabel(ef.SecretRef.Name, false) {
						allErrs = append(allErrs, field.Invalid(refPath.Child("name"), ef.SecretRef.Name, msg))
					}
				}
			}
		}

		if numSources == 0 {
			allErrs = append(allErrs, field.Required(indexPath, "must specify exactly one of configMapRef or secretRef"))
		}
	}

	return allErrs
}

func validateVirtualMachineRuntime(vm *computev1alpha.VirtualMachineRuntime, volumes map[string]computev1alpha.VolumeSource, fieldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}

	volumeAttachmentsField := fieldPath.Child("volumeAttachments")
	if len(vm.VolumeAttachments) == 0 {
		allErrs = append(allErrs, field.Required(volumeAttachmentsField, "must provide at least one volume attachment for the boot volume"))
	} else {
		allErrs = append(allErrs, validateVolumeAttachments(vm.VolumeAttachments, volumes, volumeAttachmentsField)...)

		// For VMs, the first attached volume must be a boot device
		firstAttachmentName := vm.VolumeAttachments[0].Name
		if volumeSource, ok := volumes[firstAttachmentName]; ok {
			// In the future, we may have other types of bootable volumes. For now,
			// it's only disks populated by images.
			if volumeSource.Disk == nil ||
				volumeSource.Disk.Template == nil ||
				volumeSource.Disk.Template.Spec.Populator == nil ||
				volumeSource.Disk.Template.Spec.Populator.Image == nil {
				allErrs = append(allErrs, field.Required(volumeAttachmentsField.Index(0).Child("name"), "first volume attachment must be a bootable volume"))
			}
		}
	}

	allErrs = append(allErrs, validateNamedPorts(vm.Ports, fieldPath.Child("ports"))...)

	return allErrs
}

func validateVolumeAttachments(
	attachments []computev1alpha.VolumeAttachment,
	volumes map[string]computev1alpha.VolumeSource,
	fieldPath *field.Path,
) field.ErrorList {
	allErrs := field.ErrorList{}

	allMounthPaths := sets.Set[string]{}

	// TODO(jreese) only allow attaching a volume in device mode once

	for i, attachment := range attachments {
		indexPath := fieldPath.Index(i)

		volume, ok := volumes[attachment.Name]
		if !ok {
			allErrs = append(allErrs, field.NotFound(indexPath.Child("name"), attachment.Name))
		}

		attachmentMethod := 0

		// TODO(jreese) validate against image capabilities

		if attachment.MountPath != nil {
			mountPathField := indexPath.Child("mountPath")
			if attachmentMethod > 0 {
				allErrs = append(allErrs, field.Forbidden(mountPathField, "may not specify more than 1 attachment method"))
			} else {
				attachmentMethod++

				// If the volume being attached is a disk, we must confirm it has a
				// filesystem either by being populated by an image, or a filesystem
				// populator.
				if volume.Disk != nil {
					if volume.Disk.Template == nil {
						// Mainly here for when different disk sources come into play
						allErrs = append(allErrs, field.InternalError(mountPathField, fmt.Errorf("unable to determine disk filesystem")))
					} else if volume.Disk.Template.Spec.Populator == nil {
						allErrs = append(allErrs, field.NotFound(mountPathField, "unable to determine if volume's disk has a filesystem"))
					}
				}

				mountPath := *attachment.MountPath
				// TODO(jreese) validate the mount path
				if allMounthPaths.Has(mountPath) {
					allErrs = append(allErrs, field.Duplicate(indexPath.Child("mountPath"), mountPath))
				} else {
					allMounthPaths.Insert(mountPath)
				}
			}
		}
	}

	return allErrs
}

func validateNamedPorts(ports []computev1alpha.NamedPort, fieldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}
	allNames := sets.Set[string]{}

	for i, port := range ports {
		indexPath := fieldPath.Index(i)
		nameField := indexPath.Child("name")
		portField := indexPath.Child("port")

		if len(port.Name) == 0 {
			allErrs = append(allErrs, field.Required(nameField, ""))
		} else {
			for _, msg := range apimachineryutilvalidation.IsValidPortName(port.Name) {
				allErrs = append(allErrs, field.Invalid(nameField, port.Name, msg))
			}
			if allNames.Has(port.Name) {
				allErrs = append(allErrs, field.Duplicate(nameField, port.Name))
			} else {
				allNames.Insert(port.Name)
			}
		}

		for _, msg := range apimachineryutilvalidation.IsValidPortNum(int(port.Port)) {
			allErrs = append(allErrs, field.Invalid(portField, port.Name, msg))
		}
	}

	return allErrs
}

func validateInstanceRuntimeResources(resources computev1alpha.InstanceRuntimeResources, fieldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}

	// TODO(jreese) look up available instance types
	if resources.InstanceType != defaultInstanceType {
		allErrs = append(allErrs, field.NotSupported(fieldPath, resources.InstanceType, []string{defaultInstanceType}))
	}

	if resources.Requests != nil {
		allErrs = append(allErrs, field.Forbidden(fieldPath.Child("requests"), "not implemented"))
	}

	return allErrs
}
