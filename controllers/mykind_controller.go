/*

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

package controllers

import (
	"context"

	"github.com/go-logr/logr"
	apps "k8s.io/api/apps/v1"
	core "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	webappv1 "github.com/h3poteto/crd-example/api/v1"
)

var (
	deploymentOwnerKey = ".metadata.controller"
)

// MyKindReconciler reconciles a MyKind object
type MyKindReconciler struct {
	client.Client
	Log      logr.Logger
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=webapp.h3poteto.dev,resources=mykinds,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=webapp.h3poteto.dev,resources=mykinds/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

func (r *MyKindReconciler) Reconcile(req ctrl.Request) (ctrl.Result, error) {
	ctx := context.Background()
	log := r.Log.WithValues("MyKind", req.NamespacedName)

	// your logic here
	log.Info("fetching MyKind resources")
	myKind := webappv1.MyKind{}
	if err := r.Client.Get(ctx, req.NamespacedName, &myKind); err != nil {
		log.Error(err, "failed to get MyKind resources")

		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if err := r.cleanupOwnedResources(ctx, log, &myKind); err != nil {
		log.Error(err, "failed to cleanup old Deployment resources for this MyKind")
		return ctrl.Result{}, err
	}

	log = log.WithValues("deployment_name", myKind.Spec.DeploymentName)

	log.Info("checking if an existing Deployment exists for this resource")
	deployment := apps.Deployment{}
	err := r.Client.Get(ctx, client.ObjectKey{Namespace: myKind.Namespace, Name: myKind.Spec.DeploymentName}, &deployment)
	if apierrors.IsNotFound(err) {
		log.Info("could not find existing Deployment for MyKind, creating one...")

		deployment = *buildDeployment(myKind)
		if err := r.Client.Create(ctx, &deployment); err != nil {
			log.Error(err, "failed to create Deployment resource")
			return ctrl.Result{}, err
		}

		r.Recorder.Eventf(&myKind, core.EventTypeNormal, "Created", "Created Deployment %q", deployment.Name)
		log.Info("created Deployment resource for MyKind")
		return ctrl.Result{}, nil
	}
	if err != nil {
		log.Error(err, "failed to get Deployment for MyKind resource")
		return ctrl.Result{}, err
	}

	log.Info("existing Deployment resource already exists for MyKind, checking replica count")

	expectedReplicas := int32(1)
	if myKind.Spec.Replicas != nil {
		expectedReplicas = *myKind.Spec.Replicas
	}
	if *deployment.Spec.Replicas != expectedReplicas {
		log.Info("updating replica count", "old_cound", *deployment.Spec.Replicas, "new_count", expectedReplicas)

		deployment.Spec.Replicas = &expectedReplicas
		if err := r.Client.Update(ctx, &deployment); err != nil {
			log.Error(err, "failed to update replica count in Deployment")
			return ctrl.Result{}, err
		}

		r.Recorder.Eventf(&myKind, core.EventTypeNormal, "Scaled", "Scaled deployment %q to %d replicas", deployment.Name, expectedReplicas)

		return ctrl.Result{}, nil
	}

	log.Info("replica count up to date", "replica_count", *deployment.Spec.Replicas)

	if myKind.Status.ReadyReplicas != deployment.Status.ReadyReplicas {
		log.Info("updating MyKind resource status", "status_ready_replica", myKind.Status.ReadyReplicas)
		myKind.Status.ReadyReplicas = deployment.Status.ReadyReplicas
		if err := r.Client.Update(ctx, &myKind); err != nil {
			log.Error(err, "failed to update MyKind status")
			return ctrl.Result{}, err
		}
	}

	log.Info("resource status synced")

	return ctrl.Result{}, nil
}

func (r *MyKindReconciler) cleanupOwnedResources(ctx context.Context, log logr.Logger, myKind *webappv1.MyKind) error {
	log.Info("finding existing Deployment resource")

	var deployments apps.DeploymentList
	if err := r.List(ctx, &deployments, client.InNamespace(myKind.Namespace), client.MatchingField(deploymentOwnerKey, myKind.Name)); err != nil {
		return err
	}
	deleted := 0
	for _, dep := range deployments.Items {
		if dep.Name == myKind.Spec.DeploymentName {
			continue
		}

		if err := r.Client.Delete(ctx, &dep); err != nil {
			log.Error(err, "failed to delete Deployment resource")
			return err
		}

		r.Recorder.Eventf(myKind, core.EventTypeNormal, "Deleted", "Deleted deployment %q", dep.Name)
		deleted++
	}

	log.Info("finished clenaup old Deployment resource", "number_deleted", deleted)
	return nil
}

func buildDeployment(myKind webappv1.MyKind) *apps.Deployment {
	deployment := apps.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:            myKind.Spec.DeploymentName,
			Namespace:       myKind.Namespace,
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(&myKind, webappv1.GroupVersion.WithKind("MyKind"))},
		},
		Spec: apps.DeploymentSpec{
			Replicas: myKind.Spec.Replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"deployment-name": myKind.Spec.DeploymentName,
				},
			},
			Template: core.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"deployment-name": myKind.Spec.DeploymentName,
					},
				},
				Spec: core.PodSpec{
					Containers: []core.Container{
						{
							Name:  "nginx",
							Image: "nginx:latest",
						},
					},
				},
			},
		},
	}
	return &deployment
}

func (r *MyKindReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(&apps.Deployment{}, deploymentOwnerKey, func(rawObj runtime.Object) []string {
		dep := rawObj.(*apps.Deployment)
		owner := metav1.GetControllerOf(dep)
		if owner == nil {
			return nil
		}
		if owner.APIVersion != webappv1.GroupVersion.String() || owner.Kind != "MyKind" {
			return nil
		}
		return []string{owner.Name}
	}); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&webappv1.MyKind{}).
		Owns(&apps.Deployment{}).
		Complete(r)
}
