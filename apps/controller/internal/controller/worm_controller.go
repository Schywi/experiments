package controller

import (
	"context"

	api "github.com/lucasmirandoliveira/experiments/apps/controller/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type WormReconciler struct{ client.Client }

func (r *WormReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	worm := &api.Worm{}
	if err := r.Get(ctx, req.NamespacedName, worm); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if worm.Spec.MaxReplicas < 1 || worm.Spec.WorkerDeploymentName == "" {
		return ctrl.Result{}, nil
	}
	desired := worm.Status.DesiredReplicas
	if desired < 1 {
		desired = 1
	}
	if desired > worm.Spec.MaxReplicas {
		desired = worm.Spec.MaxReplicas
	}
	deployment := &appsv1.Deployment{}
	key := types.NamespacedName{Namespace: worm.Namespace, Name: worm.Spec.WorkerDeploymentName}
	if err := r.Get(ctx, key, deployment); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	scale := &autoscalingv1.Scale{}
	if err := r.SubResource("scale").Get(ctx, deployment, scale); err != nil {
		return ctrl.Result{}, err
	}
	if scale.Spec.Replicas != desired {
		scale.Spec.Replicas = desired
		if err := r.SubResource("scale").Update(ctx, deployment, client.WithSubResourceBody(scale)); err != nil {
			return ctrl.Result{}, err
		}
	}
	if worm.Status.DesiredReplicas != desired || worm.Status.CurrentReplicas != scale.Status.Replicas || worm.Status.ObservedGeneration != worm.Generation {
		base := worm.DeepCopy()
		worm.Status.DesiredReplicas = desired
		worm.Status.CurrentReplicas = scale.Status.Replicas
		worm.Status.ObservedGeneration = worm.Generation
		if err := r.Status().Patch(ctx, worm, client.MergeFrom(base)); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{}, nil
}
func (r *WormReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).For(&api.Worm{}).Named("worm").Complete(r)
}
