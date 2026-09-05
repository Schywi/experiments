package intent

import (
	"context"
	"errors"
	"sync"
	"testing"

	api "github.com/lucasmirandoliveira/experiments/apps/controller/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func newService(t *testing.T, max, desired int32) (Service, func() *api.Worm) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	worm := &api.Worm{ObjectMeta: metav1.ObjectMeta{Name: "example", Namespace: "lab"}, Spec: api.WormSpec{WorkerDeploymentName: "worker", MaxReplicas: max}, Status: api.WormStatus{DesiredReplicas: desired}}
	client := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(worm).WithObjects(worm).Build()
	service := Service{Client: client, Worm: types.NamespacedName{Namespace: "lab", Name: "example"}}
	return service, func() *api.Worm {
		result := &api.Worm{}
		if err := client.Get(context.Background(), service.Worm, result); err != nil {
			t.Fatal(err)
		}
		return result
	}
}

func TestAcceptCapsAndIsIdempotent(t *testing.T) {
	service, get := newService(t, 2, 0)
	if desired, duplicate, err := service.Accept(context.Background(), "pod-a", "intent-a"); err != nil || duplicate || desired != 1 {
		t.Fatalf("first accept = (%d, %t, %v), want (1, false, nil)", desired, duplicate, err)
	}
	if desired, duplicate, err := service.Accept(context.Background(), "pod-a", "intent-a"); err != nil || !duplicate || desired != 1 {
		t.Fatalf("duplicate = (%d, %t, %v), want (1, true, nil)", desired, duplicate, err)
	}
	if _, _, err := service.Accept(context.Background(), "pod-b", "intent-b"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.Accept(context.Background(), "pod-c", "intent-c"); !errors.Is(err, ErrCapReached) {
		t.Fatalf("err = %v, want cap reached", err)
	}
	if got := get().Status; got.DesiredReplicas != 2 || len(got.AcceptedIntentIDs) != 2 {
		t.Fatalf("unexpected status: %#v", got)
	}
}

func TestAcceptConcurrentDuplicateRetries(t *testing.T) {
	service, get := newService(t, 10, 0)
	const requests = 12
	errs := make(chan error, requests)
	var wg sync.WaitGroup
	for range requests {
		wg.Go(func() { _, _, err := service.Accept(context.Background(), "pod-a", "same-intent"); errs <- err })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	status := get().Status
	if status.DesiredReplicas != 1 || len(status.AcceptedIntentIDs) != 1 {
		t.Fatalf("retry race incremented more than once: %#v", status)
	}
}
