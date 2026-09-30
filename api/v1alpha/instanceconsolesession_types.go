// SPDX-License-Identifier: AGPL-3.0-only

package v1alpha

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// InstanceConsoleSessionSpec is one command to run in one container of an
// instance. The whole spec is immutable: a different request is a new session.
type InstanceConsoleSessionSpec struct {
	// The instance to run the command in, in the same project as the session.
	//
	// +kubebuilder:validation:Required
	InstanceRef InstanceConsoleSessionInstanceRef `json:"instanceRef"`

	// The exact name of the container to run the command in.
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	ContainerName string `json:"containerName"`

	// The command to run, as an argument vector rather than a shell string. The
	// image must contain the executable and a shell, which the platform uses to
	// stop the command when the session ends. The arguments total at most 16 KiB.
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:MaxLength=16384
	// +kubebuilder:validation:XValidation:message="command must total at most 16 KiB",rule="self.map(arg, size(bytes(arg))).sum() <= 16384"
	Command []string `json:"command"`

	// Keeps the command's standard input open, with or without a terminal.
	//
	// +kubebuilder:validation:Optional
	Stdin bool `json:"stdin,omitempty"`

	// Allocates a terminal for the command, which merges its output streams.
	// Without one, standard output and standard error stay separate.
	//
	// +kubebuilder:validation:Optional
	Terminal bool `json:"terminal,omitempty"`

	// The client's 32-byte public key as 64 lowercase hexadecimal characters.
	// Only the holder of the matching private key can connect to the session,
	// and the private key never enters the API.
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^[0-9a-f]{64}$`
	ClientPublicKey string `json:"clientPublicKey"`

	// How long the command may run once it starts, from 1m to 1h.
	//
	// +kubebuilder:validation:Optional
	// +kubebuilder:default="15m"
	// +kubebuilder:validation:XValidation:message="ttl must be between 1m and 1h",rule="duration(self) >= duration('1m') && duration(self) <= duration('1h')"
	TTL *metav1.Duration `json:"ttl,omitempty"`
}

// InstanceConsoleSessionInstanceRef names an instance by name and UID. The UID
// keeps a session from reaching a replacement instance that reused the name.
type InstanceConsoleSessionInstanceRef struct {
	// The instance's name.
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`

	// The instance's metadata.uid.
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=36
	UID types.UID `json:"uid"`
}

// InstanceConsoleSessionConnection is where a client connects to the session.
// None of it is secret: only the holder of the session's client key can
// connect.
type InstanceConsoleSessionConnection struct {
	// The hexadecimal ID of the endpoint serving the session.
	EndpointID string `json:"endpointID"`

	// The relays the endpoint is reachable through, nearest first.
	RelayURLs []string `json:"relayURLs"`

	// The host:port the client names when it opens a stream to the endpoint.
	Target string `json:"target"`
}

