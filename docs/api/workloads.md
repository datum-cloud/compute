# API Reference

Packages:

- [compute.datumapis.com/v1alpha](#computedatumapiscomv1alpha)

# compute.datumapis.com/v1alpha

Resource Types:

- [Workload](#workload)




## Workload
<sup><sup>[↩ Parent](#computedatumapiscomv1alpha )</sup></sup>






Workload is the Schema for the workloads API

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
      <td><b>apiVersion</b></td>
      <td>string</td>
      <td>compute.datumapis.com/v1alpha</td>
      <td>true</td>
      </tr>
      <tr>
      <td><b>kind</b></td>
      <td>string</td>
      <td>Workload</td>
      <td>true</td>
      </tr>
      <tr>
      <td><b><a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#objectmeta-v1-meta">metadata</a></b></td>
      <td>object</td>
      <td>Refer to the Kubernetes API documentation for the fields of the `metadata` field.</td>
      <td>true</td>
      </tr><tr>
        <td><b><a href="#workloadspec">spec</a></b></td>
        <td>object</td>
        <td>
          WorkloadSpec defines the desired state of Workload<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b><a href="#workloadstatus">status</a></b></td>
        <td>object</td>
        <td>
          WorkloadStatus defines the observed state of Workload<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### Workload.spec
<sup><sup>[↩ Parent](#workload)</sup></sup>



WorkloadSpec defines the desired state of Workload

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b><a href="#workloadspecplacementsindex">placements</a></b></td>
        <td>[]object</td>
        <td>
          Defines where instances should be deployed, and at what scope a deployment
will live in, such as in a city, or region.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b><a href="#workloadspectemplate">template</a></b></td>
        <td>object</td>
        <td>
          Defines settings for each instance.<br/>
        </td>
        <td>true</td>
      </tr></tbody>
</table>


### Workload.spec.placements[index]
<sup><sup>[↩ Parent](#workloadspec)</sup></sup>





<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>cityCodes</b></td>
        <td>[]string</td>
        <td>
          A list of city codes that define where the instances should be deployed.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>name</b></td>
        <td>string</td>
        <td>
          The name of the placement<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b><a href="#workloadspecplacementsindexscalesettings">scaleSettings</a></b></td>
        <td>object</td>
        <td>
          Scale settings such as minimum and maximum replica counts.<br/>
        </td>
        <td>true</td>
      </tr></tbody>
</table>


### Workload.spec.placements[index].scaleSettings
<sup><sup>[↩ Parent](#workloadspecplacementsindex)</sup></sup>



Scale settings such as minimum and maximum replica counts.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>instanceManagementPolicy</b></td>
        <td>string</td>
        <td>
          Controls how instances are managed during scale up and down, as well as
during maintenance events.<br/>
          <br/>
            <i>Default</i>: OrderedReady<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>minReplicas</b></td>
        <td>integer</td>
        <td>
          The minimum number of replicas.<br/>
          <br/>
            <i>Format</i>: int32<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>maxReplicas</b></td>
        <td>integer</td>
        <td>
          The maximum number of replicas.<br/>
          <br/>
            <i>Format</i>: int32<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#workloadspecplacementsindexscalesettingsmetricsindex">metrics</a></b></td>
        <td>[]object</td>
        <td>
          A list of metrics that determine scaling behavior, such as external metrics.<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### Workload.spec.placements[index].scaleSettings.metrics[index]
<sup><sup>[↩ Parent](#workloadspecplacementsindexscalesettings)</sup></sup>





<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b><a href="#workloadspecplacementsindexscalesettingsmetricsindexresource">resource</a></b></td>
        <td>object</td>
        <td>
          Resource metrics known to Datum.<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### Workload.spec.placements[index].scaleSettings.metrics[index].resource
<sup><sup>[↩ Parent](#workloadspecplacementsindexscalesettingsmetricsindex)</sup></sup>



Resource metrics known to Datum.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>name</b></td>
        <td>string</td>
        <td>
          The name of the resource in question.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b><a href="#workloadspecplacementsindexscalesettingsmetricsindexresourcetarget">target</a></b></td>
        <td>object</td>
        <td>
          The target value for the given metric<br/>
        </td>
        <td>true</td>
      </tr></tbody>
</table>


### Workload.spec.placements[index].scaleSettings.metrics[index].resource.target
<sup><sup>[↩ Parent](#workloadspecplacementsindexscalesettingsmetricsindexresource)</sup></sup>



The target value for the given metric

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>averageUtilization</b></td>
        <td>integer</td>
        <td>
          The target value of the average of the
resource metric across all relevant instances, represented as a percentage of
the requested value of the resource for the instances.<br/>
          <br/>
            <i>Format</i>: int32<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>averageValue</b></td>
        <td>int or string</td>
        <td>
          The target value of the average of the metric across all relevant instances
(as a quantity)<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>value</b></td>
        <td>int or string</td>
        <td>
          The target value of the metric (as a quantity).<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### Workload.spec.template
<sup><sup>[↩ Parent](#workloadspec)</sup></sup>



Defines settings for each instance.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b><a href="#workloadspectemplatespec">spec</a></b></td>
        <td>object</td>
        <td>
          Describes the desired configuration of an instance<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b><a href="#workloadspectemplatemetadata">metadata</a></b></td>
        <td>object</td>
        <td>
          Metadata of the instances created from this template<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### Workload.spec.template.spec
<sup><sup>[↩ Parent](#workloadspectemplate)</sup></sup>



Describes the desired configuration of an instance

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b><a href="#workloadspectemplatespecnetworkinterfacesindex">networkInterfaces</a></b></td>
        <td>[]object</td>
        <td>
          Network interface configuration.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b><a href="#workloadspectemplatespecruntime">runtime</a></b></td>
        <td>object</td>
        <td>
          The runtime type of the instance, such as a container sandbox or a VM.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b><a href="#workloadspectemplatespeccontroller">controller</a></b></td>
        <td>object</td>
        <td>
          Controller contains settings driven by the controller managing the instance.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#workloadspectemplatespeclocation">location</a></b></td>
        <td>object</td>
        <td>
          The location which the instance has been scheduled to<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#workloadspectemplatespecvolumesindex">volumes</a></b></td>
        <td>[]object</td>
        <td>
          Volumes that must be available to attach to an instance's containers or
Virtual Machine.<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### Workload.spec.template.spec.networkInterfaces[index]
<sup><sup>[↩ Parent](#workloadspectemplatespec)</sup></sup>





<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b><a href="#workloadspectemplatespecnetworkinterfacesindexnetwork">network</a></b></td>
        <td>object</td>
        <td>
          The network to attach the network interface to.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b><a href="#workloadspectemplatespecnetworkinterfacesindexnetworkpolicy">networkPolicy</a></b></td>
        <td>object</td>
        <td>
          Interface specific network policy.

If provided, this will result in a platform managed network policy being
created that targets the specfiic instance interface. This network policy
will be of the lowest priority, and can effectively be prohibited from
influencing network connectivity.<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### Workload.spec.template.spec.networkInterfaces[index].network
<sup><sup>[↩ Parent](#workloadspectemplatespecnetworkinterfacesindex)</sup></sup>



The network to attach the network interface to.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>name</b></td>
        <td>string</td>
        <td>
          The network name<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>namespace</b></td>
        <td>string</td>
        <td>
          The network namespace.

Defaults to the namespace for the type the reference is embedded in.<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### Workload.spec.template.spec.networkInterfaces[index].networkPolicy
<sup><sup>[↩ Parent](#workloadspectemplatespecnetworkinterfacesindex)</sup></sup>



Interface specific network policy.

If provided, this will result in a platform managed network policy being
created that targets the specfiic instance interface. This network policy
will be of the lowest priority, and can effectively be prohibited from
influencing network connectivity.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b><a href="#workloadspectemplatespecnetworkinterfacesindexnetworkpolicyingressindex">ingress</a></b></td>
        <td>[]object</td>
        <td>
          <br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### Workload.spec.template.spec.networkInterfaces[index].networkPolicy.ingress[index]
<sup><sup>[↩ Parent](#workloadspectemplatespecnetworkinterfacesindexnetworkpolicy)</sup></sup>



See k8s network policy types for inspiration here

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b><a href="#workloadspectemplatespecnetworkinterfacesindexnetworkpolicyingressindexfromindex">from</a></b></td>
        <td>[]object</td>
        <td>
          from is a list of sources which should be able to access the instances selected for this rule.
Items in this list are combined using a logical OR operation. If this field is
empty or missing, this rule matches all sources (traffic not restricted by
source). If this field is present and contains at least one item, this rule
allows traffic only if the traffic matches at least one item in the from list.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#workloadspectemplatespecnetworkinterfacesindexnetworkpolicyingressindexportsindex">ports</a></b></td>
        <td>[]object</td>
        <td>
          ports is a list of ports which should be made accessible on the instances selected for
this rule. Each item in this list is combined using a logical OR. If this field is
empty or missing, this rule matches all ports (traffic not restricted by port).
If this field is present and contains at least one item, then this rule allows
traffic only if the traffic matches at least one port in the list.<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### Workload.spec.template.spec.networkInterfaces[index].networkPolicy.ingress[index].from[index]
<sup><sup>[↩ Parent](#workloadspectemplatespecnetworkinterfacesindexnetworkpolicyingressindex)</sup></sup>



NetworkPolicyPeer describes a peer to allow traffic to/from. Only certain combinations of
fields are allowed

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b><a href="#workloadspectemplatespecnetworkinterfacesindexnetworkpolicyingressindexfromindexipblock">ipBlock</a></b></td>
        <td>object</td>
        <td>
          ipBlock defines policy on a particular IPBlock. If this field is set then
neither of the other fields can be.<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### Workload.spec.template.spec.networkInterfaces[index].networkPolicy.ingress[index].from[index].ipBlock
<sup><sup>[↩ Parent](#workloadspectemplatespecnetworkinterfacesindexnetworkpolicyingressindexfromindex)</sup></sup>



ipBlock defines policy on a particular IPBlock. If this field is set then
neither of the other fields can be.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>cidr</b></td>
        <td>string</td>
        <td>
          cidr is a string representing the IPBlock
Valid examples are "192.168.1.0/24" or "2001:db8::/64"<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>except</b></td>
        <td>[]string</td>
        <td>
          except is a slice of CIDRs that should not be included within an IPBlock
Valid examples are "192.168.1.0/24" or "2001:db8::/64"
Except values will be rejected if they are outside the cidr range<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### Workload.spec.template.spec.networkInterfaces[index].networkPolicy.ingress[index].ports[index]
<sup><sup>[↩ Parent](#workloadspectemplatespecnetworkinterfacesindexnetworkpolicyingressindex)</sup></sup>



NetworkPolicyPort describes a port to allow traffic on

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>endPort</b></td>
        <td>integer</td>
        <td>
          endPort indicates that the range of ports from port to endPort if set, inclusive,
should be allowed by the policy. This field cannot be defined if the port field
is not defined or if the port field is defined as a named (string) port.
The endPort must be equal or greater than port.<br/>
          <br/>
            <i>Format</i>: int32<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>port</b></td>
        <td>int or string</td>
        <td>
          port represents the port on the given protocol. This can either be a numerical or named
port on an instance. If this field is not provided, this matches all port names and
numbers.
If present, only traffic on the specified protocol AND port will be matched.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>protocol</b></td>
        <td>string</td>
        <td>
          protocol represents the protocol (TCP, UDP, or SCTP) which traffic must match.
If not specified, this field defaults to TCP.<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### Workload.spec.template.spec.runtime
<sup><sup>[↩ Parent](#workloadspectemplatespec)</sup></sup>



The runtime type of the instance, such as a container sandbox or a VM.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b><a href="#workloadspectemplatespecruntimeresources">resources</a></b></td>
        <td>object</td>
        <td>
          Resources each instance must be allocated.

A sandbox runtime's containers may specify resource requests and
limits. When limits are defined on all containers, they MUST consume
the entire amount of resources defined here. Some resources, such
as a GPU, MUST have at least one container request them so that the
device can be presented appropriately.

A virtual machine runtime will be provided all requested resources.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b><a href="#workloadspectemplatespecruntimesandbox">sandbox</a></b></td>
        <td>object</td>
        <td>
          A sandbox is a managed isolated environment capable of running containers.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#workloadspectemplatespecruntimevirtualmachine">virtualMachine</a></b></td>
        <td>object</td>
        <td>
          A virtual machine is a classical VM environment, booting a full OS provided by the user via an image.<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### Workload.spec.template.spec.runtime.resources
<sup><sup>[↩ Parent](#workloadspectemplatespecruntime)</sup></sup>



Resources each instance must be allocated.

A sandbox runtime's containers may specify resource requests and
limits. When limits are defined on all containers, they MUST consume
the entire amount of resources defined here. Some resources, such
as a GPU, MUST have at least one container request them so that the
device can be presented appropriately.

A virtual machine runtime will be provided all requested resources.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>instanceType</b></td>
        <td>string</td>
        <td>
          Full or partial URL of the instance type resource to use for this instance.

For example: `datumcloud/d1-standard-2`

May be combined with `resources` to allow for custom instance types for
instance families that support customization. Instance types which support
customization will appear in the form `<project>/<instanceFamily>-custom`.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>requests</b></td>
        <td>map[string]int or string</td>
        <td>
          Describes adjustments to the resources defined by the instance type.<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### Workload.spec.template.spec.runtime.sandbox
<sup><sup>[↩ Parent](#workloadspectemplatespecruntime)</sup></sup>



A sandbox is a managed isolated environment capable of running containers.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b><a href="#workloadspectemplatespecruntimesandboxcontainersindex">containers</a></b></td>
        <td>[]object</td>
        <td>
          A list of containers to run within the sandbox.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b><a href="#workloadspectemplatespecruntimesandboximagepullsecretsindex">imagePullSecrets</a></b></td>
        <td>[]object</td>
        <td>
          An optional list of secrets in the same namespace to use for pulling images
used by the instance.<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### Workload.spec.template.spec.runtime.sandbox.containers[index]
<sup><sup>[↩ Parent](#workloadspectemplatespecruntimesandbox)</sup></sup>





<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>image</b></td>
        <td>string</td>
        <td>
          The fully qualified container image name.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>name</b></td>
        <td>string</td>
        <td>
          The name of the container.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>args</b></td>
        <td>[]string</td>
        <td>
          Arguments to the entrypoint, overriding the image's CMD. Combined with
Command: when Command is also set the resulting invocation is
append(Command, Args...).  When only Args is set it overrides CMD while
preserving the image's ENTRYPOINT.

If neither Command nor Args is set, the image's own ENTRYPOINT and CMD
are used unchanged.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>command</b></td>
        <td>[]string</td>
        <td>
          Entrypoint array to run in the container image, overriding the image's
ENTRYPOINT. Each element is a separate token, not a shell command — to run a
shell command use: ["sh", "-c", "my command"].

If not provided, the container image's own ENTRYPOINT is used.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#workloadspectemplatespecruntimesandboxcontainersindexenvindex">env</a></b></td>
        <td>[]object</td>
        <td>
          List of environment variables to set in the container.

so replicate the structure here too.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#workloadspectemplatespecruntimesandboxcontainersindexenvfromindex">envFrom</a></b></td>
        <td>[]object</td>
        <td>
          List of sources to populate environment variables in the container.
The keys defined within a source must be a C_IDENTIFIER. All invalid
keys will be reported as an event when the container is starting. When a
key exists in multiple sources, the value associated with the last source
will take precedence. Values defined by an Env with a duplicate key will
take precedence.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#workloadspectemplatespecruntimesandboxcontainersindexportsindex">ports</a></b></td>
        <td>[]object</td>
        <td>
          A list of named ports for the container.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#workloadspectemplatespecruntimesandboxcontainersindexresources">resources</a></b></td>
        <td>object</td>
        <td>
          The resource requirements for the container, such as CPU, memory, and GPUs.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#workloadspectemplatespecruntimesandboxcontainersindexvolumeattachmentsindex">volumeAttachments</a></b></td>
        <td>[]object</td>
        <td>
          A list of volumes to attach to the container.<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### Workload.spec.template.spec.runtime.sandbox.containers[index].env[index]
<sup><sup>[↩ Parent](#workloadspectemplatespecruntimesandboxcontainersindex)</sup></sup>



EnvVar represents an environment variable present in a Container.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>name</b></td>
        <td>string</td>
        <td>
          Name of the environment variable.
May consist of any printable ASCII characters except '='.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>value</b></td>
        <td>string</td>
        <td>
          Variable references $(VAR_NAME) are expanded
using the previously defined environment variables in the container and
any service environment variables. If a variable cannot be resolved,
the reference in the input string will be unchanged. Double $$ are reduced
to a single $, which allows for escaping the $(VAR_NAME) syntax: i.e.
"$$(VAR_NAME)" will produce the string literal "$(VAR_NAME)".
Escaped references will never be expanded, regardless of whether the variable
exists or not.
Defaults to "".<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#workloadspectemplatespecruntimesandboxcontainersindexenvindexvaluefrom">valueFrom</a></b></td>
        <td>object</td>
        <td>
          Source for the environment variable's value. Cannot be used if value is not empty.<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### Workload.spec.template.spec.runtime.sandbox.containers[index].env[index].valueFrom
<sup><sup>[↩ Parent](#workloadspectemplatespecruntimesandboxcontainersindexenvindex)</sup></sup>



Source for the environment variable's value. Cannot be used if value is not empty.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b><a href="#workloadspectemplatespecruntimesandboxcontainersindexenvindexvaluefromconfigmapkeyref">configMapKeyRef</a></b></td>
        <td>object</td>
        <td>
          Selects a key of a ConfigMap.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#workloadspectemplatespecruntimesandboxcontainersindexenvindexvaluefromfieldref">fieldRef</a></b></td>
        <td>object</td>
        <td>
          Selects a field of the pod: supports metadata.name, metadata.namespace, `metadata.labels['<KEY>']`, `metadata.annotations['<KEY>']`,
spec.nodeName, spec.serviceAccountName, status.hostIP, status.podIP, status.podIPs.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#workloadspectemplatespecruntimesandboxcontainersindexenvindexvaluefromfilekeyref">fileKeyRef</a></b></td>
        <td>object</td>
        <td>
          FileKeyRef selects a key of the env file.
Requires the EnvFiles feature gate to be enabled.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#workloadspectemplatespecruntimesandboxcontainersindexenvindexvaluefromresourcefieldref">resourceFieldRef</a></b></td>
        <td>object</td>
        <td>
          Selects a resource of the container: only resources limits and requests
(limits.cpu, limits.memory, limits.ephemeral-storage, requests.cpu, requests.memory and requests.ephemeral-storage) are currently supported.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#workloadspectemplatespecruntimesandboxcontainersindexenvindexvaluefromsecretkeyref">secretKeyRef</a></b></td>
        <td>object</td>
        <td>
          Selects a key of a secret in the pod's namespace<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### Workload.spec.template.spec.runtime.sandbox.containers[index].env[index].valueFrom.configMapKeyRef
<sup><sup>[↩ Parent](#workloadspectemplatespecruntimesandboxcontainersindexenvindexvaluefrom)</sup></sup>



Selects a key of a ConfigMap.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>key</b></td>
        <td>string</td>
        <td>
          The key to select.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>name</b></td>
        <td>string</td>
        <td>
          Name of the referent.
This field is effectively required, but due to backwards compatibility is
allowed to be empty. Instances of this type with an empty value here are
almost certainly wrong.
More info: https://kubernetes.io/docs/concepts/overview/working-with-objects/names/#names<br/>
          <br/>
            <i>Default</i>: <br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>optional</b></td>
        <td>boolean</td>
        <td>
          Specify whether the ConfigMap or its key must be defined<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### Workload.spec.template.spec.runtime.sandbox.containers[index].env[index].valueFrom.fieldRef
<sup><sup>[↩ Parent](#workloadspectemplatespecruntimesandboxcontainersindexenvindexvaluefrom)</sup></sup>



Selects a field of the pod: supports metadata.name, metadata.namespace, `metadata.labels['<KEY>']`, `metadata.annotations['<KEY>']`,
spec.nodeName, spec.serviceAccountName, status.hostIP, status.podIP, status.podIPs.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>fieldPath</b></td>
        <td>string</td>
        <td>
          Path of the field to select in the specified API version.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>apiVersion</b></td>
        <td>string</td>
        <td>
          Version of the schema the FieldPath is written in terms of, defaults to "v1".<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### Workload.spec.template.spec.runtime.sandbox.containers[index].env[index].valueFrom.fileKeyRef
<sup><sup>[↩ Parent](#workloadspectemplatespecruntimesandboxcontainersindexenvindexvaluefrom)</sup></sup>



FileKeyRef selects a key of the env file.
Requires the EnvFiles feature gate to be enabled.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>key</b></td>
        <td>string</td>
        <td>
          The key within the env file. An invalid key will prevent the pod from starting.
The keys defined within a source may consist of any printable ASCII characters except '='.
During Alpha stage of the EnvFiles feature gate, the key size is limited to 128 characters.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>path</b></td>
        <td>string</td>
        <td>
          The path within the volume from which to select the file.
Must be relative and may not contain the '..' path or start with '..'.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>volumeName</b></td>
        <td>string</td>
        <td>
          The name of the volume mount containing the env file.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>optional</b></td>
        <td>boolean</td>
        <td>
          Specify whether the file or its key must be defined. If the file or key
does not exist, then the env var is not published.
If optional is set to true and the specified key does not exist,
the environment variable will not be set in the Pod's containers.

If optional is set to false and the specified key does not exist,
an error will be returned during Pod creation.<br/>
          <br/>
            <i>Default</i>: false<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### Workload.spec.template.spec.runtime.sandbox.containers[index].env[index].valueFrom.resourceFieldRef
<sup><sup>[↩ Parent](#workloadspectemplatespecruntimesandboxcontainersindexenvindexvaluefrom)</sup></sup>



Selects a resource of the container: only resources limits and requests
(limits.cpu, limits.memory, limits.ephemeral-storage, requests.cpu, requests.memory and requests.ephemeral-storage) are currently supported.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>resource</b></td>
        <td>string</td>
        <td>
          Required: resource to select<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>containerName</b></td>
        <td>string</td>
        <td>
          Container name: required for volumes, optional for env vars<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>divisor</b></td>
        <td>int or string</td>
        <td>
          Specifies the output format of the exposed resources, defaults to "1"<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### Workload.spec.template.spec.runtime.sandbox.containers[index].env[index].valueFrom.secretKeyRef
<sup><sup>[↩ Parent](#workloadspectemplatespecruntimesandboxcontainersindexenvindexvaluefrom)</sup></sup>



Selects a key of a secret in the pod's namespace

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>key</b></td>
        <td>string</td>
        <td>
          The key of the secret to select from.  Must be a valid secret key.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>name</b></td>
        <td>string</td>
        <td>
          Name of the referent.
This field is effectively required, but due to backwards compatibility is
allowed to be empty. Instances of this type with an empty value here are
almost certainly wrong.
More info: https://kubernetes.io/docs/concepts/overview/working-with-objects/names/#names<br/>
          <br/>
            <i>Default</i>: <br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>optional</b></td>
        <td>boolean</td>
        <td>
          Specify whether the Secret or its key must be defined<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### Workload.spec.template.spec.runtime.sandbox.containers[index].envFrom[index]
<sup><sup>[↩ Parent](#workloadspectemplatespecruntimesandboxcontainersindex)</sup></sup>



EnvFromSource represents a source for a set of ConfigMaps or Secrets to be
used as environment variables in a container.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b><a href="#workloadspectemplatespecruntimesandboxcontainersindexenvfromindexconfigmapref">configMapRef</a></b></td>
        <td>object</td>
        <td>
          The ConfigMap to select from.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>prefix</b></td>
        <td>string</td>
        <td>
          An optional identifier to prepend to each key in the referenced
ConfigMap or Secret. Must be a valid C_IDENTIFIER.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#workloadspectemplatespecruntimesandboxcontainersindexenvfromindexsecretref">secretRef</a></b></td>
        <td>object</td>
        <td>
          The Secret to select from.<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### Workload.spec.template.spec.runtime.sandbox.containers[index].envFrom[index].configMapRef
<sup><sup>[↩ Parent](#workloadspectemplatespecruntimesandboxcontainersindexenvfromindex)</sup></sup>



The ConfigMap to select from.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>name</b></td>
        <td>string</td>
        <td>
          Name of the ConfigMap in the same namespace as the Workload.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>optional</b></td>
        <td>boolean</td>
        <td>
          Specify whether the ConfigMap must be defined.<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### Workload.spec.template.spec.runtime.sandbox.containers[index].envFrom[index].secretRef
<sup><sup>[↩ Parent](#workloadspectemplatespecruntimesandboxcontainersindexenvfromindex)</sup></sup>



The Secret to select from.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>name</b></td>
        <td>string</td>
        <td>
          Name of the Secret in the same namespace as the Workload.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>optional</b></td>
        <td>boolean</td>
        <td>
          Specify whether the Secret must be defined.<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### Workload.spec.template.spec.runtime.sandbox.containers[index].ports[index]
<sup><sup>[↩ Parent](#workloadspectemplatespecruntimesandboxcontainersindex)</sup></sup>





<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>name</b></td>
        <td>string</td>
        <td>
          The name of the port that can be referenced by other platform features.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>port</b></td>
        <td>integer</td>
        <td>
          The port number, which can be a value between 1 and 65535.<br/>
          <br/>
            <i>Format</i>: int32<br/>
            <i>Minimum</i>: 1<br/>
            <i>Maximum</i>: 65535<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>protocol</b></td>
        <td>string</td>
        <td>
          protocol represents the protocol (TCP, UDP, or SCTP) which traffic must match.
If not specified, this field defaults to TCP.<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### Workload.spec.template.spec.runtime.sandbox.containers[index].resources
<sup><sup>[↩ Parent](#workloadspectemplatespecruntimesandboxcontainersindex)</sup></sup>



The resource requirements for the container, such as CPU, memory, and GPUs.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>limits</b></td>
        <td>map[string]int or string</td>
        <td>
          Limits describes the maximum amount of compute resources allowed.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>requests</b></td>
        <td>map[string]int or string</td>
        <td>
          Requests describes the minimum amount of compute resources required.<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### Workload.spec.template.spec.runtime.sandbox.containers[index].volumeAttachments[index]
<sup><sup>[↩ Parent](#workloadspectemplatespecruntimesandboxcontainersindex)</sup></sup>





<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>name</b></td>
        <td>string</td>
        <td>
          The name of the volume to attach as defined in InstanceSpec.Volumes.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>mountPath</b></td>
        <td>string</td>
        <td>
          The path to mount the volume inside the guest OS.

The referenced volume must be populated with a filesystem to use this
feature.

For VM based instances, this functionality requires certain capabilities
to be annotated on the boot image, such as cloud-init.<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### Workload.spec.template.spec.runtime.sandbox.imagePullSecrets[index]
<sup><sup>[↩ Parent](#workloadspectemplatespecruntimesandbox)</sup></sup>



References a secret in the same namespace as the entity defining the
reference.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>name</b></td>
        <td>string</td>
        <td>
          The name of the secret<br/>
        </td>
        <td>true</td>
      </tr></tbody>
</table>


### Workload.spec.template.spec.runtime.virtualMachine
<sup><sup>[↩ Parent](#workloadspectemplatespecruntime)</sup></sup>



A virtual machine is a classical VM environment, booting a full OS provided by the user via an image.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b><a href="#workloadspectemplatespecruntimevirtualmachinevolumeattachmentsindex">volumeAttachments</a></b></td>
        <td>[]object</td>
        <td>
          A list of volumes to attach to the VM.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b><a href="#workloadspectemplatespecruntimevirtualmachineportsindex">ports</a></b></td>
        <td>[]object</td>
        <td>
          A list of named ports for the virtual machine.<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### Workload.spec.template.spec.runtime.virtualMachine.volumeAttachments[index]
<sup><sup>[↩ Parent](#workloadspectemplatespecruntimevirtualmachine)</sup></sup>





<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>name</b></td>
        <td>string</td>
        <td>
          The name of the volume to attach as defined in InstanceSpec.Volumes.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>mountPath</b></td>
        <td>string</td>
        <td>
          The path to mount the volume inside the guest OS.

The referenced volume must be populated with a filesystem to use this
feature.

For VM based instances, this functionality requires certain capabilities
to be annotated on the boot image, such as cloud-init.<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### Workload.spec.template.spec.runtime.virtualMachine.ports[index]
<sup><sup>[↩ Parent](#workloadspectemplatespecruntimevirtualmachine)</sup></sup>





<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>name</b></td>
        <td>string</td>
        <td>
          The name of the port that can be referenced by other platform features.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>port</b></td>
        <td>integer</td>
        <td>
          The port number, which can be a value between 1 and 65535.<br/>
          <br/>
            <i>Format</i>: int32<br/>
            <i>Minimum</i>: 1<br/>
            <i>Maximum</i>: 65535<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>protocol</b></td>
        <td>string</td>
        <td>
          protocol represents the protocol (TCP, UDP, or SCTP) which traffic must match.
If not specified, this field defaults to TCP.<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### Workload.spec.template.spec.controller
<sup><sup>[↩ Parent](#workloadspectemplatespec)</sup></sup>



Controller contains settings driven by the controller managing the instance.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>templateHash</b></td>
        <td>string</td>
        <td>
          TemplateHash is the hash of the instance template applied for this instance.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b><a href="#workloadspectemplatespeccontrollerschedulinggatesindex">schedulingGates</a></b></td>
        <td>[]object</td>
        <td>
          SchedulingGates is a list of gates that must be satisfied before the
instance can be scheduled.<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### Workload.spec.template.spec.controller.schedulingGates[index]
<sup><sup>[↩ Parent](#workloadspectemplatespeccontroller)</sup></sup>





<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>name</b></td>
        <td>string</td>
        <td>
          The name of the gate.<br/>
        </td>
        <td>true</td>
      </tr></tbody>
</table>


### Workload.spec.template.spec.location
<sup><sup>[↩ Parent](#workloadspectemplatespec)</sup></sup>



The location which the instance has been scheduled to

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>name</b></td>
        <td>string</td>
        <td>
          Name of a datum location<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>namespace</b></td>
        <td>string</td>
        <td>
          Namespace for the datum location<br/>
        </td>
        <td>true</td>
      </tr></tbody>
</table>


### Workload.spec.template.spec.volumes[index]
<sup><sup>[↩ Parent](#workloadspectemplatespec)</sup></sup>





<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>name</b></td>
        <td>string</td>
        <td>
          Name is used to reference the volume in `volumeAttachments` for
containers and VMs, and will be used to derive the platform resource
name when required by prefixing this name with the instance name upon
creation.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b><a href="#workloadspectemplatespecvolumesindexconfigmap">configMap</a></b></td>
        <td>object</td>
        <td>
          A configMap that should populate this volume<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#workloadspectemplatespecvolumesindexdisk">disk</a></b></td>
        <td>object</td>
        <td>
          A persistent disk backed volume.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#workloadspectemplatespecvolumesindexsecret">secret</a></b></td>
        <td>object</td>
        <td>
          A secret that should populate this volume<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### Workload.spec.template.spec.volumes[index].configMap
<sup><sup>[↩ Parent](#workloadspectemplatespecvolumesindex)</sup></sup>



A configMap that should populate this volume

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>defaultMode</b></td>
        <td>integer</td>
        <td>
          defaultMode is optional: mode bits used to set permissions on created files by default.
Must be an octal value between 0000 and 0777 or a decimal value between 0 and 511.
YAML accepts both octal and decimal values, JSON requires decimal values for mode bits.
Defaults to 0644.
Directories within the path are not affected by this setting.
This might be in conflict with other options that affect the file
mode, like fsGroup, and the result can be other mode bits set.<br/>
          <br/>
            <i>Format</i>: int32<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#workloadspectemplatespecvolumesindexconfigmapitemsindex">items</a></b></td>
        <td>[]object</td>
        <td>
          items if unspecified, each key-value pair in the Data field of the referenced
ConfigMap will be projected into the volume as a file whose name is the
key and content is the value. If specified, the listed keys will be
projected into the specified paths, and unlisted keys will not be
present. If a key is specified which is not present in the ConfigMap,
the volume setup will error unless it is marked optional. Paths must be
relative and may not contain the '..' path or start with '..'.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>name</b></td>
        <td>string</td>
        <td>
          Name of the referent.
This field is effectively required, but due to backwards compatibility is
allowed to be empty. Instances of this type with an empty value here are
almost certainly wrong.
More info: https://kubernetes.io/docs/concepts/overview/working-with-objects/names/#names<br/>
          <br/>
            <i>Default</i>: <br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>optional</b></td>
        <td>boolean</td>
        <td>
          optional specify whether the ConfigMap or its keys must be defined<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### Workload.spec.template.spec.volumes[index].configMap.items[index]
<sup><sup>[↩ Parent](#workloadspectemplatespecvolumesindexconfigmap)</sup></sup>



Maps a string key to a path within a volume.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>key</b></td>
        <td>string</td>
        <td>
          key is the key to project.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>path</b></td>
        <td>string</td>
        <td>
          path is the relative path of the file to map the key to.
May not be an absolute path.
May not contain the path element '..'.
May not start with the string '..'.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>mode</b></td>
        <td>integer</td>
        <td>
          mode is Optional: mode bits used to set permissions on this file.
Must be an octal value between 0000 and 0777 or a decimal value between 0 and 511.
YAML accepts both octal and decimal values, JSON requires decimal values for mode bits.
If not specified, the volume defaultMode will be used.
This might be in conflict with other options that affect the file
mode, like fsGroup, and the result can be other mode bits set.<br/>
          <br/>
            <i>Format</i>: int32<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### Workload.spec.template.spec.volumes[index].disk
<sup><sup>[↩ Parent](#workloadspectemplatespecvolumesindex)</sup></sup>



A persistent disk backed volume.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b><a href="#workloadspectemplatespecvolumesindexdisktemplate">template</a></b></td>
        <td>object</td>
        <td>
          Settings to create a new disk for an attached disk<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>deviceName</b></td>
        <td>string</td>
        <td>
          Specifies a unique device name that is reflected into the
`/dev/disk/by-id/datumcloud-*` tree of a Linux operating system
running within the instance. This name can be used to reference
the device for mounting, resizing, and so on, from within the
instance.

If not specified, the server chooses a default device name to
apply to this disk, in the form persistent-disk-x, where x is a
number assigned by Datum Cloud.<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### Workload.spec.template.spec.volumes[index].disk.template
<sup><sup>[↩ Parent](#workloadspectemplatespecvolumesindexdisk)</sup></sup>



Settings to create a new disk for an attached disk

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b><a href="#workloadspectemplatespecvolumesindexdisktemplatespec">spec</a></b></td>
        <td>object</td>
        <td>
          Describes the desired configuration of a disk<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b><a href="#workloadspectemplatespecvolumesindexdisktemplatemetadata">metadata</a></b></td>
        <td>object</td>
        <td>
          Metadata of the disks created from this template<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### Workload.spec.template.spec.volumes[index].disk.template.spec
<sup><sup>[↩ Parent](#workloadspectemplatespecvolumesindexdisktemplate)</sup></sup>



Describes the desired configuration of a disk

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b><a href="#workloadspectemplatespecvolumesindexdisktemplatespecpopulator">populator</a></b></td>
        <td>object</td>
        <td>
          Populator to use while initializing the disk.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#workloadspectemplatespecvolumesindexdisktemplatespecresources">resources</a></b></td>
        <td>object</td>
        <td>
          The resource requirements for the disk.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>type</b></td>
        <td>string</td>
        <td>
          The type the disk, such as `pd-standard`.<br/>
          <br/>
            <i>Default</i>: pd-standard<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### Workload.spec.template.spec.volumes[index].disk.template.spec.populator
<sup><sup>[↩ Parent](#workloadspectemplatespecvolumesindexdisktemplatespec)</sup></sup>



Populator to use while initializing the disk.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b><a href="#workloadspectemplatespecvolumesindexdisktemplatespecpopulatorfilesystem">filesystem</a></b></td>
        <td>object</td>
        <td>
          Populate the disk with a filesystem<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#workloadspectemplatespecvolumesindexdisktemplatespecpopulatorimage">image</a></b></td>
        <td>object</td>
        <td>
          Populate the disk from an image<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### Workload.spec.template.spec.volumes[index].disk.template.spec.populator.filesystem
<sup><sup>[↩ Parent](#workloadspectemplatespecvolumesindexdisktemplatespecpopulator)</sup></sup>



Populate the disk with a filesystem

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>type</b></td>
        <td>enum</td>
        <td>
          The type of filesystem to populate the disk with.<br/>
          <br/>
            <i>Enum</i>: ext4<br/>
        </td>
        <td>true</td>
      </tr></tbody>
</table>


### Workload.spec.template.spec.volumes[index].disk.template.spec.populator.image
<sup><sup>[↩ Parent](#workloadspectemplatespecvolumesindexdisktemplatespecpopulator)</sup></sup>



Populate the disk from an image

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>name</b></td>
        <td>string</td>
        <td>
          The name of the image to populate the disk with.

	in `populator.image.imageRef.name` though.<br/>
        </td>
        <td>true</td>
      </tr></tbody>
</table>


### Workload.spec.template.spec.volumes[index].disk.template.spec.resources
<sup><sup>[↩ Parent](#workloadspectemplatespecvolumesindexdisktemplatespec)</sup></sup>



The resource requirements for the disk.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>requests</b></td>
        <td>map[string]int or string</td>
        <td>
          Requests describes the minimum amount of storage resources required.<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### Workload.spec.template.spec.volumes[index].disk.template.metadata
<sup><sup>[↩ Parent](#workloadspectemplatespecvolumesindexdisktemplate)</sup></sup>



Metadata of the disks created from this template

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>annotations</b></td>
        <td>map[string]string</td>
        <td>
          <br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>finalizers</b></td>
        <td>[]string</td>
        <td>
          <br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>labels</b></td>
        <td>map[string]string</td>
        <td>
          <br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>name</b></td>
        <td>string</td>
        <td>
          <br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>namespace</b></td>
        <td>string</td>
        <td>
          <br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### Workload.spec.template.spec.volumes[index].secret
<sup><sup>[↩ Parent](#workloadspectemplatespecvolumesindex)</sup></sup>



A secret that should populate this volume

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>defaultMode</b></td>
        <td>integer</td>
        <td>
          defaultMode is Optional: mode bits used to set permissions on created files by default.
Must be an octal value between 0000 and 0777 or a decimal value between 0 and 511.
YAML accepts both octal and decimal values, JSON requires decimal values
for mode bits. Defaults to 0644.
Directories within the path are not affected by this setting.
This might be in conflict with other options that affect the file
mode, like fsGroup, and the result can be other mode bits set.<br/>
          <br/>
            <i>Format</i>: int32<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#workloadspectemplatespecvolumesindexsecretitemsindex">items</a></b></td>
        <td>[]object</td>
        <td>
          items If unspecified, each key-value pair in the Data field of the referenced
Secret will be projected into the volume as a file whose name is the
key and content is the value. If specified, the listed keys will be
projected into the specified paths, and unlisted keys will not be
present. If a key is specified which is not present in the Secret,
the volume setup will error unless it is marked optional. Paths must be
relative and may not contain the '..' path or start with '..'.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>optional</b></td>
        <td>boolean</td>
        <td>
          optional field specify whether the Secret or its keys must be defined<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>secretName</b></td>
        <td>string</td>
        <td>
          secretName is the name of the secret in the pod's namespace to use.
More info: https://kubernetes.io/docs/concepts/storage/volumes#secret<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### Workload.spec.template.spec.volumes[index].secret.items[index]
<sup><sup>[↩ Parent](#workloadspectemplatespecvolumesindexsecret)</sup></sup>



Maps a string key to a path within a volume.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>key</b></td>
        <td>string</td>
        <td>
          key is the key to project.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>path</b></td>
        <td>string</td>
        <td>
          path is the relative path of the file to map the key to.
May not be an absolute path.
May not contain the path element '..'.
May not start with the string '..'.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>mode</b></td>
        <td>integer</td>
        <td>
          mode is Optional: mode bits used to set permissions on this file.
Must be an octal value between 0000 and 0777 or a decimal value between 0 and 511.
YAML accepts both octal and decimal values, JSON requires decimal values for mode bits.
If not specified, the volume defaultMode will be used.
This might be in conflict with other options that affect the file
mode, like fsGroup, and the result can be other mode bits set.<br/>
          <br/>
            <i>Format</i>: int32<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### Workload.spec.template.metadata
<sup><sup>[↩ Parent](#workloadspectemplate)</sup></sup>



Metadata of the instances created from this template

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>annotations</b></td>
        <td>map[string]string</td>
        <td>
          <br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>finalizers</b></td>
        <td>[]string</td>
        <td>
          <br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>labels</b></td>
        <td>map[string]string</td>
        <td>
          <br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>name</b></td>
        <td>string</td>
        <td>
          <br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>namespace</b></td>
        <td>string</td>
        <td>
          <br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### Workload.status
<sup><sup>[↩ Parent](#workload)</sup></sup>



WorkloadStatus defines the observed state of Workload

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>currentReplicas</b></td>
        <td>integer</td>
        <td>
          The number of instances which have the latest workload settings applied
and are programmed (a subset of UpdatedReplicas that are ready to serve).<br/>
          <br/>
            <i>Format</i>: int32<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>deployments</b></td>
        <td>integer</td>
        <td>
          The number of deployments that currently exist<br/>
          <br/>
            <i>Format</i>: int32<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>desiredReplicas</b></td>
        <td>integer</td>
        <td>
          The desired number of instances<br/>
          <br/>
            <i>Format</i>: int32<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>readyReplicas</b></td>
        <td>integer</td>
        <td>
          The number of instances which are ready.<br/>
          <br/>
            <i>Format</i>: int32<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>replicas</b></td>
        <td>integer</td>
        <td>
          The number of instances that currently exist<br/>
          <br/>
            <i>Format</i>: int32<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>updatedReplicas</b></td>
        <td>integer</td>
        <td>
          The number of instances updated to the latest template revision (their
observed template hash matches the desired template), regardless of
readiness. Lags Replicas during a rolling update or restart, then catches
back up — making an in-progress roll observable.<br/>
          <br/>
            <i>Format</i>: int32<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b><a href="#workloadstatusconditionsindex">conditions</a></b></td>
        <td>[]object</td>
        <td>
          Represents the observations of a workload's current state.
Known condition types are: "Available", "Progressing"<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#workloadstatusgateway">gateway</a></b></td>
        <td>object</td>
        <td>
          The status of the workload gateway if configured.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>observedGeneration</b></td>
        <td>integer</td>
        <td>
          The most recent generation observed by the workload controller.<br/>
          <br/>
            <i>Format</i>: int64<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#workloadstatusplacementsindex">placements</a></b></td>
        <td>[]object</td>
        <td>
          The current status of placemetns in a workload.<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### Workload.status.conditions[index]
<sup><sup>[↩ Parent](#workloadstatus)</sup></sup>



Condition contains details for one aspect of the current state of this API Resource.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>lastTransitionTime</b></td>
        <td>string</td>
        <td>
          lastTransitionTime is the last time the condition transitioned from one status to another.
This should be when the underlying condition changed.  If that is not known, then using the time when the API field changed is acceptable.<br/>
          <br/>
            <i>Format</i>: date-time<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>message</b></td>
        <td>string</td>
        <td>
          message is a human readable message indicating details about the transition.
This may be an empty string.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>reason</b></td>
        <td>string</td>
        <td>
          reason contains a programmatic identifier indicating the reason for the condition's last transition.
Producers of specific condition types may define expected values and meanings for this field,
and whether the values are considered a guaranteed API.
The value should be a CamelCase string.
This field may not be empty.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>status</b></td>
        <td>enum</td>
        <td>
          status of the condition, one of True, False, Unknown.<br/>
          <br/>
            <i>Enum</i>: True, False, Unknown<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>type</b></td>
        <td>string</td>
        <td>
          type of condition in CamelCase or in foo.example.com/CamelCase.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>observedGeneration</b></td>
        <td>integer</td>
        <td>
          observedGeneration represents the .metadata.generation that the condition was set based upon.
For instance, if .metadata.generation is currently 12, but the .status.conditions[x].observedGeneration is 9, the condition is out of date
with respect to the current state of the instance.<br/>
          <br/>
            <i>Format</i>: int64<br/>
            <i>Minimum</i>: 0<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### Workload.status.gateway
<sup><sup>[↩ Parent](#workloadstatus)</sup></sup>



The status of the workload gateway if configured.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b><a href="#workloadstatusgatewayaddressesindex">addresses</a></b></td>
        <td>[]object</td>
        <td>
          Addresses lists the network addresses that have been bound to the
Gateway.

This list may differ from the addresses provided in the spec under some
conditions:

  * no addresses are specified, all addresses are dynamically assigned
  * a combination of specified and dynamic addresses are assigned
  * a specified address was unusable (e.g. already in use)

<gateway:validateIPAddress><br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#workloadstatusgatewayconditionsindex">conditions</a></b></td>
        <td>[]object</td>
        <td>
          Conditions describe the current conditions of the Gateway.

Implementations should prefer to express Gateway conditions
using the `GatewayConditionType` and `GatewayConditionReason`
constants so that operators and tools can converge on a common
vocabulary to describe Gateway state.

Known condition types are:

* "Accepted"
* "Programmed"
* "Ready"<br/>
          <br/>
            <i>Default</i>: [map[lastTransitionTime:1970-01-01T00:00:00Z message:Waiting for controller reason:Pending status:Unknown type:Accepted] map[lastTransitionTime:1970-01-01T00:00:00Z message:Waiting for controller reason:Pending status:Unknown type:Programmed]]<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#workloadstatusgatewaylistenersindex">listeners</a></b></td>
        <td>[]object</td>
        <td>
          Listeners provide status for each unique listener port defined in the Spec.<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### Workload.status.gateway.addresses[index]
<sup><sup>[↩ Parent](#workloadstatusgateway)</sup></sup>



GatewayStatusAddress describes a network address that is bound to a Gateway.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>value</b></td>
        <td>string</td>
        <td>
          Value of the address. The validity of the values will depend
on the type and support by the controller.

Examples: `1.2.3.4`, `128::1`, `my-ip-address`.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>type</b></td>
        <td>string</td>
        <td>
          Type of the address.<br/>
          <br/>
            <i>Default</i>: IPAddress<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### Workload.status.gateway.conditions[index]
<sup><sup>[↩ Parent](#workloadstatusgateway)</sup></sup>



Condition contains details for one aspect of the current state of this API Resource.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>lastTransitionTime</b></td>
        <td>string</td>
        <td>
          lastTransitionTime is the last time the condition transitioned from one status to another.
This should be when the underlying condition changed.  If that is not known, then using the time when the API field changed is acceptable.<br/>
          <br/>
            <i>Format</i>: date-time<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>message</b></td>
        <td>string</td>
        <td>
          message is a human readable message indicating details about the transition.
This may be an empty string.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>reason</b></td>
        <td>string</td>
        <td>
          reason contains a programmatic identifier indicating the reason for the condition's last transition.
Producers of specific condition types may define expected values and meanings for this field,
and whether the values are considered a guaranteed API.
The value should be a CamelCase string.
This field may not be empty.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>status</b></td>
        <td>enum</td>
        <td>
          status of the condition, one of True, False, Unknown.<br/>
          <br/>
            <i>Enum</i>: True, False, Unknown<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>type</b></td>
        <td>string</td>
        <td>
          type of condition in CamelCase or in foo.example.com/CamelCase.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>observedGeneration</b></td>
        <td>integer</td>
        <td>
          observedGeneration represents the .metadata.generation that the condition was set based upon.
For instance, if .metadata.generation is currently 12, but the .status.conditions[x].observedGeneration is 9, the condition is out of date
with respect to the current state of the instance.<br/>
          <br/>
            <i>Format</i>: int64<br/>
            <i>Minimum</i>: 0<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### Workload.status.gateway.listeners[index]
<sup><sup>[↩ Parent](#workloadstatusgateway)</sup></sup>



ListenerStatus is the status associated with a Listener.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>attachedRoutes</b></td>
        <td>integer</td>
        <td>
          AttachedRoutes represents the total number of Routes that have been
successfully attached to this Listener.

Successful attachment of a Route to a Listener is based solely on the
combination of the AllowedRoutes field on the corresponding Listener
and the Route's ParentRefs field. A Route is successfully attached to
a Listener when it is selected by the Listener's AllowedRoutes field
AND the Route has a valid ParentRef selecting the whole Gateway
resource or a specific Listener as a parent resource (more detail on
attachment semantics can be found in the documentation on the various
Route kinds ParentRefs fields). Listener or Route status does not impact
successful attachment, i.e. the AttachedRoutes field count MUST be set
for Listeners with condition Accepted: false and MUST count successfully
attached Routes that may themselves have Accepted: false conditions.

Uses for this field include troubleshooting Route attachment and
measuring blast radius/impact of changes to a Listener.<br/>
          <br/>
            <i>Format</i>: int32<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b><a href="#workloadstatusgatewaylistenersindexconditionsindex">conditions</a></b></td>
        <td>[]object</td>
        <td>
          Conditions describe the current condition of this listener.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>name</b></td>
        <td>string</td>
        <td>
          Name is the name of the Listener that this status corresponds to.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b><a href="#workloadstatusgatewaylistenersindexsupportedkindsindex">supportedKinds</a></b></td>
        <td>[]object</td>
        <td>
          SupportedKinds is the list indicating the Kinds supported by this
listener. This MUST represent the kinds an implementation supports for
that Listener configuration.

If kinds are specified in Spec that are not supported, they MUST NOT
appear in this list and an implementation MUST set the "ResolvedRefs"
condition to "False" with the "InvalidRouteKinds" reason. If both valid
and invalid Route kinds are specified, the implementation MUST
reference the valid Route kinds that have been specified.<br/>
        </td>
        <td>true</td>
      </tr></tbody>
</table>


### Workload.status.gateway.listeners[index].conditions[index]
<sup><sup>[↩ Parent](#workloadstatusgatewaylistenersindex)</sup></sup>



Condition contains details for one aspect of the current state of this API Resource.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>lastTransitionTime</b></td>
        <td>string</td>
        <td>
          lastTransitionTime is the last time the condition transitioned from one status to another.
This should be when the underlying condition changed.  If that is not known, then using the time when the API field changed is acceptable.<br/>
          <br/>
            <i>Format</i>: date-time<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>message</b></td>
        <td>string</td>
        <td>
          message is a human readable message indicating details about the transition.
This may be an empty string.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>reason</b></td>
        <td>string</td>
        <td>
          reason contains a programmatic identifier indicating the reason for the condition's last transition.
Producers of specific condition types may define expected values and meanings for this field,
and whether the values are considered a guaranteed API.
The value should be a CamelCase string.
This field may not be empty.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>status</b></td>
        <td>enum</td>
        <td>
          status of the condition, one of True, False, Unknown.<br/>
          <br/>
            <i>Enum</i>: True, False, Unknown<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>type</b></td>
        <td>string</td>
        <td>
          type of condition in CamelCase or in foo.example.com/CamelCase.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>observedGeneration</b></td>
        <td>integer</td>
        <td>
          observedGeneration represents the .metadata.generation that the condition was set based upon.
For instance, if .metadata.generation is currently 12, but the .status.conditions[x].observedGeneration is 9, the condition is out of date
with respect to the current state of the instance.<br/>
          <br/>
            <i>Format</i>: int64<br/>
            <i>Minimum</i>: 0<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### Workload.status.gateway.listeners[index].supportedKinds[index]
<sup><sup>[↩ Parent](#workloadstatusgatewaylistenersindex)</sup></sup>



RouteGroupKind indicates the group and kind of a Route resource.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>kind</b></td>
        <td>string</td>
        <td>
          Kind is the kind of the Route.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>group</b></td>
        <td>string</td>
        <td>
          Group is the group of the Route.<br/>
          <br/>
            <i>Default</i>: gateway.networking.k8s.io<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### Workload.status.placements[index]
<sup><sup>[↩ Parent](#workloadstatus)</sup></sup>





<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>currentReplicas</b></td>
        <td>integer</td>
        <td>
          The number of instances which have the latest workload settings applied
and are programmed (a subset of UpdatedReplicas that are ready to serve).<br/>
          <br/>
            <i>Format</i>: int32<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>desiredReplicas</b></td>
        <td>integer</td>
        <td>
          The desired number of instances<br/>
          <br/>
            <i>Format</i>: int32<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>name</b></td>
        <td>string</td>
        <td>
          The name of the placement<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>readyReplicas</b></td>
        <td>integer</td>
        <td>
          The number of instances which are ready.<br/>
          <br/>
            <i>Format</i>: int32<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>replicas</b></td>
        <td>integer</td>
        <td>
          The number of instances that currently exist<br/>
          <br/>
            <i>Format</i>: int32<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>updatedReplicas</b></td>
        <td>integer</td>
        <td>
          The number of instances updated to the latest template revision, regardless
of readiness. Lags Replicas during a rolling update or restart.<br/>
          <br/>
            <i>Format</i>: int32<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b><a href="#workloadstatusplacementsindexconditionsindex">conditions</a></b></td>
        <td>[]object</td>
        <td>
          Represents the observations of a placement's current state.
Known condition types are: "Available", "Progressing"<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### Workload.status.placements[index].conditions[index]
<sup><sup>[↩ Parent](#workloadstatusplacementsindex)</sup></sup>



Condition contains details for one aspect of the current state of this API Resource.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>lastTransitionTime</b></td>
        <td>string</td>
        <td>
          lastTransitionTime is the last time the condition transitioned from one status to another.
This should be when the underlying condition changed.  If that is not known, then using the time when the API field changed is acceptable.<br/>
          <br/>
            <i>Format</i>: date-time<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>message</b></td>
        <td>string</td>
        <td>
          message is a human readable message indicating details about the transition.
This may be an empty string.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>reason</b></td>
        <td>string</td>
        <td>
          reason contains a programmatic identifier indicating the reason for the condition's last transition.
Producers of specific condition types may define expected values and meanings for this field,
and whether the values are considered a guaranteed API.
The value should be a CamelCase string.
This field may not be empty.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>status</b></td>
        <td>enum</td>
        <td>
          status of the condition, one of True, False, Unknown.<br/>
          <br/>
            <i>Enum</i>: True, False, Unknown<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>type</b></td>
        <td>string</td>
        <td>
          type of condition in CamelCase or in foo.example.com/CamelCase.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>observedGeneration</b></td>
        <td>integer</td>
        <td>
          observedGeneration represents the .metadata.generation that the condition was set based upon.
For instance, if .metadata.generation is currently 12, but the .status.conditions[x].observedGeneration is 9, the condition is out of date
with respect to the current state of the instance.<br/>
          <br/>
            <i>Format</i>: int64<br/>
            <i>Minimum</i>: 0<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>
