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
	corev1 "k8s.io/api/core/v1"
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

// AlertReconciler reconciles a Alert object
type AlertReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=smartlog.smart-tools.com,resources=alerts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=smartlog.smart-tools.com,resources=alerts/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=smartlog.smart-tools.com,resources=alerts/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

func (r *AlertReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var alert smartlogv1alpha1.Alert
	if err := r.Get(ctx, req.NamespacedName, &alert); err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("Alert resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get Alert")
		return ctrl.Result{}, err
	}

	isValid, validationErr := r.validateAlert(ctx, &alert)

	readyCondition := metav1.Condition{
		Type:    "Ready",
		Status:  metav1.ConditionUnknown,
		Reason:  "Validating",
		Message: "Validating alert configuration",
	}

	if isValid {
		readyCondition.Status = metav1.ConditionTrue
		readyCondition.Reason = "ValidationSucceeded"
		readyCondition.Message = "Alert configuration is valid"
	} else {
		readyCondition.Status = metav1.ConditionFalse
		readyCondition.Reason = "ValidationFailed"
		readyCondition.Message = validationErr.Error()
	}

	meta.SetStatusCondition(&alert.Status.Conditions, readyCondition)

	if err := r.Status().Update(ctx, &alert); err != nil {
		logger.Error(err, "Failed to update Alert status")
		return ctrl.Result{}, err
	}

	logger.Info("Successfully reconciled Alert status", "Ready", isValid)
	return ctrl.Result{}, nil
}

func (r *AlertReconciler) validateAlert(ctx context.Context, alert *smartlogv1alpha1.Alert) (bool, error) {
	logger := log.FromContext(ctx)

	if alert.Spec.Type != "Webhook" {
		return false, fmt.Errorf("unsupported alert type: %s", alert.Spec.Type)
	}

	if alert.Spec.Webhook == nil {
		return false, fmt.Errorf("webhook spec is not defined for alert type Webhook")
	}

	webhookSpec := alert.Spec.Webhook
	secretName := webhookSpec.URLSecretRef.Name
	secretKey := webhookSpec.URLSecretRef.Key
	secretNamespace := alert.Namespace

	var secret corev1.Secret
	secretObjectKey := types.NamespacedName{Namespace: secretNamespace, Name: secretName}

	if err := r.Get(ctx, secretObjectKey, &secret); err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("Referenced secret not found", "secret", secretObjectKey.String())
			return false, fmt.Errorf("referenced secret '%s' not found in namespace '%s'", secretName, secretNamespace)
		}
		logger.Error(err, "Failed to get referenced secret")
		return false, fmt.Errorf("failed to get referenced secret '%s': %w", secretName, err)
	}

	if _, ok := secret.Data[secretKey]; !ok {
		logger.Info("Key not found in referenced secret", "key", secretKey)
		return false, fmt.Errorf("key '%s' not found in secret '%s'", secretKey, secretName)
	}

	return true, nil
}

func (r *AlertReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&smartlogv1alpha1.Alert{}).
		Watches(
			&corev1.Secret{},
			handler.EnqueueRequestsFromMapFunc(r.findAlertsForSecret),
		).
		Complete(r)
}

func (r *AlertReconciler) findAlertsForSecret(ctx context.Context, secret client.Object) []reconcile.Request {
	var alerts smartlogv1alpha1.AlertList
	if err := r.List(ctx, &alerts, client.InNamespace(secret.GetNamespace())); err != nil {
		return []reconcile.Request{}
	}

	requests := make([]reconcile.Request, 0)
	for _, alert := range alerts.Items {
		if alert.Spec.Webhook != nil && alert.Spec.Webhook.URLSecretRef.Name == secret.GetName() {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      alert.Name,
					Namespace: alert.Namespace,
				},
			})
		}
	}
	return requests
}
