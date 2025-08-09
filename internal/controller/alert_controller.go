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
	"k8s.io/client-go/util/retry"
	"reflect"
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

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var alert smartlogv1alpha1.Alert
		if err := r.Get(ctx, req.NamespacedName, &alert); err != nil {
			// 如果在重试期间对象被删除，这里会返回 NotFound 错误，并停止重试
			if apierrors.IsNotFound(err) {
				logger.Info("Alert resource not found during status update. Ignoring.")
				return nil
			}
			return err
		}

		// 深度复制一份原始状态，用于后续比较
		originalStatus := alert.Status.DeepCopy()

		// 在最新对象上计算和设置状态
		isValid, validationErr := r.validateAlert(ctx, &alert)
		readyCondition := metav1.Condition{
			Type: "Ready",
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

		// 只有在状态发生有意义的变化时才更新
		if reflect.DeepEqual(originalStatus, &alert.Status) {
			return nil
		}

		return r.Status().Update(ctx, &alert)
	})

	if err != nil {
		logger.Error(err, "Failed to update Alert status after multiple retries")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *AlertReconciler) validateAlert(ctx context.Context, alert *smartlogv1alpha1.Alert) (bool, error) {
	switch alert.Spec.Type {
	case "Webhook":
		if alert.Spec.Webhook == nil {
			return false, fmt.Errorf("webhook spec is not defined for alert type Webhook")
		}
		return r.validateSecretRef(ctx, alert.Namespace, &alert.Spec.Webhook.URLSecretRef)
	case "Feishu":
		if alert.Spec.Feishu == nil {
			return false, fmt.Errorf("feishu spec is not defined for alert type Feishu")
		}
		// 验证主 Webhook URL
		valid, err := r.validateSecretRef(ctx, alert.Namespace, &alert.Spec.Feishu.URLSecretRef)
		if !valid {
			return false, err
		}
		// 如果配置了加签密钥，也需要验证
		if alert.Spec.Feishu.SecretKeySecretRef != nil {
			return r.validateSecretRef(ctx, alert.Namespace, alert.Spec.Feishu.SecretKeySecretRef)
		}
		return true, nil
	default:
		return false, fmt.Errorf("unsupported alert type: %s", alert.Spec.Type)
	}
}

func (r *AlertReconciler) validateSecretRef(ctx context.Context, namespace string, selector *corev1.SecretKeySelector) (bool, error) {
	logger := log.FromContext(ctx)
	var secret corev1.Secret
	secretKey := types.NamespacedName{Namespace: namespace, Name: selector.Name}

	if err := r.Get(ctx, secretKey, &secret); err != nil {
		if apierrors.IsNotFound(err) {
			return false, fmt.Errorf("referenced secret '%s' not found", selector.Name)
		}
		return false, fmt.Errorf("failed to get referenced secret '%s': %w", selector.Name, err)
	}

	if _, ok := secret.Data[selector.Key]; !ok {
		logger.Info("Key not found in referenced secret", "key", selector.Key)
		return false, fmt.Errorf("key '%s' not found in secret '%s'", selector.Key, selector.Name)
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
		feishuSpec := alert.Spec.Feishu
		webhookSpec := alert.Spec.Webhook

		// 检查 Webhook 的引用
		isWebhookRelated := webhookSpec != nil && webhookSpec.URLSecretRef.Name == secret.GetName()

		// 检查 Feishu 的引用（包括所有字段）
		isFeishuRelated := feishuSpec != nil && (feishuSpec.URLSecretRef.Name == secret.GetName() ||
			(feishuSpec.SecretKeySecretRef != nil && feishuSpec.SecretKeySecretRef.Name == secret.GetName()))
		if isWebhookRelated || isFeishuRelated {
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
