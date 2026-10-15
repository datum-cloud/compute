package exec

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	quotav1alpha1 "go.miloapis.com/milo/pkg/apis/quota/v1alpha1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/cmd/compute/util"
	"go.datum.net/compute/internal/consoleclient"
)

const (
	web    = "web"
	worker = "worker"

	instanceName = "api-0"
)

func exitCode(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		return 0
	}
	var exitErr *util.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.Code
	}
	return 1
}

func TestArguments(t *testing.T) {
	cases := []struct {
		name        string
		args        []string
		wantCode    int
		wantErr     string
		wantCommand []string
	}{
		{name: "command after dash", args: []string{instanceName, "--", "ls", "-la"}, wantCommand: []string{"ls", "-la"}},
		{name: "flags before dash", args: []string{instanceName, "-it", "-c", web, "--", "sh"}, wantCommand: []string{"sh"}},
		{name: "no arguments", args: nil, wantCode: 2, wantErr: "name an instance"},
		{name: "no dash", args: []string{instanceName, "sh"}, wantCode: 2, wantErr: "separate the command with --"},
		{name: "no command", args: []string{instanceName, "--"}, wantCode: 2, wantErr: "name a command"},
		{name: "two instances", args: []string{instanceName, "api-1", "--", "sh"}, wantCode: 2, wantErr: "exactly one instance"},
		{name: "unknown flag", args: []string{instanceName, "--bogus", "--", "sh"}, wantCode: 2, wantErr: "unknown flag"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotCommand []string
			cmd := newCommand(func(_ *cobra.Command, opts *options) error {
				gotCommand = opts.command
				return nil
			})
			cmd.SetArgs(tc.args)
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)

			err := cmd.Execute()
			if code := exitCode(t, err); code != tc.wantCode {
				t.Fatalf("Execute(%q) exit = %d (%v), want %d", tc.args, code, err, tc.wantCode)
			}
			if tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("Execute(%q) error = %v, want it to mention %q", tc.args, err, tc.wantErr)
			}
			if tc.wantCommand != nil && strings.Join(gotCommand, " ") != strings.Join(tc.wantCommand, " ") {
				t.Errorf("command = %q, want %q", gotCommand, tc.wantCommand)
			}
		})
	}
}

func instanceWith(containers ...string) *computev1alpha.Instance {
	inst := &computev1alpha.Instance{ObjectMeta: metav1.ObjectMeta{Name: instanceName}}
	if len(containers) > 0 {
		inst.Spec.Runtime.Sandbox = &computev1alpha.SandboxRuntime{}
		for _, c := range containers {
			inst.Spec.Runtime.Sandbox.Containers = append(inst.Spec.Runtime.Sandbox.Containers,
				computev1alpha.SandboxContainer{Name: c})
		}
	}
	return inst
}

