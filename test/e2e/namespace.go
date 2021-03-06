/*
Copyright 2014 The Kubernetes Authors All rights reserved.

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

package e2e

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"k8s.io/kubernetes/pkg/api"
	"k8s.io/kubernetes/pkg/api/errors"
	"k8s.io/kubernetes/pkg/util/intstr"
	"k8s.io/kubernetes/pkg/util/wait"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

func extinguish(f *Framework, totalNS int, maxAllowedAfterDel int, maxSeconds int) {
	var err error

	By("Creating testing namespaces")
	wg := &sync.WaitGroup{}
	wg.Add(totalNS)
	for n := 0; n < totalNS; n += 1 {
		go func(n int) {
			defer wg.Done()
			defer GinkgoRecover()
			_, err = f.CreateNamespace(fmt.Sprintf("nslifetest-%v", n), nil)
			Expect(err).NotTo(HaveOccurred())
		}(n)
	}
	wg.Wait()

	//Wait 10 seconds, then SEND delete requests for all the namespaces.
	By("Waiting 10 seconds")
	time.Sleep(time.Duration(10 * time.Second))
	deleted, err := deleteNamespaces(f.Client, []string{"nslifetest"}, nil /* skipFilter */)
	Expect(err).NotTo(HaveOccurred())
	Expect(len(deleted)).To(Equal(totalNS))

	By("Waiting for namespaces to vanish")
	//Now POLL until all namespaces have been eradicated.
	expectNoError(wait.Poll(2*time.Second, time.Duration(maxSeconds)*time.Second,
		func() (bool, error) {
			var cnt = 0
			nsList, err := f.Client.Namespaces().List(api.ListOptions{})
			if err != nil {
				return false, err
			}
			for _, item := range nsList.Items {
				if strings.Contains(item.Name, "nslifetest") {
					cnt++
				}
			}
			if cnt > maxAllowedAfterDel {
				Logf("Remaining namespaces : %v", cnt)
				return false, nil
			}
			return true, nil
		}))
}

func ensurePodsAreRemovedWhenNamespaceIsDeleted(f *Framework) {
	var err error

	By("Creating a test namespace")
	namespace, err := f.CreateNamespace("nsdeletetest", nil)
	Expect(err).NotTo(HaveOccurred())

	By("Waiting for a default service account to be provisioned in namespace")
	err = waitForDefaultServiceAccountInNamespace(f.Client, namespace.Name)
	Expect(err).NotTo(HaveOccurred())

	By("Creating a pod in the namespace")
	pod := &api.Pod{
		ObjectMeta: api.ObjectMeta{
			Name: "test-pod",
		},
		Spec: api.PodSpec{
			Containers: []api.Container{
				{
					Name:  "nginx",
					Image: "gcr.io/google_containers/pause:2.0",
				},
			},
		},
	}
	pod, err = f.Client.Pods(namespace.Name).Create(pod)
	Expect(err).NotTo(HaveOccurred())

	By("Waiting for the pod to have running status")
	expectNoError(waitForPodRunningInNamespace(f.Client, pod.Name, pod.Namespace))

	By("Deleting the namespace")
	err = f.Client.Namespaces().Delete(namespace.Name)
	Expect(err).NotTo(HaveOccurred())

	By("Waiting for the namespace to be removed.")
	maxWaitSeconds := int64(60) + *pod.Spec.TerminationGracePeriodSeconds
	expectNoError(wait.Poll(1*time.Second, time.Duration(maxWaitSeconds)*time.Second,
		func() (bool, error) {
			_, err = f.Client.Namespaces().Get(namespace.Name)
			if err != nil && errors.IsNotFound(err) {
				return true, nil
			}
			return false, nil
		}))

	By("Verifying there is no pod in the namespace")
	_, err = f.Client.Pods(namespace.Name).Get(pod.Name)
	Expect(err).To(HaveOccurred())
}

