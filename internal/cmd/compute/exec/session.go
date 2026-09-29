package exec

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"sigs.k8s.io/controller-runtime/pkg/client"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/cmd/compute/util"
	"go.datum.net/compute/internal/consoleclient"
)

const readyTimeout = time.Minute

var errTooManySessions = errors.New("too many open sessions in this project")

func createSession(ctx context.Context, c client.Client, opts *options, tty bool, publicKey string) (*computev1alpha.InstanceConsoleSession, error) {
	var inst computev1alpha.Instance
	if err := c.Get(ctx, types.NamespacedName{Namespace: util.ResourceNamespace, Name: opts.instance}, &inst); err != nil {
		if k8serrors.IsNotFound(err) {
			return nil, fmt.Errorf("instance %q not found", opts.instance)
		}
		return nil, fmt.Errorf("getting instance: %w", err)
	}
	container, err := pickContainer(&inst, opts.container)
	if err != nil {
		return nil, err
	}

	session := &computev1alpha.InstanceConsoleSession{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:    util.ResourceNamespace,
			GenerateName: inst.Name + "-",
		},
		Spec: computev1alpha.InstanceConsoleSessionSpec{
			InstanceRef:     computev1alpha.InstanceConsoleSessionInstanceRef{Name: inst.Name, UID: inst.UID},
			ContainerName:   container,
			Command:         opts.command,
			Stdin:           opts.stdin,
			Terminal:        tty,
			ClientPublicKey: publicKey,
		},
	}
	if err := c.Create(ctx, session); err != nil {
		return nil, createError(err)
	}
	return session, nil
}

func pickContainer(inst *computev1alpha.Instance, name string) (string, error) {
	var names []string
	if sb := inst.Spec.Runtime.Sandbox; sb != nil {
		for _, c := range sb.Containers {
			names = append(names, c.Name)
		}
	}
	switch {
	case len(names) == 0:
		return "", fmt.Errorf("instance %q has no containers to run a command in", inst.Name)
	case name != "":
		for _, n := range names {
			if n == name {
				return name, nil
			}
		}
		return "", fmt.Errorf("instance %q has no container %q; it runs: %s", inst.Name, name, strings.Join(names, ", "))
	case len(names) == 1:
		return names[0], nil
	default:
		return "", &util.ExitError{Code: 2, Err: fmt.Errorf(
			"instance %q runs several containers; choose one with -c: %s", inst.Name, strings.Join(names, ", "))}
	}
}

func createError(err error) error {
	if k8serrors.IsForbidden(err) && strings.Contains(err.Error(), "reached your quota") {
		return errTooManySessions
	}
	return fmt.Errorf("creating session: %w", err)
}

// waitReady watches the session until the platform says where to connect, or
// ends it first.
func waitReady(ctx context.Context, c client.WithWatch, s *computev1alpha.InstanceConsoleSession, timeout time.Duration) (*computev1alpha.InstanceConsoleSession, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	key := client.ObjectKeyFromObject(s)

	for {
		w, err := c.Watch(ctx, &computev1alpha.InstanceConsoleSessionList{},
			client.InNamespace(key.Namespace), client.MatchingFields{"metadata.name": key.Name})
		if err != nil {
			return nil, fmt.Errorf("watching session: %w", err)
		}
		if err := c.Get(ctx, key, s); err != nil {
			w.Stop()
			return nil, fmt.Errorf("getting session: %w", err)
		}
		if done, err := readiness(s); done {
			w.Stop()
			return s, err
		}
		got, done, err := nextReadiness(ctx, w, key.Name)
		w.Stop()
		if done {
			return got, err
		}
		if ctx.Err() != nil {
			return nil, fmt.Errorf("the platform did not prepare the session within %s", timeout)
		}
	}
}

func nextReadiness(ctx context.Context, w watch.Interface, name string) (*computev1alpha.InstanceConsoleSession, bool, error) {
	for {
		select {
		case <-ctx.Done():
			return nil, false, nil
		case ev, ok := <-w.ResultChan():
			if !ok {
				return nil, false, nil
			}
			if ev.Type == watch.Deleted {
				return nil, true, errors.New("the session was deleted before it was ready")
			}
			s, ok := ev.Object.(*computev1alpha.InstanceConsoleSession)
			if !ok || s.Name != name {
				continue
			}
			if done, err := readiness(s); done {
				return s, true, err
			}
		}
	}
}

// readiness reports whether waiting is over: the session is ready to connect,
// or it ended, in which case the error says why.
func readiness(s *computev1alpha.InstanceConsoleSession) (bool, error) {
	if res, ok := ending(s); ok {
		return true, exitFor(res, nil)
	}
	cond := meta.FindStatusCondition(s.Status.Conditions, computev1alpha.InstanceConsoleSessionReady)
	if cond == nil || cond.Status != metav1.ConditionTrue {
		return false, nil
	}
	if cond.Reason == computev1alpha.InstanceConsoleSessionReasonConnected {
		return true, errors.New("another client already connected to the session")
	}
	return s.Status.Connection != nil, nil
}

// ending reports how a session ended according to its status.
func ending(s *computev1alpha.InstanceConsoleSession) (consoleclient.Result, bool) {
	cond := meta.FindStatusCondition(s.Status.Conditions, computev1alpha.InstanceConsoleSessionReady)
	if cond == nil || cond.Status != metav1.ConditionFalse {
		return consoleclient.Result{}, false
	}
	if cond.Reason == computev1alpha.InstanceConsoleSessionReasonCompleted && s.Status.ExitCode != nil {
		return consoleclient.Result{ExitCode: int(*s.Status.ExitCode)}, true
	}
	return consoleclient.Result{ExitCode: 1, Reason: cond.Reason, Message: cond.Message}, true
}

func connectionOf(s *computev1alpha.InstanceConsoleSession) consoleclient.Connection {
	return consoleclient.Connection{
		EndpointID: s.Status.Connection.EndpointID,
		RelayURLs:  s.Status.Connection.RelayURLs,
		Target:     s.Status.Connection.Target,
	}
}

// exitFor turns how a session ended into the plugin's exit: the command's own
// code, or 1 with the reason when the platform ended it.
func exitFor(res consoleclient.Result, err error) error {
	switch {
	case err != nil:
		return err
	case res.PlatformEnded():
		msg := "session ended: " + res.Reason
		if res.Message != "" {
			msg += ": " + res.Message
		}
		return &util.ExitError{Code: 1, Err: errors.New(msg)}
	case res.ExitCode != 0:
		return &util.ExitError{Code: res.ExitCode}
	default:
		return nil
	}
}

// deleteSession ends the session and releases its quota. It builds a fresh
// client because the session may have outlived the original token.
func deleteSession(project string, s *computev1alpha.InstanceConsoleSession) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := util.NewClient(project)
	if err != nil {
		return
	}
	_ = client.IgnoreNotFound(c.Delete(ctx, s))
}
