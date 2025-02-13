//go:build integration

package policy_integration

import (
	"encoding/json"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"
	v1 "open-cluster-management.io/api/cluster/v1"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/Kuadrant/multicluster-gateway-controller/pkg/_internal/conditions"
	"github.com/Kuadrant/multicluster-gateway-controller/pkg/apis/v1alpha1"
	"github.com/Kuadrant/multicluster-gateway-controller/pkg/common"
	. "github.com/Kuadrant/multicluster-gateway-controller/pkg/controllers/dnspolicy"
	testutil "github.com/Kuadrant/multicluster-gateway-controller/test/util"
)

var _ = Describe("DNSPolicy", func() {

	var gatewayClass *gatewayapiv1.GatewayClass
	var managedZone *v1alpha1.ManagedZone
	var testNamespace string
	var dnsPolicyBuilder *testutil.DNSPolicyBuilder
	var gateway *gatewayapiv1.Gateway
	var dnsPolicy *v1alpha1.DNSPolicy
	var recordName, wildcardRecordName string

	BeforeEach(func() {
		CreateNamespace(&testNamespace)

		gatewayClass = testutil.NewTestGatewayClass("foo", "default", "kuadrant.io/bar")
		Expect(k8sClient.Create(ctx, gatewayClass)).To(Succeed())

		managedZone = testutil.NewManagedZoneBuilder("mz-example-com", testNamespace, "example.com").ManagedZone
		Expect(k8sClient.Create(ctx, managedZone)).To(Succeed())

		dnsPolicyBuilder = testutil.NewDNSPolicyBuilder("test-dns-policy", testNamespace)
	})

	AfterEach(func() {
		if gateway != nil {
			err := k8sClient.Delete(ctx, gateway)
			Expect(client.IgnoreNotFound(err)).ToNot(HaveOccurred())
		}
		if dnsPolicy != nil {
			err := k8sClient.Delete(ctx, dnsPolicy)
			Expect(client.IgnoreNotFound(err)).ToNot(HaveOccurred())

		}
		if managedZone != nil {
			err := k8sClient.Delete(ctx, managedZone)
			Expect(client.IgnoreNotFound(err)).ToNot(HaveOccurred())
		}
		if gatewayClass != nil {
			err := k8sClient.Delete(ctx, gatewayClass)
			Expect(client.IgnoreNotFound(err)).ToNot(HaveOccurred())
		}
	})

	Context("invalid target", func() {

		BeforeEach(func() {
			dnsPolicy = dnsPolicyBuilder.
				WithTargetGateway("test-gateway").
				WithRoutingStrategy(v1alpha1.SimpleRoutingStrategy).
				DNSPolicy
			Expect(k8sClient.Create(ctx, dnsPolicy)).To(Succeed())
		})

		It("should have ready condition with status false and correct reason", func() {
			Eventually(func(g Gomega) {
				err := k8sClient.Get(ctx, client.ObjectKeyFromObject(dnsPolicy), dnsPolicy)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(dnsPolicy.Status.Conditions).To(
					ContainElement(MatchFields(IgnoreExtras, Fields{
						"Type":   Equal(string(conditions.ConditionTypeReady)),
						"Status": Equal(metav1.ConditionFalse),
						"Reason": Equal(string(conditions.PolicyReasonTargetNotFound)),
					})),
				)
			}, TestTimeoutMedium, time.Second).Should(Succeed())
		})

		It("should have ready condition with status true", func() {
			By("creating a valid Gateway")

			gateway = testutil.NewGatewayBuilder("test-gateway", gatewayClass.Name, testNamespace).
				WithHTTPListener("test-listener", "test.example.com").Gateway
			Expect(k8sClient.Create(ctx, gateway)).To(Succeed())

			Eventually(func(g Gomega) {
				err := k8sClient.Get(ctx, client.ObjectKeyFromObject(dnsPolicy), dnsPolicy)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(dnsPolicy.Status.Conditions).To(
					ContainElement(MatchFields(IgnoreExtras, Fields{
						"Type":   Equal(string(conditions.ConditionTypeReady)),
						"Status": Equal(metav1.ConditionTrue),
						"Reason": Equal("GatewayDNSEnabled"),
					})),
				)
			}, TestTimeoutMedium, time.Second).Should(Succeed())
		})

		It("should not have any health check records created", func() {
			// create a health check with the labels for the dnspolicy and the gateway name and namespace that would be expected in a valid target scenario
			// this one should get deleted if the gateway is invalid policy ref
			probe := &v1alpha1.DNSHealthCheckProbe{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("%s-%s-%s", TestIPAddressTwo, TestGatewayName, TestHostOne),
					Namespace: testNamespace,
					Labels: map[string]string{
						DNSPolicyBackRefAnnotation:                              "test-dns-policy",
						fmt.Sprintf("%s-namespace", DNSPolicyBackRefAnnotation): testNamespace,
						LabelGatewayNSRef:                                       testNamespace,
						LabelGatewayReference:                                   "test-gateway",
					},
				},
			}
			Expect(k8sClient.Create(ctx, probe)).To(Succeed())

			Eventually(func(g Gomega) { // probe should be removed
				err := k8sClient.Get(ctx, client.ObjectKey{Name: probe.Name, Namespace: probe.Namespace}, &v1alpha1.DNSHealthCheckProbe{})
				g.Expect(err).To(HaveOccurred())
				g.Expect(err).To(MatchError(ContainSubstring("not found")))
			}, TestTimeoutMedium, time.Second).Should(Succeed())
		})

		It("should not process gateway with inconsistent addresses", func() {
			// build invalid gateway
			gateway = testutil.NewGatewayBuilder("test-gateway", gatewayClass.Name, testNamespace).
				WithHTTPListener(TestListenerNameOne, TestHostOne).Gateway
			Expect(k8sClient.Create(ctx, gateway)).To(Succeed())

			// ensure Gateway exists and invalidate it by setting inconsistent addresses
			Eventually(func(g Gomega) {
				err := k8sClient.Get(ctx, client.ObjectKey{Name: gateway.Name, Namespace: gateway.Namespace}, gateway)
				g.Expect(err).ToNot(HaveOccurred())
				gateway.Status.Addresses = []gatewayapiv1.GatewayStatusAddress{
					{
						Type:  testutil.Pointer(gatewayapiv1.HostnameAddressType),
						Value: TestClusterNameOne + "/" + TestIPAddressOne,
					},
					{
						Type:  testutil.Pointer(common.MultiClusterIPAddressType),
						Value: TestIPAddressTwo,
					},
				}
				gateway.Status.Listeners = []gatewayapiv1.ListenerStatus{
					{
						Name:           TestClusterNameOne + "." + TestListenerNameOne,
						SupportedKinds: []gatewayapiv1.RouteGroupKind{},
						AttachedRoutes: 1,
						Conditions:     []metav1.Condition{},
					},
					{
						Name:           TestListenerNameOne,
						SupportedKinds: []gatewayapiv1.RouteGroupKind{},
						AttachedRoutes: 1,
						Conditions:     []metav1.Condition{},
					},
				}
				err = k8sClient.Status().Update(ctx, gateway)
				g.Expect(err).ToNot(HaveOccurred())
			}, TestTimeoutMedium, TestRetryIntervalMedium).Should(Succeed())

			// expect no dns records
			Consistently(func() []v1alpha1.DNSRecord {
				dnsRecords := v1alpha1.DNSRecordList{}
				err := k8sClient.List(ctx, &dnsRecords, client.InNamespace(dnsPolicy.GetNamespace()))
				Expect(err).ToNot(HaveOccurred())
				return dnsRecords.Items
			}, time.Second*15, time.Second).Should(BeEmpty())
			Eventually(func(g Gomega) {
				err := k8sClient.Get(ctx, client.ObjectKeyFromObject(dnsPolicy), dnsPolicy)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(dnsPolicy.Status.Conditions).To(
					ContainElement(MatchFields(IgnoreExtras, Fields{
						"Type":    Equal(string(conditions.ConditionTypeReady)),
						"Status":  Equal(metav1.ConditionFalse),
						"Reason":  Equal("ReconciliationError"),
						"Message": ContainSubstring("gateway is invalid: inconsistent status addresses"),
					})),
				)
			}, TestTimeoutMedium, time.Second).Should(Succeed())
		})

	})

	Context("valid target with no gateway status", func() {
		testGatewayName := "test-no-gateway-status"

		BeforeEach(func() {
			gateway = testutil.NewGatewayBuilder(testGatewayName, gatewayClass.Name, testNamespace).
				WithHTTPListener(TestListenerNameOne, TestHostOne).
				Gateway
			dnsPolicy = dnsPolicyBuilder.
				WithTargetGateway(testGatewayName).
				WithRoutingStrategy(v1alpha1.SimpleRoutingStrategy).
				DNSPolicy

			Expect(k8sClient.Create(ctx, gateway)).To(Succeed())
			Expect(k8sClient.Create(ctx, dnsPolicy)).To(Succeed())
		})

		It("should not create a dns record", func() {
			Consistently(func() []v1alpha1.DNSRecord { // DNS record exists
				dnsRecords := v1alpha1.DNSRecordList{}
				err := k8sClient.List(ctx, &dnsRecords, client.InNamespace(dnsPolicy.GetNamespace()))
				Expect(err).ToNot(HaveOccurred())
				return dnsRecords.Items
			}, time.Second*15, time.Second).Should(BeEmpty())
		})

		It("should have ready status", func() {
			Eventually(func(g Gomega) {
				err := k8sClient.Get(ctx, client.ObjectKeyFromObject(dnsPolicy), dnsPolicy)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(dnsPolicy.Status.Conditions).To(
					ContainElement(MatchFields(IgnoreExtras, Fields{
						"Type":   Equal(string(conditions.ConditionTypeReady)),
						"Status": Equal(metav1.ConditionTrue),
						"Reason": Equal("GatewayDNSEnabled"),
					})),
				)
			}, TestTimeoutMedium, time.Second).Should(Succeed())
		})

		It("should set gateway back reference", func() {
			policyBackRefValue := testNamespace + "/" + dnsPolicy.Name
			refs, _ := json.Marshal([]client.ObjectKey{{Name: dnsPolicy.Name, Namespace: testNamespace}})
			policiesBackRefValue := string(refs)

			Eventually(func(g Gomega) {
				gw := &gatewayapiv1.Gateway{}
				err := k8sClient.Get(ctx, client.ObjectKey{Name: gateway.Name, Namespace: testNamespace}, gw)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(gw.Annotations).To(HaveKeyWithValue(DNSPolicyBackRefAnnotation, policyBackRefValue))
				g.Expect(gw.Annotations).To(HaveKeyWithValue(DNSPoliciesBackRefAnnotation, policiesBackRefValue))
			}, TestTimeoutMedium, time.Second).Should(Succeed())
		})
	})

	Context("valid target and valid gateway status", func() {

		BeforeEach(func() {
			gateway = testutil.NewGatewayBuilder(TestGatewayName, gatewayClass.Name, testNamespace).
				WithHTTPListener(TestListenerNameOne, TestHostOne).
				WithHTTPListener(TestListenerNameWildcard, TestHostWildcard).
				Gateway
			dnsPolicy = dnsPolicyBuilder.WithTargetGateway(TestGatewayName).
				WithRoutingStrategy(v1alpha1.SimpleRoutingStrategy).
				DNSPolicy

			Expect(k8sClient.Create(ctx, gateway)).To(Succeed())
			Expect(k8sClient.Create(ctx, dnsPolicy)).To(Succeed())

			Eventually(func() error {
				if err := k8sClient.Create(ctx, &v1.ManagedCluster{
					ObjectMeta: metav1.ObjectMeta{
						Name: TestClusterNameOne,
					},
				}); err != nil && !k8serrors.IsAlreadyExists(err) {
					return err
				}
				if err := k8sClient.Create(ctx, &v1.ManagedCluster{
					ObjectMeta: metav1.ObjectMeta{
						Name: TestClusterNameTwo,
					},
				}); err != nil && !k8serrors.IsAlreadyExists(err) {
					return err
				}

				err := k8sClient.Get(ctx, client.ObjectKeyFromObject(gateway), gateway)
				Expect(err).ShouldNot(HaveOccurred())

				gateway.Status.Addresses = []gatewayapiv1.GatewayStatusAddress{
					{
						Type:  testutil.Pointer(common.MultiClusterIPAddressType),
						Value: TestClusterNameOne + "/" + TestIPAddressOne,
					},
					{
						Type:  testutil.Pointer(common.MultiClusterIPAddressType),
						Value: TestClusterNameTwo + "/" + TestIPAddressTwo,
					},
				}
				gateway.Status.Listeners = []gatewayapiv1.ListenerStatus{
					{
						Name:           TestClusterNameOne + "." + TestListenerNameOne,
						SupportedKinds: []gatewayapiv1.RouteGroupKind{},
						AttachedRoutes: 1,
						Conditions:     []metav1.Condition{},
					},
					{
						Name:           TestClusterNameTwo + "." + TestListenerNameOne,
						SupportedKinds: []gatewayapiv1.RouteGroupKind{},
						AttachedRoutes: 1,
						Conditions:     []metav1.Condition{},
					},
					{
						Name:           TestClusterNameOne + "." + TestListenerNameWildcard,
						SupportedKinds: []gatewayapiv1.RouteGroupKind{},
						AttachedRoutes: 1,
						Conditions:     []metav1.Condition{},
					},
					{
						Name:           TestClusterNameTwo + "." + TestListenerNameWildcard,
						SupportedKinds: []gatewayapiv1.RouteGroupKind{},
						AttachedRoutes: 1,
						Conditions:     []metav1.Condition{},
					},
				}
				return k8sClient.Status().Update(ctx, gateway)
			}, TestTimeoutMedium, TestRetryIntervalMedium).ShouldNot(HaveOccurred())

			recordName = fmt.Sprintf("%s-%s", TestGatewayName, TestListenerNameOne)
			wildcardRecordName = fmt.Sprintf("%s-%s", TestGatewayName, TestListenerNameWildcard)
		})

		It("should have correct status", func() {
			Eventually(func(g Gomega) {
				err := k8sClient.Get(ctx, client.ObjectKeyFromObject(dnsPolicy), dnsPolicy)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(dnsPolicy.Finalizers).To(ContainElement(DNSPolicyFinalizer))
				g.Expect(dnsPolicy.Status.Conditions).To(
					ContainElement(MatchFields(IgnoreExtras, Fields{
						"Type":   Equal(string(conditions.ConditionTypeReady)),
						"Status": Equal(metav1.ConditionTrue),
						"Reason": Equal("GatewayDNSEnabled"),
					})),
				)
				err = k8sClient.Get(ctx, client.ObjectKeyFromObject(gateway), gateway)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(gateway.Status.Conditions).To(
					ContainElement(MatchFields(IgnoreExtras, Fields{
						"Type":               Equal(string(DNSPolicyAffected)),
						"Status":             Equal(metav1.ConditionTrue),
						"Reason":             Equal(string(conditions.PolicyReasonAccepted)),
						"ObservedGeneration": Equal(gateway.Generation),
					})),
				)
			}, TestTimeoutMedium, time.Second).Should(Succeed())
		})

		It("should set gateway back reference", func() {
			policyBackRefValue := testNamespace + "/" + dnsPolicy.Name
			refs, _ := json.Marshal([]client.ObjectKey{{Name: dnsPolicy.Name, Namespace: testNamespace}})
			policiesBackRefValue := string(refs)

			Eventually(func(g Gomega) {
				err := k8sClient.Get(ctx, client.ObjectKeyFromObject(gateway), gateway)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(gateway.Annotations).To(HaveKeyWithValue(DNSPolicyBackRefAnnotation, policyBackRefValue))
				g.Expect(gateway.Annotations).To(HaveKeyWithValue(DNSPoliciesBackRefAnnotation, policiesBackRefValue))
			}, TestTimeoutMedium, time.Second).Should(Succeed())
		})

		It("should remove dns records when listener removed", func() {
			//get the gateway and remove the listeners

			Eventually(func() error {
				existingGateway := &gatewayapiv1.Gateway{}
				if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(gateway), existingGateway); err != nil {
					return err
				}
				newListeners := []gatewayapiv1.Listener{}
				for _, existing := range existingGateway.Spec.Listeners {
					if existing.Name == TestListenerNameWildcard {
						newListeners = append(newListeners, existing)
					}
				}

				patch := client.MergeFrom(existingGateway.DeepCopy())
				existingGateway.Spec.Listeners = newListeners
				rec := &v1alpha1.DNSRecord{}
				if err := k8sClient.Patch(ctx, existingGateway, patch); err != nil {
					return err
				}
				//dns record should be removed for non wildcard
				if err := k8sClient.Get(ctx, client.ObjectKey{Name: recordName, Namespace: testNamespace}, rec); err != nil && !k8serrors.IsNotFound(err) {
					return err
				}
				return k8sClient.Get(ctx, client.ObjectKey{Name: wildcardRecordName, Namespace: testNamespace}, rec)
			}, time.Second*10, time.Second).Should(BeNil())
		})

		It("should remove gateway back reference on policy deletion", func() {
			policyBackRefValue := testNamespace + "/" + dnsPolicy.Name
			refs, _ := json.Marshal([]client.ObjectKey{{Name: dnsPolicy.Name, Namespace: testNamespace}})
			policiesBackRefValue := string(refs)

			Eventually(func(g Gomega) {
				err := k8sClient.Get(ctx, client.ObjectKeyFromObject(gateway), gateway)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(gateway.Annotations).To(HaveKeyWithValue(DNSPolicyBackRefAnnotation, policyBackRefValue))
				g.Expect(gateway.Annotations).To(HaveKeyWithValue(DNSPoliciesBackRefAnnotation, policiesBackRefValue))
				g.Expect(gateway.Status.Conditions).To(
					ContainElement(MatchFields(IgnoreExtras, Fields{
						"Type":               Equal(string(DNSPolicyAffected)),
						"Status":             Equal(metav1.ConditionTrue),
						"Reason":             Equal(string(conditions.PolicyReasonAccepted)),
						"ObservedGeneration": Equal(gateway.Generation),
					})),
				)

				err = k8sClient.Get(ctx, client.ObjectKeyFromObject(dnsPolicy), dnsPolicy)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(dnsPolicy.Finalizers).To(ContainElement(DNSPolicyFinalizer))
			}, TestTimeoutMedium, time.Second).Should(Succeed())

			By("deleting the dns policy")
			Expect(k8sClient.Delete(ctx, dnsPolicy)).To(BeNil())

			Eventually(func(g Gomega) {
				err := k8sClient.Get(ctx, client.ObjectKeyFromObject(gateway), gateway)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(gateway.Annotations).ToNot(HaveKey(DNSPolicyBackRefAnnotation))
				g.Expect(gateway.Annotations).ToNot(HaveKeyWithValue(DNSPoliciesBackRefAnnotation, policiesBackRefValue))
				g.Expect(gateway.Status.Conditions).ToNot(
					ContainElement(MatchFields(IgnoreExtras, Fields{
						"Type": Equal(string(DNSPolicyAffected)),
					})),
				)
			}, TestTimeoutMedium, time.Second).Should(Succeed())
		})

		It("should remove dns record reference on policy deletion even if gateway is removed", func() {

			Eventually(func() error { // DNS record exists
				return k8sClient.Get(ctx, client.ObjectKey{Name: recordName, Namespace: testNamespace}, &v1alpha1.DNSRecord{})
			}, TestTimeoutMedium, TestRetryIntervalMedium).Should(Succeed())

			err := k8sClient.Delete(ctx, gateway)
			Expect(client.IgnoreNotFound(err)).ToNot(HaveOccurred())

			err = k8sClient.Delete(ctx, dnsPolicy)
			Expect(client.IgnoreNotFound(err)).ToNot(HaveOccurred())

			Eventually(func() error { // DNS record removed
				return k8sClient.Get(ctx, client.ObjectKey{Name: recordName, Namespace: testNamespace}, &v1alpha1.DNSRecord{})
			}, TestTimeoutMedium, TestRetryIntervalMedium).Should(MatchError(ContainSubstring("not found")))

		})

	})

})
