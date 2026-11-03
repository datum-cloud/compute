package controller

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

// reconcileInstanceTypeInProject reconciles the named InstanceType the way the
// controller does in production: through the multicluster manager, scoped to
// one project control plane, here backed by the envtest API server.
func reconcileInstanceTypeInProject(ctx context.Context, name string) error {
	const project = "project-a"
	reconciler := &InstanceTypeReconciler{mgr: newFakeMCManager(project, newFakeCluster(k8sClient))}
	_, err := reconciler.Reconcile(ctx, mcreconcile.Request{
		ClusterName: project,
		Request:     ctrl.Request{NamespacedName: types.NamespacedName{Name: name}},
	})
	return err
}

var _ = Describe("InstanceType Controller", func() {
	Context("When reconciling an InstanceType", func() {
		It("Should set Ready=True when valid and no replacement specified", func() {
			ctx := context.Background()

			instanceType := &computev1alpha.InstanceType{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-type-1",
				},
				Spec: computev1alpha.InstanceTypeSpec{
					Resources: computev1alpha.InstanceTypeResources{
						CPU:    resource.MustParse("1000m"),
						Memory: resource.MustParse("2Gi"),
					},
					Lifecycle: computev1alpha.InstanceTypeLifecycle{
						Phase: computev1alpha.InstanceTypePhaseActive,
					},
				},
			}

			Expect(k8sClient.Create(ctx, instanceType)).Should(Succeed())

			err := reconcileInstanceTypeInProject(ctx, instanceType.Name)
			Expect(err).NotTo(HaveOccurred())

			var fetched computev1alpha.InstanceType
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: instanceType.Name}, &fetched)).Should(Succeed())

			readyCond := meta.FindStatusCondition(fetched.Status.Conditions, computev1alpha.InstanceTypeConditionReady)
			Expect(readyCond).NotTo(BeNil())
			Expect(readyCond.Status).To(Equal(metav1.ConditionTrue))
		})

		It("Should set Ready=False when replacement does not exist", func() {
			ctx := context.Background()

			instanceType := &computev1alpha.InstanceType{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-type-2",
				},
				Spec: computev1alpha.InstanceTypeSpec{
					Resources: computev1alpha.InstanceTypeResources{
						CPU:    resource.MustParse("1000m"),
						Memory: resource.MustParse("2Gi"),
					},
					Lifecycle: computev1alpha.InstanceTypeLifecycle{
						Phase:                   computev1alpha.InstanceTypePhaseDeprecated,
						ReplacementInstanceType: "nonexistent-type",
					},
				},
			}

			Expect(k8sClient.Create(ctx, instanceType)).Should(Succeed())

			err := reconcileInstanceTypeInProject(ctx, instanceType.Name)
			Expect(err).NotTo(HaveOccurred())

			var fetched computev1alpha.InstanceType
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: instanceType.Name}, &fetched)).Should(Succeed())

			readyCond := meta.FindStatusCondition(fetched.Status.Conditions, computev1alpha.InstanceTypeConditionReady)
			Expect(readyCond).NotTo(BeNil())
			Expect(readyCond.Status).To(Equal(metav1.ConditionFalse))
			Expect(readyCond.Reason).To(Equal("ReplacementNotFound"))
		})

		It("Should set Ready=False when replacement is disabled", func() {
			ctx := context.Background()

			replacementType := &computev1alpha.InstanceType{
				ObjectMeta: metav1.ObjectMeta{
					Name: "disabled-replacement",
				},
				Spec: computev1alpha.InstanceTypeSpec{
					Resources: computev1alpha.InstanceTypeResources{
						CPU:    resource.MustParse("1000m"),
						Memory: resource.MustParse("2Gi"),
					},
					Lifecycle: computev1alpha.InstanceTypeLifecycle{
						Phase: computev1alpha.InstanceTypePhaseDisabled,
					},
				},
			}
			Expect(k8sClient.Create(ctx, replacementType)).Should(Succeed())

			instanceType := &computev1alpha.InstanceType{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-type-3",
				},
				Spec: computev1alpha.InstanceTypeSpec{
					Resources: computev1alpha.InstanceTypeResources{
						CPU:    resource.MustParse("1000m"),
						Memory: resource.MustParse("2Gi"),
					},
					Lifecycle: computev1alpha.InstanceTypeLifecycle{
						Phase:                   computev1alpha.InstanceTypePhaseDeprecated,
						ReplacementInstanceType: replacementType.Name,
					},
				},
			}

			Expect(k8sClient.Create(ctx, instanceType)).Should(Succeed())

			err := reconcileInstanceTypeInProject(ctx, instanceType.Name)
			Expect(err).NotTo(HaveOccurred())

			var fetched computev1alpha.InstanceType
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: instanceType.Name}, &fetched)).Should(Succeed())

			readyCond := meta.FindStatusCondition(fetched.Status.Conditions, computev1alpha.InstanceTypeConditionReady)
			Expect(readyCond).NotTo(BeNil())
			Expect(readyCond.Status).To(Equal(metav1.ConditionFalse))
			Expect(readyCond.Reason).To(Equal("ReplacementDisabled"))
		})
	})
})
