# API Reference

Packages:

- [compute.datumapis.com/v1alpha](#computedatumapiscomv1alpha)

# compute.datumapis.com/v1alpha

Resource Types:

- [Instance](#instance)




## Instance
<sup><sup>[↩ Parent](#computedatumapiscomv1alpha )</sup></sup>






Instance is the Schema for the instances API

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
      <td>Instance</td>
      <td>true</td>
      </tr>
      <tr>
      <td><b><a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#objectmeta-v1-meta">metadata</a></b></td>
      <td>object</td>
      <td>Refer to the Kubernetes API documentation for the fields of the `metadata` field.</td>
      <td>true</td>
      </tr><tr>
        <td><b><a href="#instancespec">spec</a></b></td>
        <td>object</td>
        <td>
          Spec defines the desired state of an Instance.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#instancestatus">status</a></b></td>
        <td>object</td>
        <td>
          Status defines the current state of an Instance.<br/>
          <br/>
            <i>Default</i>: map[conditions:[map[lastTransitionTime:1970-01-01T00:00:00Z message:Waiting for controller reason:Pending status:Unknown type:Programmed] map[lastTransitionTime:1970-01-01T00:00:00Z message:Waiting for controller reason:Pending status:Unknown type:Available] map[lastTransitionTime:1970-01-01T00:00:00Z message:Waiting for controller reason:Pending status:Unknown type:Ready] map[lastTransitionTime:1970-01-01T00:00:00Z message:Waiting for quota evaluation reason:PendingEvaluation status:Unknown type:QuotaGranted]]]<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### Instance.spec
<sup><sup>[↩ Parent](#instance)</sup></sup>



Spec defines the desired state of an Instance.

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
        <td><b><a href="#instancespecnetworkinterfacesindex">networkInterfaces</a></b></td>
        <td>[]object</td>
        <td>
          Network interface configuration.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b><a href="#instancespecruntime">runtime</a></b></td>
        <td>object</td>
        <td>
          The runtime type of the instance, such as a container sandbox or a VM.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b><a href="#instancespeccontroller">controller</a></b></td>
        <td>object</td>
        <td>
          Controller contains settings driven by the controller managing the instance.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#instancespeclocation">location</a></b></td>
        <td>object</td>
        <td>
          The location which the instance has been scheduled to<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#instancespecvolumesindex">volumes</a></b></td>
        <td>[]object</td>
        <td>
          Volumes that must be available to attach to an instance's containers or
Virtual Machine.<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### Instance.spec.networkInterfaces[index]
<sup><sup>[↩ Parent](#instancespec)</sup></sup>





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
        <td><b><a href="#instancespecnetworkinterfacesindexnetwork">network</a></b></td>
        <td>object</td>
        <td>
          The network to attach the network interface to.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b><a href="#instancespecnetworkinterfacesindexnetworkpolicy">networkPolicy</a></b></td>
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


### Instance.spec.networkInterfaces[index].network
<sup><sup>[↩ Parent](#instancespecnetworkinterfacesindex)</sup></sup>



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


### Instance.spec.networkInterfaces[index].networkPolicy
<sup><sup>[↩ Parent](#instancespecnetworkinterfacesindex)</sup></sup>



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
        <td><b><a href="#instancespecnetworkinterfacesindexnetworkpolicyingressindex">ingress</a></b></td>
        <td>[]object</td>
        <td>
          <br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### Instance.spec.networkInterfaces[index].networkPolicy.ingress[index]
<sup><sup>[↩ Parent](#instancespecnetworkinterfacesindexnetworkpolicy)</sup></sup>



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
        <td><b><a href="#instancespecnetworkinterfacesindexnetworkpolicyingressindexfromindex">from</a></b></td>
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
        <td><b><a href="#instancespecnetworkinterfacesindexnetworkpolicyingressindexportsindex">ports</a></b></td>
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


### Instance.spec.networkInterfaces[index].networkPolicy.ingress[index].from[index]
<sup><sup>[↩ Parent](#instancespecnetworkinterfacesindexnetworkpolicyingressindex)</sup></sup>



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
        <td><b><a href="#instancespecnetworkinterfacesindexnetworkpolicyingressindexfromindexipblock">ipBlock</a></b></td>
        <td>object</td>
        <td>
          ipBlock defines policy on a particular IPBlock. If this field is set then
neither of the other fields can be.<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### Instance.spec.networkInterfaces[index].networkPolicy.ingress[index].from[index].ipBlock
<sup><sup>[↩ Parent](#instancespecnetworkinterfacesindexnetworkpolicyingressindexfromindex)</sup></sup>



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


### Instance.spec.networkInterfaces[index].networkPolicy.ingress[index].ports[index]
<sup><sup>[↩ Parent](#instancespecnetworkinterfacesindexnetworkpolicyingressindex)</sup></sup>



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


### Instance.spec.runtime
<sup><sup>[↩ Parent](#instancespec)</sup></sup>



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
        <td><b><a href="#instancespecruntimeresources">resources</a></b></td>
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
        <td><b><a href="#instancespecruntimesandbox">sandbox</a></b></td>
        <td>object</td>
        <td>
          A sandbox is a managed isolated environment capable of running containers.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#instancespecruntimevirtualmachine">virtualMachine</a></b></td>
        <td>object</td>
        <td>
          A virtual machine is a classical VM environment, booting a full OS provided by the user via an image.<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### Instance.spec.runtime.resources
<sup><sup>[↩ Parent](#instancespecruntime)</sup></sup>



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


### Instance.spec.runtime.sandbox
<sup><sup>[↩ Parent](#instancespecruntime)</sup></sup>



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
        <td><b><a href="#instancespecruntimesandboxcontainersindex">containers</a></b></td>
        <td>[]object</td>
        <td>
          A list of containers to run within the sandbox.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b><a href="#instancespecruntimesandboximagepullsecretsindex">imagePullSecrets</a></b></td>
        <td>[]object</td>
        <td>
          An optional list of secrets in the same namespace to use for pulling images
used by the instance.<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### Instance.spec.runtime.sandbox.containers[index]
<sup><sup>[↩ Parent](#instancespecruntimesandbox)</sup></sup>





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
        <td><b><a href="#instancespecruntimesandboxcontainersindexenvindex">env</a></b></td>
        <td>[]object</td>
        <td>
          List of environment variables to set in the container.

so replicate the structure here too.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#instancespecruntimesandboxcontainersindexenvfromindex">envFrom</a></b></td>
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
        <td><b><a href="#instancespecruntimesandboxcontainersindexportsindex">ports</a></b></td>
        <td>[]object</td>
        <td>
          A list of named ports for the container.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#instancespecruntimesandboxcontainersindexresources">resources</a></b></td>
        <td>object</td>
        <td>
          The resource requirements for the container, such as CPU, memory, and GPUs.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#instancespecruntimesandboxcontainersindexvolumeattachmentsindex">volumeAttachments</a></b></td>
        <td>[]object</td>
        <td>
          A list of volumes to attach to the container.<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### Instance.spec.runtime.sandbox.containers[index].env[index]
<sup><sup>[↩ Parent](#instancespecruntimesandboxcontainersindex)</sup></sup>



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
        <td><b><a href="#instancespecruntimesandboxcontainersindexenvindexvaluefrom">valueFrom</a></b></td>
        <td>object</td>
        <td>
          Source for the environment variable's value. Cannot be used if value is not empty.<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### Instance.spec.runtime.sandbox.containers[index].env[index].valueFrom
<sup><sup>[↩ Parent](#instancespecruntimesandboxcontainersindexenvindex)</sup></sup>



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
        <td><b><a href="#instancespecruntimesandboxcontainersindexenvindexvaluefromconfigmapkeyref">configMapKeyRef</a></b></td>
        <td>object</td>
        <td>
          Selects a key of a ConfigMap.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#instancespecruntimesandboxcontainersindexenvindexvaluefromfieldref">fieldRef</a></b></td>
        <td>object</td>
        <td>
          Selects a field of the pod: supports metadata.name, metadata.namespace, `metadata.labels['<KEY>']`, `metadata.annotations['<KEY>']`,
spec.nodeName, spec.serviceAccountName, status.hostIP, status.podIP, status.podIPs.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#instancespecruntimesandboxcontainersindexenvindexvaluefromfilekeyref">fileKeyRef</a></b></td>
        <td>object</td>
        <td>
          FileKeyRef selects a key of the env file.
Requires the EnvFiles feature gate to be enabled.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#instancespecruntimesandboxcontainersindexenvindexvaluefromresourcefieldref">resourceFieldRef</a></b></td>
        <td>object</td>
        <td>
          Selects a resource of the container: only resources limits and requests
(limits.cpu, limits.memory, limits.ephemeral-storage, requests.cpu, requests.memory and requests.ephemeral-storage) are currently supported.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#instancespecruntimesandboxcontainersindexenvindexvaluefromsecretkeyref">secretKeyRef</a></b></td>
        <td>object</td>
        <td>
          Selects a key of a secret in the pod's namespace<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### Instance.spec.runtime.sandbox.containers[index].env[index].valueFrom.configMapKeyRef
<sup><sup>[↩ Parent](#instancespecruntimesandboxcontainersindexenvindexvaluefrom)</sup></sup>



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


### Instance.spec.runtime.sandbox.containers[index].env[index].valueFrom.fieldRef
<sup><sup>[↩ Parent](#instancespecruntimesandboxcontainersindexenvindexvaluefrom)</sup></sup>



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


### Instance.spec.runtime.sandbox.containers[index].env[index].valueFrom.fileKeyRef
<sup><sup>[↩ Parent](#instancespecruntimesandboxcontainersindexenvindexvaluefrom)</sup></sup>



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


### Instance.spec.runtime.sandbox.containers[index].env[index].valueFrom.resourceFieldRef
<sup><sup>[↩ Parent](#instancespecruntimesandboxcontainersindexenvindexvaluefrom)</sup></sup>



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


### Instance.spec.runtime.sandbox.containers[index].env[index].valueFrom.secretKeyRef
<sup><sup>[↩ Parent](#instancespecruntimesandboxcontainersindexenvindexvaluefrom)</sup></sup>



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


### Instance.spec.runtime.sandbox.containers[index].envFrom[index]
<sup><sup>[↩ Parent](#instancespecruntimesandboxcontainersindex)</sup></sup>



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
        <td><b><a href="#instancespecruntimesandboxcontainersindexenvfromindexconfigmapref">configMapRef</a></b></td>
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
        <td><b><a href="#instancespecruntimesandboxcontainersindexenvfromindexsecretref">secretRef</a></b></td>
        <td>object</td>
        <td>
          The Secret to select from.<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### Instance.spec.runtime.sandbox.containers[index].envFrom[index].configMapRef
<sup><sup>[↩ Parent](#instancespecruntimesandboxcontainersindexenvfromindex)</sup></sup>



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


### Instance.spec.runtime.sandbox.containers[index].envFrom[index].secretRef
<sup><sup>[↩ Parent](#instancespecruntimesandboxcontainersindexenvfromindex)</sup></sup>



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


### Instance.spec.runtime.sandbox.containers[index].ports[index]
<sup><sup>[↩ Parent](#instancespecruntimesandboxcontainersindex)</sup></sup>





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


### Instance.spec.runtime.sandbox.containers[index].resources
<sup><sup>[↩ Parent](#instancespecruntimesandboxcontainersindex)</sup></sup>



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


### Instance.spec.runtime.sandbox.containers[index].volumeAttachments[index]
<sup><sup>[↩ Parent](#instancespecruntimesandboxcontainersindex)</sup></sup>





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


### Instance.spec.runtime.sandbox.imagePullSecrets[index]
<sup><sup>[↩ Parent](#instancespecruntimesandbox)</sup></sup>



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


### Instance.spec.runtime.virtualMachine
<sup><sup>[↩ Parent](#instancespecruntime)</sup></sup>



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
        <td><b><a href="#instancespecruntimevirtualmachinevolumeattachmentsindex">volumeAttachments</a></b></td>
        <td>[]object</td>
        <td>
          A list of volumes to attach to the VM.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b><a href="#instancespecruntimevirtualmachineportsindex">ports</a></b></td>
        <td>[]object</td>
        <td>
          A list of named ports for the virtual machine.<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### Instance.spec.runtime.virtualMachine.volumeAttachments[index]
<sup><sup>[↩ Parent](#instancespecruntimevirtualmachine)</sup></sup>





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


### Instance.spec.runtime.virtualMachine.ports[index]
<sup><sup>[↩ Parent](#instancespecruntimevirtualmachine)</sup></sup>





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


### Instance.spec.controller
<sup><sup>[↩ Parent](#instancespec)</sup></sup>



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
        <td><b><a href="#instancespeccontrollerschedulinggatesindex">schedulingGates</a></b></td>
        <td>[]object</td>
        <td>
          SchedulingGates is a list of gates that must be satisfied before the
instance can be scheduled.<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### Instance.spec.controller.schedulingGates[index]
<sup><sup>[↩ Parent](#instancespeccontroller)</sup></sup>





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


### Instance.spec.location
<sup><sup>[↩ Parent](#instancespec)</sup></sup>



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


### Instance.spec.volumes[index]
<sup><sup>[↩ Parent](#instancespec)</sup></sup>





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
        <td><b><a href="#instancespecvolumesindexconfigmap">configMap</a></b></td>
        <td>object</td>
        <td>
          A configMap that should populate this volume<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#instancespecvolumesindexdisk">disk</a></b></td>
        <td>object</td>
        <td>
          A persistent disk backed volume.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#instancespecvolumesindexsecret">secret</a></b></td>
        <td>object</td>
        <td>
          A secret that should populate this volume<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### Instance.spec.volumes[index].configMap
<sup><sup>[↩ Parent](#instancespecvolumesindex)</sup></sup>



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
        <td><b><a href="#instancespecvolumesindexconfigmapitemsindex">items</a></b></td>
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


### Instance.spec.volumes[index].configMap.items[index]
<sup><sup>[↩ Parent](#instancespecvolumesindexconfigmap)</sup></sup>



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


### Instance.spec.volumes[index].disk
<sup><sup>[↩ Parent](#instancespecvolumesindex)</sup></sup>



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
        <td><b><a href="#instancespecvolumesindexdisktemplate">template</a></b></td>
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


### Instance.spec.volumes[index].disk.template
<sup><sup>[↩ Parent](#instancespecvolumesindexdisk)</sup></sup>



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
        <td><b><a href="#instancespecvolumesindexdisktemplatespec">spec</a></b></td>
        <td>object</td>
        <td>
          Describes the desired configuration of a disk<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b><a href="#instancespecvolumesindexdisktemplatemetadata">metadata</a></b></td>
        <td>object</td>
        <td>
          Metadata of the disks created from this template<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### Instance.spec.volumes[index].disk.template.spec
<sup><sup>[↩ Parent](#instancespecvolumesindexdisktemplate)</sup></sup>



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
        <td><b><a href="#instancespecvolumesindexdisktemplatespecpopulator">populator</a></b></td>
        <td>object</td>
        <td>
          Populator to use while initializing the disk.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#instancespecvolumesindexdisktemplatespecresources">resources</a></b></td>
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


### Instance.spec.volumes[index].disk.template.spec.populator
<sup><sup>[↩ Parent](#instancespecvolumesindexdisktemplatespec)</sup></sup>



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
        <td><b><a href="#instancespecvolumesindexdisktemplatespecpopulatorfilesystem">filesystem</a></b></td>
        <td>object</td>
        <td>
          Populate the disk with a filesystem<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#instancespecvolumesindexdisktemplatespecpopulatorimage">image</a></b></td>
        <td>object</td>
        <td>
          Populate the disk from an image<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### Instance.spec.volumes[index].disk.template.spec.populator.filesystem
<sup><sup>[↩ Parent](#instancespecvolumesindexdisktemplatespecpopulator)</sup></sup>



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


### Instance.spec.volumes[index].disk.template.spec.populator.image
<sup><sup>[↩ Parent](#instancespecvolumesindexdisktemplatespecpopulator)</sup></sup>



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


### Instance.spec.volumes[index].disk.template.spec.resources
<sup><sup>[↩ Parent](#instancespecvolumesindexdisktemplatespec)</sup></sup>



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


### Instance.spec.volumes[index].disk.template.metadata
<sup><sup>[↩ Parent](#instancespecvolumesindexdisktemplate)</sup></sup>



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


### Instance.spec.volumes[index].secret
<sup><sup>[↩ Parent](#instancespecvolumesindex)</sup></sup>



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
        <td><b><a href="#instancespecvolumesindexsecretitemsindex">items</a></b></td>
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


### Instance.spec.volumes[index].secret.items[index]
<sup><sup>[↩ Parent](#instancespecvolumesindexsecret)</sup></sup>



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


### Instance.status
<sup><sup>[↩ Parent](#instance)</sup></sup>



Status defines the current state of an Instance.

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
        <td><b><a href="#instancestatusconditionsindex">conditions</a></b></td>
        <td>[]object</td>
        <td>
          Represents the observations of an instance's current state.
Known condition types are: "Available", "Progressing"<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#instancestatuscontroller">controller</a></b></td>
        <td>object</td>
        <td>
          Controller contains status information about the controller managing the instance.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#instancestatusnetworkinterfacesindex">networkInterfaces</a></b></td>
        <td>[]object</td>
        <td>
          Network interface information<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### Instance.status.conditions[index]
<sup><sup>[↩ Parent](#instancestatus)</sup></sup>



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


### Instance.status.controller
<sup><sup>[↩ Parent](#instancestatus)</sup></sup>



Controller contains status information about the controller managing the instance.

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
        <td><b>observedTemplateHash</b></td>
        <td>string</td>
        <td>
          ObservedTemplateHash is the hash of the instance template applied for this instance.<br/>
        </td>
        <td>true</td>
      </tr></tbody>
</table>


### Instance.status.networkInterfaces[index]
<sup><sup>[↩ Parent](#instancestatus)</sup></sup>





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
        <td><b><a href="#instancestatusnetworkinterfacesindexassignments">assignments</a></b></td>
        <td>object</td>
        <td>
          <br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### Instance.status.networkInterfaces[index].assignments
<sup><sup>[↩ Parent](#instancestatusnetworkinterfacesindex)</sup></sup>





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
        <td><b>externalIP</b></td>
        <td>string</td>
        <td>
          The external IP address used for the interface. A one to one NAT will be
performed for this address with the interface's network IP.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>networkIP</b></td>
        <td>string</td>
        <td>
          The IP address assigned as the primary IP from the attached network.<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>