func TestPickContainer(t *testing.T) {
	cases := []struct {
		name       string
		containers []string
		flag       string
		want       string
		wantCode   int
	}{
		{name: "only container", containers: []string{web}, want: web},
		{name: "named container", containers: []string{web, worker}, flag: worker, want: worker},
		{name: "several without flag", containers: []string{web, worker}, wantCode: 2},
		{name: "unknown container", containers: []string{web}, flag: "db", wantCode: 1},
		{name: "virtual machine", wantCode: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := pickContainer(instanceWith(tc.containers...), tc.flag)
			if code := exitCode(t, err); code != tc.wantCode {
				t.Fatalf("pickContainer() exit = %d (%v), want %d", code, err, tc.wantCode)
			}
			if got != tc.want {
				t.Errorf("pickContainer() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestExitFor(t *testing.T) {
	cases := []struct {
		name     string
		res      consoleclient.Result
		wantCode int
		wantErr  string
	}{
		{name: "success", res: consoleclient.Result{}},
		{name: "command failed", res: consoleclient.Result{ExitCode: 3}, wantCode: 3},
		{name: "platform ending", res: consoleclient.Result{ExitCode: 1, Reason: "Expired", Message: "The session expired."},
			wantCode: 1, wantErr: "session ended: Expired: The session expired."},
		{name: "unavailable", res: consoleclient.Result{ExitCode: 1, Reason: computev1alpha.InstanceConsoleSessionReasonUnavailable},
			wantCode: 1, wantErr: "session ended: Unavailable: No part of Datum took the session in time. Try again shortly."},
		{name: "unavailable with message", res: consoleclient.Result{ExitCode: 1, Reason: computev1alpha.InstanceConsoleSessionReasonUnavailable, Message: "No cell took it."},
			wantCode: 1, wantErr: "session ended: Unavailable: No cell took it."},
		{name: "disconnected", res: consoleclient.Result{ExitCode: 1, Reason: computev1alpha.InstanceConsoleSessionReasonDisconnected},
			wantCode: 1, wantErr: "session ended: Disconnected: The connection to the session was lost, so Datum stopped the command."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := exitFor(tc.res, nil)
			if code := exitCode(t, err); code != tc.wantCode {
				t.Fatalf("exitFor() exit = %d, want %d", code, tc.wantCode)
			}
			var exitErr *util.ExitError
			if errors.As(err, &exitErr) && (exitErr.Err == nil) != (tc.wantErr == "") {
				t.Fatalf("exitFor() message = %v, want %q", exitErr.Err, tc.wantErr)
			}
			if tc.wantErr != "" && err.Error() != tc.wantErr {
				t.Errorf("exitFor() = %q, want %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestCreateError(t *testing.T) {
	gr := schema.GroupResource{Group: "compute.datumapis.com", Resource: "instanceconsolesessions"}
	quota := k8serrors.NewForbidden(gr, "", errors.New(
		"You've reached your quota for this resource type (need 1, 0 available). Delete unused resources to free up capacity, or contact support to request a higher limit."))

	cases := []struct {
		name     string
		err      error
		c        client.Client
		want     string
		contains bool
	}{
		{name: "not enabled", err: quota, c: newFakeClient(t, sessionBucket(0)), want: "shell sessions aren't enabled for this project"},
		{name: "full", err: quota, c: newFakeClient(t, sessionBucket(3)), want: "too many open sessions in this project"},
		{name: "no allowance", err: quota, c: newFakeClient(t), want: "shell sessions aren't enabled for this project"},
		{name: "allowance unreadable", err: quota, c: forbiddenLister{newFakeClient(t)},
			want: "shell sessions aren't enabled for this project, or too many are open in it"},
		{name: "not a quota denial", err: k8serrors.NewForbidden(gr, "", errors.New("user cannot create instanceconsolesessions")),
			c: newFakeClient(t), want: "user cannot create", contains: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := createError(context.Background(), tc.c, tc.err)
			if code := exitCode(t, err); code != 1 {
				t.Errorf("createError() exit = %d, want 1", code)
			}
			if tc.contains && !strings.Contains(err.Error(), tc.want) || !tc.contains && err.Error() != tc.want {
				t.Errorf("createError() = %q, want %q", err, tc.want)
			}
		})
	}
}

func sessionBucket(limit int64) *quotav1alpha1.AllowanceBucket {
	return &quotav1alpha1.AllowanceBucket{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "milo-system",
			Name:      "project-sessions",
			Labels:    map[string]string{"quota.miloapis.com/consumer-kind": "Project"},
		},
		Spec:   quotav1alpha1.AllowanceBucketSpec{ResourceType: sessionQuotaType},
		Status: quotav1alpha1.AllowanceBucketStatus{Limit: limit, Allocated: limit},
	}
}

type forbiddenLister struct {
	client.WithWatch
}

func (forbiddenLister) List(context.Context, client.ObjectList, ...client.ListOption) error {
	return k8serrors.NewForbidden(schema.GroupResource{Group: "quota.miloapis.com", Resource: "allowancebuckets"}, "", errors.New("denied"))
}

func session(cond *metav1.Condition, connection bool) *computev1alpha.InstanceConsoleSession {
	s := &computev1alpha.InstanceConsoleSession{ObjectMeta: metav1.ObjectMeta{Name: "api-0-abcde", Namespace: util.ResourceNamespace}}
	if cond != nil {
		cond.Type = computev1alpha.InstanceConsoleSessionReady
		s.Status.Conditions = []metav1.Condition{*cond}
	}
	if connection {
		s.Status.Connection = &computev1alpha.InstanceConsoleSessionConnection{EndpointID: "ep", RelayURLs: []string{"https://relay"}, Target: "agent:7777"}
	}
	return s
}

func TestReadiness(t *testing.T) {
	cases := []struct {
		name     string
		session  *computev1alpha.InstanceConsoleSession
		wantDone bool
		wantErr  string
	}{
		{name: "no status", session: session(nil, false)},
		{name: "pending", session: session(&metav1.Condition{Status: metav1.ConditionUnknown, Reason: computev1alpha.InstanceConsoleSessionReasonPending}, false)},
		{name: "ready", session: session(&metav1.Condition{Status: metav1.ConditionTrue, Reason: computev1alpha.InstanceConsoleSessionReasonSessionReady}, true), wantDone: true},
		{name: "ready without connection", session: session(&metav1.Condition{Status: metav1.ConditionTrue, Reason: computev1alpha.InstanceConsoleSessionReasonSessionReady}, false)},
		{name: "taken", session: session(&metav1.Condition{Status: metav1.ConditionTrue, Reason: computev1alpha.InstanceConsoleSessionReasonConnected}, true),
			wantDone: true, wantErr: "another client"},
		{name: "refused", session: session(&metav1.Condition{Status: metav1.ConditionFalse, Reason: computev1alpha.InstanceConsoleSessionReasonNoShell, Message: "The container has no shell."}, false),
			wantDone: true, wantErr: "session ended: NoShell: The container has no shell."},
		{name: "unavailable", session: session(&metav1.Condition{Status: metav1.ConditionFalse, Reason: computev1alpha.InstanceConsoleSessionReasonUnavailable}, false),
			wantDone: true, wantErr: "session ended: Unavailable: No part of Datum took the session in time."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			done, err := readiness(tc.session)
			if done != tc.wantDone {
				t.Errorf("readiness() done = %v, want %v", done, tc.wantDone)
			}
			if (err != nil) != (tc.wantErr != "") || (err != nil && !strings.Contains(err.Error(), tc.wantErr)) {
				t.Errorf("readiness() error = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestEndingUsesRecordedExitCode(t *testing.T) {
	s := session(&metav1.Condition{Status: metav1.ConditionFalse, Reason: computev1alpha.InstanceConsoleSessionReasonCompleted}, false)
	s.Status.ExitCode = ptr.To[int32](7)
	if res, ok := ending(s); !ok || res != (consoleclient.Result{ExitCode: 7}) {
		t.Errorf("ending() = %+v, %v; want exit 7", res, ok)
	}
}

func newFakeClient(t *testing.T, objs ...client.Object) client.WithWatch {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := computev1alpha.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := quotav1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).
		WithStatusSubresource(&computev1alpha.InstanceConsoleSession{}).Build()
}

func TestWaitReady(t *testing.T) {
	pending := session(&metav1.Condition{Status: metav1.ConditionUnknown, Reason: computev1alpha.InstanceConsoleSessionReasonPending}, false)
	c := newFakeClient(t, pending)

	go func() {
		time.Sleep(50 * time.Millisecond)
		s := &computev1alpha.InstanceConsoleSession{}
		if err := c.Get(context.Background(), client.ObjectKeyFromObject(pending), s); err != nil {
			t.Error(err)
			return
		}
		ready := session(&metav1.Condition{Status: metav1.ConditionTrue, Reason: computev1alpha.InstanceConsoleSessionReasonSessionReady, LastTransitionTime: metav1.Now()}, true)
		s.Status = ready.Status
		if err := c.Status().Update(context.Background(), s); err != nil {
			t.Error(err)
		}
	}()

	got, err := waitReady(context.Background(), c, pending.DeepCopy(), 5*time.Second)
	if err != nil {
		t.Fatalf("waitReady() error = %v", err)
	}
	if got.Status.Connection == nil || got.Status.Connection.Target != "agent:7777" {
		t.Errorf("waitReady() connection = %+v", got.Status.Connection)
	}
}

func TestWaitReadyTimesOut(t *testing.T) {
	pending := session(&metav1.Condition{Status: metav1.ConditionUnknown, Reason: computev1alpha.InstanceConsoleSessionReasonPending}, false)
	c := newFakeClient(t, pending)

	_, err := waitReady(context.Background(), c, pending.DeepCopy(), 100*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "did not prepare the session") {
		t.Fatalf("waitReady() error = %v, want a timeout", err)
	}
}

func TestClosedOnSignal(t *testing.T) {
	interruptedCtx, cancel := context.WithCancel(context.Background())
	cancel()
	closed := consoleclient.Result{ExitCode: 1, Reason: computev1alpha.InstanceConsoleSessionReasonClosedByUser}
	tests := []struct {
		name string
		ctx  context.Context
		res  consoleclient.Result
		err  error
		want bool
	}{
		{name: "agent confirmed the close datumctl sent on a signal", ctx: interruptedCtx, res: closed, want: true},
		{name: "stream broke after a signal", ctx: interruptedCtx, err: errors.New("closed"), want: true},
		{name: "command exited as the signal arrived", ctx: interruptedCtx, res: consoleclient.Result{ExitCode: 3}},
		{name: "another client closed the session", ctx: context.Background(), res: closed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := closedOnSignal(tt.ctx, tt.res, tt.err); got != tt.want {
				t.Fatalf("closedOnSignal() = %v, want %v", got, tt.want)
			}
		})
	}
	var exit *util.ExitError
	if err := interrupted(interruptedCtx, nil); !errors.As(err, &exit) || exit.Code != exitInterrupted {
		t.Fatalf("interrupted() = %v, want exit %d", err, exitInterrupted)
	}
}
