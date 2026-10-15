# API Reference

Packages:

- [compute.datumapis.com/v1alpha](#computedatumapiscomv1alpha)

# compute.datumapis.com/v1alpha

Resource Types:

- [InstanceType](#instancetype)




## InstanceType
<sup><sup>[↩ Parent](#computedatumapiscomv1alpha )</sup></sup>






InstanceType is the Schema for the instancetypes API

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
      <td>InstanceType</td>
      <td>true</td>
      </tr>
      <tr>
      <td><b><a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#objectmeta-v1-meta">metadata</a></b></td>
      <td>object</td>
      <td>Refer to the Kubernetes API documentation for the fields of the `metadata` field.</td>
      <td>true</td>
      </tr><tr>
        <td><b><a href="#instancetypespec">spec</a></b></td>
        <td>object</td>
        <td>
          InstanceTypeSpec defines the desired state of InstanceType<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#instancetypestatus">status</a></b></td>
        <td>object</td>
        <td>
          InstanceTypeStatus defines the observed state of InstanceType<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### InstanceType.spec
<sup><sup>[↩ Parent](#instancetype)</sup></sup>



InstanceTypeSpec defines the desired state of InstanceType

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
        <td><b><a href="#instancetypespeclifecycle">lifecycle</a></b></td>
        <td>object</td>
        <td>
          Lifecycle declares the operator-managed lifecycle of this tier.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b><a href="#instancetypespecresources">resources</a></b></td>
        <td>object</td>
        <td>
          Resources defines the core dimensions of the instance type.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>description</b></td>
        <td>string</td>
        <td>
          Description is a human-friendly description of the instance type.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>displayName</b></td>
        <td>string</td>
        <td>
          DisplayName is a human-friendly display name.<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### InstanceType.spec.lifecycle
<sup><sup>[↩ Parent](#instancetypespec)</sup></sup>



Lifecycle declares the operator-managed lifecycle of this tier.

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
        <td><b>phase</b></td>
        <td>enum</td>
        <td>
          Phase defines the lifecycle phase of this instance type.
Valid values are "Active", "Deprecated", and "Disabled".
The lifecycle progression must follow Active -> Deprecated -> Disabled;
transitions cannot skip states or go backwards.<br/>
          <br/>
            <i>Enum</i>: Active, Deprecated, Disabled<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>replacementInstanceType</b></td>
        <td>string</td>
        <td>
          ReplacementInstanceType specifies the optional successor tier when Phase is Deprecated or Disabled.<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### InstanceType.spec.resources
<sup><sup>[↩ Parent](#instancetypespec)</sup></sup>



Resources defines the core dimensions of the instance type.

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
        <td><b>cpu</b></td>
        <td>int or string</td>
        <td>
          CPU specifies the compute capacity (e.g. "2" or "2000m") representing corev1.ResourceCPU.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>memory</b></td>
        <td>int or string</td>
        <td>
          Memory specifies the memory capacity (e.g. "4Gi" or "4096Mi") representing corev1.ResourceMemory.<br/>
        </td>
        <td>true</td>
      </tr></tbody>
</table>


### InstanceType.status
<sup><sup>[↩ Parent](#instancetype)</sup></sup>



InstanceTypeStatus defines the observed state of InstanceType

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
        <td><b><a href="#instancetypestatusconditionsindex">conditions</a></b></td>
        <td>[]object</td>
        <td>
          Conditions hold the latest available observations of the InstanceType's state.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>deprecatedAt</b></td>
        <td>string</td>
        <td>
          DeprecatedAt records when this instance type entered the Deprecated phase.
It is set by the operator and used to gate the Deprecated -> Disabled
transition on the configured grace period.<br/>
          <br/>
            <i>Format</i>: date-time<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### InstanceType.status.conditions[index]
<sup><sup>[↩ Parent](#instancetypestatus)</sup></sup>



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
