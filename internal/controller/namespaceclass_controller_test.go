// internal/controller/namespaceclass_controller_test.go
package controller

import (
    "context"
    "encoding/json"
    "time"

    . "github.com/onsi/ginkgo/v2"
    . "github.com/onsi/gomega"
    
    corev1 "k8s.io/api/core/v1"
    networkingv1 "k8s.io/api/networking/v1"
    "k8s.io/apimachinery/pkg/api/errors"
    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    "k8s.io/apimachinery/pkg/runtime"
    "k8s.io/apimachinery/pkg/types"
    "sigs.k8s.io/controller-runtime/pkg/client"
    "sigs.k8s.io/controller-runtime/pkg/client/fake"
    logf "sigs.k8s.io/controller-runtime/pkg/log"
    "sigs.k8s.io/controller-runtime/pkg/log/zap"
    "sigs.k8s.io/controller-runtime/pkg/reconcile"
    
    v1 "github.com/nickleefly/namespace-class-controller/api/v1"
)

var _ = Describe("NamespaceClass controller", func() {
    var (
        reconciler *NamespaceClassReconciler
        cl         client.Client
        ctx        context.Context
    )

    BeforeEach(func() {
        logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))
        ctx = context.Background()
        
        scheme := runtime.NewScheme()
        Expect(corev1.AddToScheme(scheme)).To(Succeed())
        Expect(networkingv1.AddToScheme(scheme)).To(Succeed())
        Expect(v1.AddToScheme(scheme)).To(Succeed())
        
        cl = fake.NewClientBuilder().WithScheme(scheme).Build()
        reconciler = &NamespaceClassReconciler{
            Client: cl,
            Scheme: scheme,
        }
    })

    Context("When reconciling a namespace with a NamespaceClass", func() {
        It("should create resources defined in the class", func() {
            // Create a test NamespaceClass
            namespaceCls := &v1.NamespaceClass{
                ObjectMeta: metav1.ObjectMeta{
                    Name: "test-class",
                },
                Spec: v1.NamespaceClassSpec{
                    Resources: []runtime.RawExtension{
                        createNetworkPolicyRaw("test-policy", "0.0.0.0/0"),
                    },
                },
            }
            Expect(cl.Create(ctx, namespaceCls)).To(Succeed())
            
            // Create a test Namespace with the class label
            ns := &corev1.Namespace{
                ObjectMeta: metav1.ObjectMeta{
                    Name: "test-namespace",
                    Labels: map[string]string{
                        LabelKey: "test-class",
                    },
                },
            }
            Expect(cl.Create(ctx, ns)).To(Succeed())
            
            // Reconcile
            req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "test-namespace"}}
            res, err := reconciler.Reconcile(ctx, req)
            Expect(err).NotTo(HaveOccurred())
            Expect(res).To(Equal(reconcile.Result{Requeue: true})) // First reconcile adds finalizer
            
            // Verify finalizer was added
            ns = &corev1.Namespace{}
            Expect(cl.Get(ctx, types.NamespacedName{Name: "test-namespace"}, ns)).To(Succeed())
            Expect(ns.Finalizers).To(ContainElement(NamespaceFinalizer))
            
            // Reconcile again after finalizer is added
            res, err = reconciler.Reconcile(ctx, req)
            Expect(err).NotTo(HaveOccurred())
            Expect(res).To(Equal(reconcile.Result{}))
            
            // Verify NetworkPolicy was created
            netPolicy := &networkingv1.NetworkPolicy{}
            Eventually(func() error {
                return cl.Get(ctx, types.NamespacedName{
                    Namespace: "test-namespace",
                    Name:      "test-policy",
                }, netPolicy)
            }, time.Second*5, time.Millisecond*500).Should(Succeed())
            
            // Verify annotations
            Expect(netPolicy.Annotations[ManagedByAnnotation]).To(Equal("namespaceclass-controller"))
            Expect(netPolicy.Annotations[CreatedByClassAnnotation]).To(Equal("test-class"))
            
            // Verify status update
            namespaceClsUpdated := &v1.NamespaceClass{}
            Expect(cl.Get(ctx, types.NamespacedName{Name: "test-class"}, namespaceClsUpdated)).To(Succeed())
            Expect(namespaceClsUpdated.Status.ManagedNamespaces).To(ContainElement("test-namespace"))
            
            // Change the class
            namespaceCls2 := &v1.NamespaceClass{
                ObjectMeta: metav1.ObjectMeta{
                    Name: "new-class",
                },
                Spec: v1.NamespaceClassSpec{
                    Resources: []runtime.RawExtension{
                        createNetworkPolicyRaw("new-policy", "10.0.0.0/8"),
                    },
                },
            }
            Expect(cl.Create(ctx, namespaceCls2)).To(Succeed())
            
            // Update the namespace to use the new class
            Expect(cl.Get(ctx, types.NamespacedName{Name: "test-namespace"}, ns)).To(Succeed())
            ns.Labels[LabelKey] = "new-class"
            Expect(cl.Update(ctx, ns)).To(Succeed())
            
            // Reconcile
            res, err = reconciler.Reconcile(ctx, req)
            Expect(err).NotTo(HaveOccurred())
            
            // Verify old policy is deleted
            Eventually(func() bool {
                err := cl.Get(ctx, types.NamespacedName{
                    Namespace: "test-namespace",
                    Name:      "test-policy",
                }, netPolicy)
                return errors.IsNotFound(err)
            }, time.Second*5, time.Millisecond*500).Should(BeTrue())
            
            // Verify new policy is created
            newPolicy := &networkingv1.NetworkPolicy{}
            Eventually(func() error {
                return cl.Get(ctx, types.NamespacedName{
                    Namespace: "test-namespace",
                    Name:      "new-policy",
                }, newPolicy)
            }, time.Second*5, time.Millisecond*500).Should(Succeed())
            
            // Remove class label
            Expect(cl.Get(ctx, types.NamespacedName{Name: "test-namespace"}, ns)).To(Succeed())
            delete(ns.Labels, LabelKey)
            Expect(cl.Update(ctx, ns)).To(Succeed())
            
            // Reconcile
            res, err = reconciler.Reconcile(ctx, req)
            Expect(err).NotTo(HaveOccurred())

            // Verify all policies are deleted
            Eventually(func() bool {
                err := cl.Get(ctx, types.NamespacedName{
                    Namespace: "test-namespace",
                    Name:      "new-policy",
                }, newPolicy)
                return errors.IsNotFound(err)
            }, time.Second*5, time.Millisecond*500).Should(BeTrue())
            
            // Verify finalizer is removed
            Expect(cl.Get(ctx, types.NamespacedName{Name: "test-namespace"}, ns)).To(Succeed())
            Expect(ns.Finalizers).NotTo(ContainElement(NamespaceFinalizer))
            
            // Test namespace deletion
            ns.Labels[LabelKey] = "test-class" // Re-add label
            Expect(cl.Update(ctx, ns)).To(Succeed())
            
            // Reconcile to add resources back
            res, err = reconciler.Reconcile(ctx, req)
            Expect(err).NotTo(HaveOccurred())
            
            // Mark namespace for deletion
            now := metav1.Now()
            ns.DeletionTimestamp = &now
            Expect(cl.Update(ctx, ns)).To(Succeed())
            
            // Reconcile to handle deletion
            res, err = reconciler.Reconcile(ctx, req)
            Expect(err).NotTo(HaveOccurred())
            
            // Verify resources are deleted and finalizer is removed
            Expect(cl.Get(ctx, types.NamespacedName{Name: "test-namespace"}, ns)).To(Succeed())
            Expect(ns.Finalizers).NotTo(ContainElement(NamespaceFinalizer))
        })
    })
})

// Helper to create a network policy raw extension
func createNetworkPolicyRaw(name, cidr string) runtime.RawExtension {
    policy := map[string]interface{}{
        "apiVersion": "networking.k8s.io/v1",
        "kind":       "NetworkPolicy",
        "metadata": map[string]interface{}{
            "name": name,
        },
        "spec": map[string]interface{}{
            "podSelector": map[string]interface{}{},
            "policyTypes": []string{"Ingress"},
            "ingress": []map[string]interface{}{
                {
                    "from": []map[string]interface{}{
                        {
                            "ipBlock": map[string]interface{}{
                                "cidr": cidr,
                            },
                        },
                    },
                },
            },
        },
    }
    
    raw, _ := json.Marshal(policy)
    return runtime.RawExtension{Raw: raw}
}