// internal/controller/namespaceclass_controller.go
package controller

import (
    "context"
    "crypto/sha256"
    "encoding/json"
    "fmt"
    "reflect"
    "strings"
    "time"

    corev1 "k8s.io/api/core/v1"
    "k8s.io/apimachinery/pkg/api/errors"
    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
    "k8s.io/apimachinery/pkg/runtime"
    "k8s.io/apimachinery/pkg/types"
    "k8s.io/client-go/util/retry"
    "sigs.k8s.io/controller-runtime/pkg/client"
    "sigs.k8s.io/controller-runtime/pkg/controller"
    "sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
    "sigs.k8s.io/controller-runtime/pkg/event"
    "sigs.k8s.io/controller-runtime/pkg/handler"
    "sigs.k8s.io/controller-runtime/pkg/log"
    "sigs.k8s.io/controller-runtime/pkg/manager"
    "sigs.k8s.io/controller-runtime/pkg/predicate"
    "sigs.k8s.io/controller-runtime/pkg/reconcile"
    "sigs.k8s.io/controller-runtime/pkg/builder"

    v1 "github.com/nickleefly/namespace-class-controller/api/v1"
)

const (
    // Label key to identify which NamespaceClass a Namespace belongs to
    LabelKey                 = "namespaceclass.akuity.io/name"
    
    // Annotation to track resources managed by the controller
    AnnotationKey            = "namespaceclass.akuity.io/managed-resources"
    
    // Annotation to mark resources as managed by this controller
    ManagedByAnnotation      = "namespaceclass.akuity.io/managed-by"
    
    // Annotation to track resource hash for change detection
    ResourceHashAnnotation   = "namespaceclass.akuity.io/resource-hash"
    
    // Annotation to track which class created a resource
    CreatedByClassAnnotation = "namespaceclass.akuity.io/created-by-class"
    
    // Finalizer to ensure cleanup of resources when namespace is deleted
    NamespaceFinalizer       = "namespaceclass.akuity.io/finalizer"
)

// ManagedResource tracks resources applied to a namespace.
type ManagedResource struct {
    APIVersion string `json:"apiVersion"`
    Kind       string `json:"kind"`
    Name       string `json:"name"`
    Hash       string `json:"hash,omitempty"` // Store hash for change detection
}

