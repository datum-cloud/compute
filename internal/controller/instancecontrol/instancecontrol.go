package instancecontrol

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"go.datum.net/compute/api/v1alpha"
)

type Strategy interface {
	// GetActions returns a set of actions that should be taken in order to drive
	// the workload deployment to the desired state. Some actions may be informational,
	// such as providing context into pending actions when instance management
	// policies may require waiting for pod readiness.
	GetActions(
		ctx context.Context,
		scheme *runtime.Scheme,
		deployment *v1alpha.WorkloadDeployment,
		currentInstances []v1alpha.Instance,
	) ([]Action, error)
}

type ActionType string

const (
	ActionTypeCreate ActionType = "Create"
	ActionTypeUpdate ActionType = "Update"
	ActionTypeDelete ActionType = "Delete"
	ActionTypeWait   ActionType = "Wait"
)

type Action struct {
	Object        client.Object
	actionType    ActionType
	skipExecution bool
	fn            func(ctx context.Context, c client.Client) error
}

func (a Action) Execute(ctx context.Context, c client.Client) error {
	if a.skipExecution {
		return nil
	}

	return a.fn(ctx, c)
}

func (a *Action) SkipExecution() {
	a.skipExecution = true
}

func (a Action) IsSkipped() bool {
	return a.skipExecution
}

func (a Action) ActionType() ActionType {
	return a.actionType
}

func NewCreateAction(object client.Object) Action {
	return Action{
		Object:     object,
		actionType: ActionTypeCreate,
		fn: func(ctx context.Context, c client.Client) error {
			if err := c.Create(ctx, object); err != nil {
				return fmt.Errorf("failed to create %T: %w", object, err)
			}

			return nil
		},
	}
}

func NewUpdateAction(object client.Object) Action {
	return Action{
		Object:     object,
		actionType: ActionTypeUpdate,
		fn: func(ctx context.Context, c client.Client) error {
			if err := c.Update(ctx, object); err != nil {
				return fmt.Errorf("failed to update %T: %w", object, err)
			}

			return nil
		},
	}
}

func NewDeleteAction(object client.Object) Action {
	return Action{
		Object:     object,
		actionType: ActionTypeDelete,
		fn: func(ctx context.Context, c client.Client) error {
			return c.Delete(ctx, object)
		},
	}
}

func NewWaitAction(object client.Object) Action {
	return Action{
		Object:     object,
		actionType: ActionTypeWait,
		fn:         func(ctx context.Context, c client.Client) error { return nil },
	}
}