// InstanceConsoleSessionStatus is the session's progress as the platform
// reports it. Every field is output only.
type InstanceConsoleSessionStatus struct {
	// Where to connect, set once the session is ready.
	//
	// +kubebuilder:validation:Optional
	Connection *InstanceConsoleSessionConnection `json:"connection,omitempty"`

	// The deadline for connecting. A session nobody connects to by then ends
	// with NotConnected.
	//
	// +kubebuilder:validation:Optional
	ConnectBefore *metav1.Time `json:"connectBefore,omitempty"`

	// When the command started.
	//
	// +kubebuilder:validation:Optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`

	// When the command is stopped: startedAt plus spec.ttl.
	//
	// +kubebuilder:validation:Optional
	ExpiresAt *metav1.Time `json:"expiresAt,omitempty"`

	// When the session ended.
	//
	// +kubebuilder:validation:Optional
	EndedAt *metav1.Time `json:"endedAt,omitempty"`

	// The command's exit code, set only when the command exited on its own.
	//
	// +kubebuilder:validation:Optional
	ExitCode *int32 `json:"exitCode,omitempty"`

	// +listType=map
	// +listMapKey=type
	// +kubebuilder:validation:MaxItems=8
	// +kubebuilder:validation:Optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// Condition types reported on an InstanceConsoleSession.
const (
	// InstanceConsoleSessionReady is Unknown while the session waits for the
	// platform, True while a client can connect or is connected, and False
	// once the session has ended. A False reason never changes.
	InstanceConsoleSessionReady = "Ready"
)

// Reasons for the Ready condition while the session is not yet ready.
const (
	// InstanceConsoleSessionReasonPending means the platform has not yet
	// prepared the session.
	InstanceConsoleSessionReasonPending = "Pending"
)

// Reasons for the Ready condition while a client can connect or is connected.
const (
	// InstanceConsoleSessionReasonSessionReady means the platform has published
	// where to connect and is waiting for the client.
	InstanceConsoleSessionReasonSessionReady = "SessionReady"

	// InstanceConsoleSessionReasonConnected means a client has connected and
	// the command is running. A session accepts only one connection.
	InstanceConsoleSessionReasonConnected = "Connected"
)

// Terminal reasons for the Ready condition. Each ends the session for good.
const (
	// InstanceConsoleSessionReasonCompleted means the command exited on its
	// own, with any exit code. status.exitCode carries the code.
	InstanceConsoleSessionReasonCompleted = "Completed"

	// InstanceConsoleSessionReasonExpired means the command ran until
	// status.expiresAt and was stopped.
	InstanceConsoleSessionReasonExpired = "Expired"

	// InstanceConsoleSessionReasonRevoked means the session was deleted before
	// it ended.
	InstanceConsoleSessionReasonRevoked = "Revoked"

	// InstanceConsoleSessionReasonNotConnected means no client connected before
	// status.connectBefore.
	InstanceConsoleSessionReasonNotConnected = "NotConnected"

	// InstanceConsoleSessionReasonAgentShutdown means the platform stopped the
	// session for maintenance.
	InstanceConsoleSessionReasonAgentShutdown = "AgentShutdown"

	// InstanceConsoleSessionReasonAgentLost means the platform lost the
	// session unexpectedly.
	InstanceConsoleSessionReasonAgentLost = "AgentLost"

	// InstanceConsoleSessionReasonTooManySessions means the instance already
	// has as many open sessions as it allows.
	InstanceConsoleSessionReasonTooManySessions = "TooManySessions"

	// InstanceConsoleSessionReasonNoShell means the container has no shell,
	// which the platform needs to manage the command.
	InstanceConsoleSessionReasonNoShell = "NoShell"

	// InstanceConsoleSessionReasonCommandUnavailable means the container does
	// not have the requested executable.
	InstanceConsoleSessionReasonCommandUnavailable = "CommandUnavailable"

	// InstanceConsoleSessionReasonInstanceNotRunning means the instance or the
	// container is not running.
	InstanceConsoleSessionReasonInstanceNotRunning = "InstanceNotRunning"

	// InstanceConsoleSessionReasonInstanceNotFound means the instance named in
	// spec.instanceRef no longer exists or has been replaced.
	InstanceConsoleSessionReasonInstanceNotFound = "InstanceNotFound"

	// InstanceConsoleSessionReasonInvalid means the request cannot be served
	// as written. The message says why.
	InstanceConsoleSessionReasonInvalid = "Invalid"

	// InstanceConsoleSessionReasonUnavailable means no cell took the session in
	// time, so sessions are not available for the instance right now.
	InstanceConsoleSessionReasonUnavailable = "Unavailable"

	// InstanceConsoleSessionReasonDisconnected means the client went away
	// before the command exited, and the platform stopped the command.
	InstanceConsoleSessionReasonDisconnected = "Disconnected"
)

// Finalizers on InstanceConsoleSessions.
const (
	// InstanceConsoleSessionFinalizer holds a project session until the cell
	// has confirmed its processes are stopped, or the confirmation times out.
	InstanceConsoleSessionFinalizer = "compute.datumapis.com/instance-console-session"

	// InstanceConsoleSessionAgentFinalizer holds the cell copy of a session
	// until the shell agent has stopped its processes and released its slot.
	InstanceConsoleSessionAgentFinalizer = "compute.datumapis.com/shell-agent"
)

// Annotations on copies of InstanceConsoleSessions.
const (
	// InstanceConsoleSessionRevokeAnnotation asks the cell to end a session as
	// Revoked. The control plane sets it on the hub copy when the project
	// session is deleted, so the cell can report the session's end through
	// status before the copy goes away. Deleting a copy also ends its session,
	// but a deleted copy can report nothing back.
	InstanceConsoleSessionRevokeAnnotation = "compute.datumapis.com/revoke"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=ics
// +kubebuilder:metadata:annotations="discovery.miloapis.com/parent-contexts=Project"

// InstanceConsoleSession runs one command in one container of an instance and
// connects a client to it, such as an interactive shell. Deleting the session
// stops the command.
//
// Each session is one invocation, so clients create sessions with generateName.
//
// +kubebuilder:printcolumn:name="Instance",type=string,JSONPath=`.spec.instanceRef.name`
// +kubebuilder:printcolumn:name="Container",type=string,JSONPath=`.spec.containerName`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].reason`
// +kubebuilder:printcolumn:name="Expires",type=string,JSONPath=`.status.expiresAt`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:printcolumn:name="Connect Before",type=string,JSONPath=`.status.connectBefore`,priority=1
// +kubebuilder:printcolumn:name="Message",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].message`,priority=1
type InstanceConsoleSession struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +kubebuilder:validation:Required
	// +kubebuilder:validation:XValidation:message="spec is immutable",rule="self == oldSelf"
	Spec InstanceConsoleSessionSpec `json:"spec"`

	// +kubebuilder:default={conditions:{{type:"Ready",status:"Unknown",reason:"Pending",message:"Waiting for the session to be prepared",lastTransitionTime:"1970-01-01T00:00:00Z"}}}
	Status InstanceConsoleSessionStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// InstanceConsoleSessionList contains a list of InstanceConsoleSession objects.
type InstanceConsoleSessionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []InstanceConsoleSession `json:"items"`
}

func init() {
	SchemeBuilder.Register(&InstanceConsoleSession{}, &InstanceConsoleSessionList{})
}