// NamespaceClassReconciler reconciles Namespaces based on NamespaceClass.
type NamespaceClassReconciler struct {
    client.Client
    Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=namespaceclass.akuity.io,resources=namespaceclasses,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=namespaceclass.akuity.io,resources=namespaceclasses/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=core,resources=namespaces,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=*,resources=*,verbs=get;list;watch;create;update;patch;delete

// Reconcile ensures a namespace's resources match its NamespaceClass.
func (r *NamespaceClassReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
    logger := log.FromContext(ctx).WithValues(
        "namespace", req.Name, 
        "controller", "NamespaceClassReconciler",
    )
    
    startTime := time.Now()
    logger.Info("Starting reconciliation")
    defer func() {
        logger.Info("Completed reconciliation", "durationSeconds", time.Since(startTime).Seconds())
    }()

    // Fetch the namespace
    ns := &corev1.Namespace{}
    if err := r.Get(ctx, req.NamespacedName, ns); err != nil {
        if errors.IsNotFound(err) {
            logger.Info("Namespace not found, ignoring")
            return reconcile.Result{}, nil
        }
        logger.Error(err, "Failed to get namespace")
        return reconcile.Result{}, err
    }

    // Handle namespace deletion with finalizer
    if !ns.DeletionTimestamp.IsZero() {
        return r.handleNamespaceDeletion(ctx, ns)
    }

    // Get the current class label
    className, hasClass := ns.Labels[LabelKey]
    currentManaged, err := r.getManagedResources(ns)
    if err != nil {
        logger.Error(err, "Failed to parse managed resources")
        return reconcile.Result{}, err
    }

    // If no class, clean up and exit
    if !hasClass {
        logger.Info("Namespace has no class label, cleaning up managed resources")
        for _, res := range currentManaged {
            if err := r.deleteResource(ctx, ns.Name, res); err != nil {
                if !errors.IsNotFound(err) {
                    logger.Error(err, "Failed to delete resource", "resource", fmt.Sprintf("%s/%s", res.Kind, res.Name))
                }
            }
        }

        // Remove finalizer if exists
        if containsString(ns.Finalizers, NamespaceFinalizer) {
            ns.Finalizers = removeString(ns.Finalizers, NamespaceFinalizer)
            if err := r.Update(ctx, ns); err != nil {
                logger.Error(err, "Failed to remove finalizer")
                return reconcile.Result{}, err
            }
        }

        // Clear managed resources annotation
        if err := r.updateManagedResources(ctx, ns, nil); err != nil {
            logger.Error(err, "Failed to clear managed resources")
            return reconcile.Result{}, err
        }
        return reconcile.Result{}, nil
    }

    // Add finalizer if needed
    if !containsString(ns.Finalizers, NamespaceFinalizer) {
        controllerutil.AddFinalizer(ns, NamespaceFinalizer)
        if err := r.Update(ctx, ns); err != nil {
            logger.Error(err, "Failed to add finalizer")
            return reconcile.Result{}, err
        }
        // Return and requeue - the update will trigger a new reconcile
        return reconcile.Result{Requeue: true}, nil
    }

    // Fetch the NamespaceClass
    nsc := &v1.NamespaceClass{}
    if err := r.Get(ctx, types.NamespacedName{Name: className}, nsc); err != nil {
        if errors.IsNotFound(err) {
            logger.Error(err, "NamespaceClass not found", "class", className)
            return reconcile.Result{RequeueAfter: time.Minute}, nil // Requeue in case class is created later
        }
        logger.Error(err, "Failed to get NamespaceClass", "class", className)
        return reconcile.Result{}, err
    }

    // Parse desired resources from the NamespaceClass
    desiredResources, err := r.parseResources(ctx, nsc.Spec.Resources, className)
    if err != nil {
        logger.Error(err, "Failed to parse resources")
        return reconcile.Result{}, err
    }

    // Create or update desired resources
    var managed []ManagedResource
    for _, res := range desiredResources {
        // Set namespace and add management annotations
        res.SetNamespace(ns.Name)
        annotations := res.GetAnnotations()
        if annotations == nil {
            annotations = make(map[string]string)
        }
        annotations[ManagedByAnnotation] = "namespaceclass-controller"
        annotations[CreatedByClassAnnotation] = className

        // Calculate resource hash
        resourceHash := calculateResourceHash(res)
        annotations[ResourceHashAnnotation] = resourceHash
        res.SetAnnotations(annotations)

        // Create or update the resource
        if err := r.createOrUpdateResource(ctx, res); err != nil {
            logger.Error(err, "Failed to apply resource", 
                "kind", res.GetKind(), "name", res.GetName())
            return reconcile.Result{}, err
        }

        // Add to managed list
        managed = append(managed, ManagedResource{
            APIVersion: res.GetAPIVersion(),
            Kind:       res.GetKind(),
            Name:       res.GetName(),
            Hash:       resourceHash,
        })
    }

    // Get keys of desired resources for cleanup
    desiredKeys := make(map[string]bool)
    for _, res := range managed {
        key := fmt.Sprintf("%s/%s/%s", res.APIVersion, res.Kind, res.Name)
        desiredKeys[key] = true
    }

    // Clean up undesired resources
    for _, res := range currentManaged {
        key := fmt.Sprintf("%s/%s/%s", res.APIVersion, res.Kind, res.Name)
        if !desiredKeys[key] {
            if err := r.deleteResource(ctx, ns.Name, res); err != nil {
                if !errors.IsNotFound(err) {
                    logger.Error(err, "Failed to delete resource", 
                        "kind", res.Kind, "name", res.Name)
                    return reconcile.Result{}, err
                }
            }
            logger.Info("Deleted resource", "kind", res.Kind, "name", res.Name)
        }
    }

    // Update managed resources annotation
    if err := r.updateManagedResources(ctx, ns, managed); err != nil {
        logger.Error(err, "Failed to update managed resources")
        return reconcile.Result{}, err
    }

    // Update NamespaceClass status with retry
    return reconcile.Result{}, r.updateNamespaceClassStatus(ctx, nsc, ns.Name)
}

// Handle namespace deletion by cleaning up resources and removing finalizer
func (r *NamespaceClassReconciler) handleNamespaceDeletion(ctx context.Context, ns *corev1.Namespace) (reconcile.Result, error) {
    logger := log.FromContext(ctx).WithValues("namespace", ns.Name)
    
    // Check for our finalizer
    if !containsString(ns.Finalizers, NamespaceFinalizer) {
        logger.Info("Namespace is being deleted but has no finalizer, nothing to do")
        return reconcile.Result{}, nil
    }
    
    logger.Info("Namespace is being deleted, cleaning up resources")
    
    // Get managed resources
    managed, err := r.getManagedResources(ns)
    if err != nil {
        logger.Error(err, "Failed to parse managed resources")
        return reconcile.Result{}, err
    }
    
    // Delete all managed resources
    allSucceeded := true
    for _, res := range managed {
        if err := r.deleteResource(ctx, ns.Name, res); err != nil {
            if !errors.IsNotFound(err) {
                logger.Error(err, "Failed to delete resource", 
                    "kind", res.Kind, "name", res.Name)
                allSucceeded = false
            }
        }
    }
    
    // If any errors, retry
    if !allSucceeded {
        return reconcile.Result{RequeueAfter: time.Second * 10}, nil
    }
    
    // Remove finalizer
    logger.Info("All resources cleaned up, removing finalizer")
    controllerutil.RemoveFinalizer(ns, NamespaceFinalizer)
    if err := r.Update(ctx, ns); err != nil {
        logger.Error(err, "Failed to remove finalizer")
        return reconcile.Result{}, err
    }
    
    return reconcile.Result{}, nil
}

// Helper functions
func (r *NamespaceClassReconciler) getManagedResources(ns *corev1.Namespace) ([]ManagedResource, error) {
    if ns.Annotations == nil || ns.Annotations[AnnotationKey] == "" {
        return nil, nil
    }
    var managed []ManagedResource
    if err := json.Unmarshal([]byte(ns.Annotations[AnnotationKey]), &managed); err != nil {
        return nil, err
    }
    return managed, nil
}

func (r *NamespaceClassReconciler) updateManagedResources(ctx context.Context, ns *corev1.Namespace, managed []ManagedResource) error {
    return retry.RetryOnConflict(retry.DefaultRetry, func() error {
        // Get latest namespace
        if err := r.Get(ctx, types.NamespacedName{Name: ns.Name}, ns); err != nil {
            return err
        }
        
        if ns.Annotations == nil {
            ns.Annotations = make(map[string]string)
        }
        
        if managed == nil || len(managed) == 0 {
            delete(ns.Annotations, AnnotationKey)
        } else {
            data, err := json.Marshal(managed)
            if err != nil {
                return err
            }
            ns.Annotations[AnnotationKey] = string(data)
        }
        
        return r.Update(ctx, ns)
    })
}

func (r *NamespaceClassReconciler) parseResources(ctx context.Context, raw []runtime.RawExtension, className string) ([]*unstructured.Unstructured, error) {
    var result []*unstructured.Unstructured
    for _, r := range raw {
        var u unstructured.Unstructured
        if err := json.Unmarshal(r.Raw, &u); err != nil {
            return nil, err
        }
        
        // Validate the resource
        if err := validateResource(&u); err != nil {
            return nil, fmt.Errorf("invalid resource in class %s: %v", className, err)
        }
        
        result = append(result, &u)
    }
    return result, nil
}

func validateResource(u *unstructured.Unstructured) error {
    if u.GetAPIVersion() == "" {
        return fmt.Errorf("resource is missing apiVersion")
    }
    if u.GetKind() == "" {
        return fmt.Errorf("resource is missing kind")
    }
    if u.GetName() == "" {
        return fmt.Errorf("resource is missing name")
    }
    return nil
}

func calculateResourceHash(obj *unstructured.Unstructured) string {
    // Deep copy to avoid modifying the original
    copy := obj.DeepCopy()
    
    // Remove metadata fields that change frequently
    if metaMap, ok := copy.Object["metadata"].(map[string]interface{}); ok {
        delete(metaMap, "resourceVersion")
        delete(metaMap, "generation")
        delete(metaMap, "creationTimestamp")
        delete(metaMap, "annotations")
    }
    
    // Serialize the object for hashing
    data, err := json.Marshal(copy.Object["spec"])
    if err != nil {
        return ""
    }
    
    // Calculate hash
    hash := sha256.Sum256(data)
    return fmt.Sprintf("%x", hash)
}

func (r *NamespaceClassReconciler) createOrUpdateResource(ctx context.Context, desired *unstructured.Unstructured) error {
    logger := log.FromContext(ctx)
    
    existing := &unstructured.Unstructured{}
    existing.SetGroupVersionKind(desired.GroupVersionKind())
    
    err := r.Get(ctx, types.NamespacedName{
        Namespace: desired.GetNamespace(), 
        Name: desired.GetName(),
    }, existing)
    
    if errors.IsNotFound(err) {
        logger.Info("Creating resource", 
            "kind", desired.GetKind(), 
            "name", desired.GetName(),
            "namespace", desired.GetNamespace())
        return r.Create(ctx, desired)
    } else if err != nil {
        return err
    }
    
    // Check if update is needed by comparing hash
    existingHash := existing.GetAnnotations()[ResourceHashAnnotation]
    newHash := desired.GetAnnotations()[ResourceHashAnnotation]
    
    if existingHash != newHash {
        logger.Info("Updating resource", 
            "kind", desired.GetKind(), 
            "name", desired.GetName(),
            "namespace", desired.GetNamespace())
        
        // Preserve resource version for update
        desired.SetResourceVersion(existing.GetResourceVersion())
        return r.Update(ctx, desired)
    }
    
    logger.V(1).Info("No changes needed for resource", 
        "kind", desired.GetKind(), 
        "name", desired.GetName(),
        "namespace", desired.GetNamespace())
    return nil
}

func (r *NamespaceClassReconciler) deleteResource(ctx context.Context, namespace string, res ManagedResource) error {
    obj := &unstructured.Unstructured{}
    obj.SetAPIVersion(res.APIVersion)
    obj.SetKind(res.Kind)
    obj.SetName(res.Name)
    obj.SetNamespace(namespace)
    
    err := r.Delete(ctx, obj)
    if err != nil && !errors.IsNotFound(err) {
        return err
    }
    return nil
}

// Update NamespaceClass status with managed namespaces
func (r *NamespaceClassReconciler) updateNamespaceClassStatus(ctx context.Context, nsc *v1.NamespaceClass, namespace string) error {
    return retry.RetryOnConflict(retry.DefaultRetry, func() error {
        // Get latest NamespaceClass
        if err := r.Get(ctx, types.NamespacedName{Name: nsc.Name}, nsc); err != nil {
            return err
        }
        
        // Check if namespace is already in the status
        if !containsString(nsc.Status.ManagedNamespaces, namespace) {
            nsc.Status.ManagedNamespaces = append(nsc.Status.ManagedNamespaces, namespace)
            nsc.Status.LastUpdateTime = metav1.Now()
            
            if err := r.Status().Update(ctx, nsc); err != nil {
                return err
            }
        }
        
        return nil
    })
}

// Helper function to check if a string slice contains a string
func containsString(slice []string, s string) bool {
    for _, item := range slice {
        if item == s {
            return true
        }
    }
    return false
}

// Helper function to remove string from slice
func removeString(slice []string, s string) []string {
    result := make([]string, 0, len(slice))
    for _, item := range slice {
        if item != s {
            result = append(result, item)
        }
    }
    return result
}

// isTransientError checks if an error is likely transient
func isTransientError(err error) bool {
    if err == nil {
        return false
    }
    
    if errors.IsServerTimeout(err) || errors.IsTimeout(err) || 
       errors.IsTooManyRequests(err) || errors.IsServiceUnavailable(err) {
        return true
    }
    
    errMsg := err.Error()
    return strings.Contains(errMsg, "connection refused") || 
           strings.Contains(errMsg, "EOF") ||
           strings.Contains(errMsg, "i/o timeout")
}

// SetupWithManager sets up the controller with the Manager.
func (r *NamespaceClassReconciler) SetupWithManager(mgr manager.Manager) error {
    // Define predicates for namespace events
    namespacePredicate := predicate.Funcs{
        CreateFunc: func(e event.CreateEvent) bool {
            // Process namespace creation
            return true
        },
        UpdateFunc: func(e event.UpdateEvent) bool {
            oldNs, ok1 := e.ObjectOld.(*corev1.Namespace)
            newNs, ok2 := e.ObjectNew.(*corev1.Namespace)
            
            if !ok1 || !ok2 {
                return false
            }
            
            // Process if class label changed or finalizers changed
            oldClass, oldHasClass := oldNs.Labels[LabelKey]
            newClass, newHasClass := newNs.Labels[LabelKey]
            
            finalizersChanged := !reflect.DeepEqual(oldNs.Finalizers, newNs.Finalizers)
            
            return oldHasClass != newHasClass || oldClass != newClass || 
                   finalizersChanged || !newNs.DeletionTimestamp.IsZero()
        },
        DeleteFunc: func(e event.DeleteEvent) bool {
            // Ignore namespace deletion - handled by finalizers
            return false
        },
    }
    
    // Define mapping function for NamespaceClass to trigger reconcile on related Namespaces
    mapFunc := func(ctx context.Context, obj client.Object) []reconcile.Request {
        namespaceCls, ok := obj.(*v1.NamespaceClass)
        if !ok {
            return nil
        }
        
        // Find all namespaces with this class
        var nsList corev1.NamespaceList
        if err := mgr.GetClient().List(ctx, &nsList, client.MatchingLabels{LabelKey: namespaceCls.Name}); err != nil {
            log.FromContext(ctx).Error(err, "Failed to list namespaces for class", "class", namespaceCls.Name)
            return nil
        }
        
        // Queue reconcile requests for all affected namespaces
        var requests []reconcile.Request
        for _, ns := range nsList.Items {
            requests = append(requests, reconcile.Request{
                NamespacedName: types.NamespacedName{Name: ns.Name},
            })
        }
        
        return requests
    }

    // Set up controller with the builder pattern
    return builder.ControllerManagedBy(mgr).
        Named("namespaceclass-controller").
        WithOptions(controller.Options{
            MaxConcurrentReconciles: 5, // Allow parallel processing
        }).
        For(&corev1.Namespace{}, builder.WithPredicates(namespacePredicate)).
        Watches(
            &v1.NamespaceClass{},
            handler.EnqueueRequestsFromMapFunc(mapFunc),
        ).
        Complete(r)
}