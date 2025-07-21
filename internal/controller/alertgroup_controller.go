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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	smartlogv1alpha1 "github.com/gitlayzer/smart-log/api/v1alpha1"
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

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the AlertGroup object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.21.0/pkg/reconcile
func (r *AlertGroupReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// 获取 AlertGroup 实例
	var alertGroup smartlogv1alpha1.AlertGroup
	if err := r.Get(ctx, req.NamespacedName, &alertGroup); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("AlertGroup resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to get AlertGroup")
		return ctrl.Result{}, err
	}

	// 核心：验证 AlertGroup 配置并更新状态
	readyMembers, validationErr := r.validateAlertGroupMembers(ctx, &alertGroup)

	// 准备更新 status
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

	// 将更新后的状态写回 API Server

	if err := r.Status().Update(ctx, &alertGroup); err != nil {
		log.Error(err, "Failed to update AlertGroup status")
		return ctrl.Result{}, err
	}

	log.Info("Successfully reconciled AlertGroup status")

	return ctrl.Result{}, nil
}

// validateAlertGroupMembers 检查 AlertGroup 的成员是否有效
func (r *AlertGroupReconciler) validateAlertGroupMembers(ctx context.Context, alertGroup *smartlogv1alpha1.AlertGroup) (int, error) {
	log := logf.FromContext(ctx)
	readyCount := 0

	// 遍历 spec 中定义的所有 Alert 名称
	for _, alertName := range alertGroup.Spec.AlertNames {
		var alert smartlogv1alpha1.Alert
		alertKey := types.NamespacedName{
			Name:      alertName,
			Namespace: alertGroup.Namespace, // 在同一命名空间内查找
		}

		// 尝试获取每一个 Alert 成员
		if err := r.Get(ctx, alertKey, &alert); err != nil {
			if apierrors.IsNotFound(err) {
				log.Info("Member alert not found", "alert", alertKey.String())
				return readyCount, fmt.Errorf("member alert '%s' not found", alertName)
			}
			log.Error(err, "Failed to get member alert", "alert", alertKey.String())
			return readyCount, fmt.Errorf("failed to get member alert '%s': %w", alertName, err)
		}

		// (推荐) 检查成员 Alert 自身的状态是否就绪
		if !alert.Status.Ready {
			log.Info("Member alert is not ready", "alert", alertKey.String())
			return readyCount, fmt.Errorf("member alert '%s' is not ready", alertName)
		}

		// 如果该成员验证通过，计数器加一
		readyCount++
	}

	// 所有成员都验证通过
	return readyCount, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *AlertGroupReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&smartlogv1alpha1.AlertGroup{}).
		Watches(
			&smartlogv1alpha1.Alert{},
			handler.EnqueueRequestsFromMapFunc(r.findAlertGroupsForAlert),
		).
		Named("alertgroup").
		Complete(r)
}

// findAlertGroupsForAlert 找到所有引用了某个 Alert 的 AlertGroup
func (r *AlertGroupReconciler) findAlertGroupsForAlert(ctx context.Context, alertObj client.Object) []reconcile.Request {
	// 检查对象是否为 Alert
	alert, ok := alertObj.(*smartlogv1alpha1.Alert)
	if !ok {
		return []reconcile.Request{}
	}

	// 获取所有 AlertGroup
	var allAlertGroups smartlogv1alpha1.AlertGroupList
	// 我们需要在所有命名空间中查找 AlertGroup，或者限定在同一命名空间，这里以后者为例
	if err := r.List(ctx, &allAlertGroups, client.InNamespace(alert.GetNamespace())); err != nil {
		return []reconcile.Request{}
	}

	// 遍历所有 AlertGroup
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
