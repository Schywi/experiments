package intent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	api "github.com/lucasmirandoliveira/experiments/apps/controller/api/v1alpha1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const MaxTrackedIntentIDs = 1024

var ErrCapReached = errors.New("maximum replicas reached")

// Service atomically records replication intent acceptance in a single Worm status.
type Service struct {
	Client client.Client
	Worm   types.NamespacedName
}

// Accept increments desired replicas once for a previously unseen intent ID.
// It returns the resulting desired count. A duplicate is accepted idempotently.
func (s Service) Accept(ctx context.Context, wormID, intentID string) (int32, bool, error) {
	if strings.TrimSpace(wormID) == "" || strings.TrimSpace(intentID) == "" {
		return 0, false, fmt.Errorf("worm_id and intent_id are required")
	}
	var desired int32
	var duplicate bool
	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		worm := &api.Worm{}
		if err := s.Client.Get(ctx, s.Worm, worm); err != nil {
			return err
		}
		for _, accepted := range worm.Status.AcceptedIntentIDs {
			if accepted == intentID {
				desired, duplicate = worm.Status.DesiredReplicas, true
				return nil
			}
		}
		if worm.Status.DesiredReplicas >= worm.Spec.MaxReplicas {
			return ErrCapReached
		}
		worm.Status.DesiredReplicas++
		worm.Status.AcceptedIntentIDs = append(worm.Status.AcceptedIntentIDs, intentID)
		if len(worm.Status.AcceptedIntentIDs) > MaxTrackedIntentIDs {
			worm.Status.AcceptedIntentIDs = worm.Status.AcceptedIntentIDs[len(worm.Status.AcceptedIntentIDs)-MaxTrackedIntentIDs:]
		}
		desired = worm.Status.DesiredReplicas
		return s.Client.Status().Update(ctx, worm)
	})
	return desired, duplicate, err
}