func ensureServicesAreRemovedWhenNamespaceIsDeleted(f *Framework) {
	var err error

	By("Creating a test namespace")
	namespace, err := f.CreateNamespace("nsdeletetest", nil)
	Expect(err).NotTo(HaveOccurred())

	By("Waiting for a default service account to be provisioned in namespace")
	err = waitForDefaultServiceAccountInNamespace(f.Client, namespace.Name)
	Expect(err).NotTo(HaveOccurred())

	By("Creating a service in the namespace")
	serviceName := "test-service"
	labels := map[string]string{
		"foo": "bar",
		"baz": "blah",
	}
	service := &api.Service{
		ObjectMeta: api.ObjectMeta{
			Name: serviceName,
		},
		Spec: api.ServiceSpec{
			Selector: labels,
			Ports: []api.ServicePort{{
				Port:       80,
				TargetPort: intstr.FromInt(80),
			}},
		},
	}
	service, err = f.Client.Services(namespace.Name).Create(service)
	Expect(err).NotTo(HaveOccurred())

	By("Deleting the namespace")
	err = f.Client.Namespaces().Delete(namespace.Name)
	Expect(err).NotTo(HaveOccurred())

	By("Waiting for the namespace to be removed.")
	maxWaitSeconds := int64(60)
	expectNoError(wait.Poll(1*time.Second, time.Duration(maxWaitSeconds)*time.Second,
		func() (bool, error) {
			_, err = f.Client.Namespaces().Get(namespace.Name)
			if err != nil && errors.IsNotFound(err) {
				return true, nil
			}
			return false, nil
		}))

	By("Verifying there is no service in the namespace")
	_, err = f.Client.Services(namespace.Name).Get(service.Name)
	Expect(err).To(HaveOccurred())
}

// This test must run [Serial] due to the impact of running other parallel
// tests can have on its performance.  Each test that follows the common
// test framework follows this pattern:
//   1. Create a Namespace
//   2. Do work that generates content in that namespace
//   3. Delete a Namespace
// Creation of a Namespace is non-trivial since it requires waiting for a
// ServiceAccount to be generated.
// Deletion of a Namespace is non-trivial and performance intensive since
// its an orchestrated process.  The controller that handles deletion must
// query the namespace for all existing content, and then delete each piece
// of content in turn.  As the API surface grows to add more KIND objects
// that could exist in a Namespace, the number of calls that the namespace
// controller must orchestrate grows since it must LIST, DELETE (1x1) each
// KIND.
// There is work underway to improve this, but it's
// most likely not going to get significantly better until etcd v3.
// Going back to this test, this test generates 100 Namespace objects, and then
// rapidly deletes all of them.  This causes the NamespaceController to observe
// and attempt to process a large number of deletes concurrently.  In effect,
// it's like running 100 traditional e2e tests in parallel.  If the namespace
// controller orchestrating deletes is slowed down deleting another test's
// content then this test may fail.  Since the goal of this test is to soak
// Namespace creation, and soak Namespace deletion, its not appropriate to
// further soak the cluster with other parallel Namespace deletion activities
// that each have a variable amount of content in the associated Namespace.
// When run in [Serial] this test appears to delete Namespace objects at a
// rate of approximately 1 per second.
var _ = KubeDescribe("Namespaces [Serial]", func() {

	f := NewDefaultFramework("namespaces")

	It("should ensure that all pods are removed when a namespace is deleted.",
		func() { ensurePodsAreRemovedWhenNamespaceIsDeleted(f) })

	It("should ensure that all services are removed when a namespace is deleted.",
		func() { ensureServicesAreRemovedWhenNamespaceIsDeleted(f) })

	It("should delete fast enough (90 percent of 100 namespaces in 150 seconds)",
		func() { extinguish(f, 100, 10, 150) })

	// On hold until etcd3; see #7372
	It("should always delete fast (ALL of 100 namespaces in 150 seconds) [Feature:ComprehensiveNamespaceDraining]",
		func() { extinguish(f, 100, 0, 150) })

})
