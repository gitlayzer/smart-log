/*
Copyright 2025 gitlayzer.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"

	smartlogv1alpha1 "github.com/gitlayzer/smart-log/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// AlertGroupReconciler reconciles a AlertGroup object
type AlertGroupReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=smartlog.smart-tools.com,resources=alertgroups,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=smartlog.smart-tools.com,resources=alertgroups/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=smartlog.smart-tools.com,resources=alertgroups/finalizers,verbs=update
// +kubebuilder:rbac:groups=smartlog.smart-tools.com,resources=alerts,verbs=get;list;watch

func (r *AlertGroupReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var alertGroup smartlogv1alpha1.AlertGroup
	if err := r.Get(ctx, req.NamespacedName, &alertGroup); err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("AlertGroup resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get AlertGroup")
		return ctrl.Result{}, err
	}

	readyMembers, validationErr := r.validateAlertGroupMembers(ctx, &alertGroup)

	alertGroup.Status.TotalAlerts = len(alertGroup.Spec.AlertNames)
	alertGroup.Status.ReadyAlerts = readyMembers

	readyCondition := metav1.Condition{
		Type:    "Ready",
		Status:  metav1.ConditionUnknown,
		Reason:  "Validating",
		Message: "Validating alert group members",
	}

	if validationErr == nil {
		readyCondition.Status = metav1.ConditionTrue
		readyCondition.Reason = "ValidationSucceeded"
		readyCondition.Message = "All alert members are valid and ready"
	} else {
		readyCondition.Status = metav1.ConditionFalse
		readyCondition.Reason = "ValidationFailed"
		readyCondition.Message = validationErr.Error()
	}

	meta.SetStatusCondition(&alertGroup.Status.Conditions, readyCondition)

	if err := r.Status().Update(ctx, &alertGroup); err != nil {
		logger.Error(err, "Failed to update AlertGroup status")
		return ctrl.Result{}, err
	}

	logger.Info("Successfully reconciled AlertGroup status")
	return ctrl.Result{}, nil
}

func (r *AlertGroupReconciler) validateAlertGroupMembers(ctx context.Context, alertGroup *smartlogv1alpha1.AlertGroup) (int, error) {
	logger := log.FromContext(ctx)
	readyCount := 0

	for _, alertName := range alertGroup.Spec.AlertNames {
		var alert smartlogv1alpha1.Alert
		alertKey := types.NamespacedName{
			Name:      alertName,
			Namespace: alertGroup.Namespace,
		}

		if err := r.Get(ctx, alertKey, &alert); err != nil {
			if apierrors.IsNotFound(err) {
				logger.Info("Member alert not found", "alert", alertKey.String())
				return readyCount, fmt.Errorf("member alert '%s' not found", alertName)
			}
			logger.Error(err, "Failed to get member alert", "alert", alertKey.String())
			return readyCount, fmt.Errorf("failed to get member alert '%s': %w", alertName, err)
		}

		if !meta.IsStatusConditionTrue(alert.Status.Conditions, "Ready") {
			logger.Info("Member alert is not ready", "alert", alertKey.String())
			return readyCount, fmt.Errorf("member alert '%s' is not ready", alertName)
		}

		readyCount++
	}

	return readyCount, nil
}

func (r *AlertGroupReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&smartlogv1alpha1.AlertGroup{}).
		Watches(
			&smartlogv1alpha1.Alert{},
			handler.EnqueueRequestsFromMapFunc(r.findAlertGroupsForAlert),
		).
		Complete(r)
}

func (r *AlertGroupReconciler) findAlertGroupsForAlert(ctx context.Context, alertObj client.Object) []reconcile.Request {
	alert, ok := alertObj.(*smartlogv1alpha1.Alert)
	if !ok {
		return []reconcile.Request{}
	}

	var allAlertGroups smartlogv1alpha1.AlertGroupList
	if err := r.List(ctx, &allAlertGroups, client.InNamespace(alert.GetNamespace())); err != nil {
		return []reconcile.Request{}
	}

	requests := make([]reconcile.Request, 0)
	for _, group := range allAlertGroups.Items {
		for _, memberName := range group.Spec.AlertNames {
			if memberName == alert.Name {
				requests = append(requests, reconcile.Request{
					NamespacedName: types.NamespacedName{
						Name:      group.Name,
						Namespace: group.Namespace,
					},
				})
				break
			}
		}
	}
	return requests
}
