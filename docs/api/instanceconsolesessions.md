# API Reference

Packages:

- [compute.datumapis.com/v1alpha](#computedatumapiscomv1alpha)

# compute.datumapis.com/v1alpha

Resource Types:

- [InstanceConsoleSession](#instanceconsolesession)




## InstanceConsoleSession
<sup><sup>[↩ Parent](#computedatumapiscomv1alpha )</sup></sup>






InstanceConsoleSession runs one command in one container of an instance and
connects a client to it, such as an interactive shell. Deleting the session
stops the command.

Each session is one invocation, so clients create sessions with generateName.

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
      <td>InstanceConsoleSession</td>
      <td>true</td>
      </tr>
      <tr>
      <td><b><a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#objectmeta-v1-meta">metadata</a></b></td>
      <td>object</td>
      <td>Refer to the Kubernetes API documentation for the fields of the `metadata` field.</td>
      <td>true</td>
      </tr><tr>
        <td><b><a href="#instanceconsolesessionspec">spec</a></b></td>
        <td>object</td>
        <td>
          InstanceConsoleSessionSpec is one command to run in one container of an
instance. The whole spec is immutable: a different request is a new session.<br/>
          <br/>
            <i>Validations</i>:<li>self == oldSelf: spec is immutable</li>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b><a href="#instanceconsolesessionstatus">status</a></b></td>
        <td>object</td>
        <td>
          InstanceConsoleSessionStatus is the session's progress as the platform
reports it. Every field is output only.<br/>
          <br/>
            <i>Default</i>: map[conditions:[map[lastTransitionTime:1970-01-01T00:00:00Z message:Waiting for the session to be prepared reason:Pending status:Unknown type:Ready]]]<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### InstanceConsoleSession.spec
<sup><sup>[↩ Parent](#instanceconsolesession)</sup></sup>



InstanceConsoleSessionSpec is one command to run in one container of an
instance. The whole spec is immutable: a different request is a new session.

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
        <td><b>clientPublicKey</b></td>
        <td>string</td>
        <td>
          The client's 32-byte public key as 64 lowercase hexadecimal characters.
Only the holder of the matching private key can connect to the session,
and the private key never enters the API.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>command</b></td>
        <td>[]string</td>
        <td>
          The command to run, as an argument vector rather than a shell string. The
image must contain the executable and a shell, which the platform uses to
stop the command when the session ends. The arguments total at most 16 KiB.<br/>
          <br/>
            <i>Validations</i>:<li>self.map(arg, size(bytes(arg))).sum() <= 16384: command must total at most 16 KiB</li>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>containerName</b></td>
        <td>string</td>
        <td>
          The exact name of the container to run the command in.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b><a href="#instanceconsolesessionspecinstanceref">instanceRef</a></b></td>
        <td>object</td>
        <td>
          The instance to run the command in, in the same project as the session.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>stdin</b></td>
        <td>boolean</td>
        <td>
          Keeps the command's standard input open, with or without a terminal.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>terminal</b></td>
        <td>boolean</td>
        <td>
          Allocates a terminal for the command, which merges its output streams.
Without one, standard output and standard error stay separate.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>ttl</b></td>
        <td>string</td>
        <td>
          How long the command may run once it starts, from 1m to 1h.<br/>
          <br/>
            <i>Validations</i>:<li>duration(self) >= duration('1m') && duration(self) <= duration('1h'): ttl must be between 1m and 1h</li>
            <i>Default</i>: 15m<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### InstanceConsoleSession.spec.instanceRef
<sup><sup>[↩ Parent](#instanceconsolesessionspec)</sup></sup>



The instance to run the command in, in the same project as the session.

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
          The instance's name.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>uid</b></td>
        <td>string</td>
        <td>
          The instance's metadata.uid.<br/>
        </td>
        <td>true</td>
      </tr></tbody>
</table>


### InstanceConsoleSession.status
<sup><sup>[↩ Parent](#instanceconsolesession)</sup></sup>



InstanceConsoleSessionStatus is the session's progress as the platform
reports it. Every field is output only.

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
        <td><b><a href="#instanceconsolesessionstatusconditionsindex">conditions</a></b></td>
        <td>[]object</td>
        <td>
          <br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>connectBefore</b></td>
        <td>string</td>
        <td>
          The deadline for connecting. A session nobody connects to by then ends
with NotConnected.<br/>
          <br/>
            <i>Format</i>: date-time<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#instanceconsolesessionstatusconnection">connection</a></b></td>
        <td>object</td>
        <td>
          Where to connect, set once the session is ready.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>endedAt</b></td>
        <td>string</td>
        <td>
          When the session ended.<br/>
          <br/>
            <i>Format</i>: date-time<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>exitCode</b></td>
        <td>integer</td>
        <td>
          The command's exit code, set only when the command exited on its own.<br/>
          <br/>
            <i>Format</i>: int32<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>expiresAt</b></td>
        <td>string</td>
        <td>
          When the command is stopped: startedAt plus spec.ttl.<br/>
          <br/>
            <i>Format</i>: date-time<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>startedAt</b></td>
        <td>string</td>
        <td>
          When the command started.<br/>
          <br/>
            <i>Format</i>: date-time<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### InstanceConsoleSession.status.conditions[index]
<sup><sup>[↩ Parent](#instanceconsolesessionstatus)</sup></sup>



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


### InstanceConsoleSession.status.connection
<sup><sup>[↩ Parent](#instanceconsolesessionstatus)</sup></sup>



Where to connect, set once the session is ready.

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
        <td><b>endpointID</b></td>
        <td>string</td>
        <td>
          The hexadecimal ID of the endpoint serving the session.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>relayURLs</b></td>
        <td>[]string</td>
        <td>
          The relays the endpoint is reachable through, nearest first.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>target</b></td>
        <td>string</td>
        <td>
          The host:port the client names when it opens a stream to the endpoint.<br/>
        </td>
        <td>true</td>
      </tr></tbody>
</table>
