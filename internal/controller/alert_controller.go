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
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	smartlogv1alpha1 "github.com/gitlayzer/smart-log/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// AlertReconciler reconciles a Alert object
type AlertReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=smartlog.smart-tools.com,resources=alerts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=smartlog.smart-tools.com,resources=alerts/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=smartlog.smart-tools.com,resources=alerts/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Alert object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.21.0/pkg/reconcile
func (r *AlertReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// 获取 Alert 实例
	var alert smartlogv1alpha1.Alert
	if err := r.Get(ctx, req.NamespacedName, &alert); err != nil {
		// 关键修复点在这里！
		if apierrors.IsNotFound(err) {
			// 当资源找不到时，说明它很可能已经被删除了。
			// 这不是一个需要重试的错误，所以我们打印一条日志然后直接返回。
			log.Info("Alert resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil // 直接返回，不再执行后面的逻辑
		}

		// 如果是其他类型的错误（比如网络问题），则打印错误并返回，以便稍后重试。
		log.Error(err, "Failed to get Alert")
		return ctrl.Result{}, err
	}

	// 验证 Alert 并更新其状态
	isValid, validationErr := r.validateAlert(ctx, &alert)

	// 验证通过更新状态
	alert.Status.Ready = isValid
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

	// 使用 meta 来设置 Condition，它可以避免重复添加相同的 Condition
	meta.SetStatusCondition(&alert.Status.Conditions, readyCondition)

	// 将更新后的状态写回 API Server
	if err := r.Status().Update(ctx, &alert); err != nil {
		log.Error(err, "Failed to update Alert status")
		return ctrl.Result{}, err
	}

	log.Info("Successfully reconciled Alert status", "Ready", isValid)

	return ctrl.Result{}, nil
}

// validateAlert 验证 Alert 是否合法
func (r *AlertReconciler) validateAlert(ctx context.Context, alert *smartlogv1alpha1.Alert) (bool, error) {
	log := logf.FromContext(ctx)

	// 目前我们只支持 WebHook
	if alert.Spec.Type != "WebHook" {
		return false, fmt.Errorf("unsupported alert type: %s", alert.Spec.Type)
	}

	// 检查 Webhook 配置是否存在
	if alert.Spec.Webhook == nil {
		return false, fmt.Errorf("webhook spec is not defined for alert type WebHook")
	}

	// 验证 Webhook 配置的核心部分：引用的 Secret 是否存在且有效
	webhookSpec := alert.Spec.Webhook
	secretName := webhookSpec.URLSecretRef.Name
	secretKey := webhookSpec.URLSecretRef.Key

	// 在与 Alert 资源相同的命名空间中查找 Secret
	secretNamespace := alert.Namespace
	log.Info("Validating Secret reference", "SecretName", secretName, "SecretKey", secretKey, "InNamespace", secretNamespace)

	var secret corev1.Secret
	secretObjectKey := types.NamespacedName{Namespace: secretNamespace, Name: secretName}

	if err := r.Get(ctx, secretObjectKey, &secret); err != nil {
		log.Error(err, "unable to fetch Secret")
		return false, client.IgnoreNotFound(err)
	}

	// 检查 Secret 中是否存在指定的 key
	if _, ok := secret.Data[secretKey]; !ok {
		log.Info("Key not found in referenced secret", "key", secretKey)
		return false, fmt.Errorf("key '%s' not found in secret '%s'", secretKey, secretName)
	}

	// 所有验证通过
	return true, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *AlertReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&smartlogv1alpha1.Alert{}).
		Watches(
			&corev1.Secret{},
			handler.EnqueueRequestsFromMapFunc(r.findAlertsForSecret),
		).
		Named("alert").
		Complete(r)
}

// findAlertsForSecret 是一个辅助函数，用于找到所有引用了某个 Secret 的 Alert 资源

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
