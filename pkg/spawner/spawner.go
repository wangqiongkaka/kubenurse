// Package spawner implements spawning of patient pods
package spawner

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
)

// Spawner defines the type for spawning patient pods
type Spawner struct {
	PatientNamespace string
	PatientImage     string
	ServiceURL       string
	IngressURL       string
	PodTemplate      []byte
}

// RunScheduled runs the patient check in the specified interval which can be used
// to keep the metrics up-to-date
func (spw *Spawner) RunScheduled(d time.Duration) {
	cs, err := getClientset()
	if err != nil {
		log.Fatalln(err)
	}

	for range time.Tick(d) {
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second) // TODO: configure timeout

		err := spw.Run(ctx, cs)
		if err != nil {
			// TODO: Create metric
			log.Printf("running spawner: %s", err)
		}

		cancel()
	}
}

// Run runs the patient check and returns the result
func (spw *Spawner) Run(ctx context.Context, clientset kubernetes.Interface) error {
	// Decode template
	tmpPod, err := decodePodTemplate(spw.PodTemplate)
	if err != nil {
		return fmt.Errorf("decoding pod template: %w", err)
	}

	// Get data about myself
	myName, _ := os.Hostname()

	parentPod, err := clientset.CoreV1().Pods(spw.PatientNamespace).Get(ctx, myName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("fetching data about myself: %w", err)
	}

	// Set properties
	spw.configurePod(tmpPod, parentPod)

	// Create Pod
	pod, err := clientset.CoreV1().Pods(spw.PatientNamespace).Create(ctx, tmpPod, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("creating patient pod: %w", err)
	}

	// Garbage collect the patient pod at the end
	defer spw.cleanupPod(clientset, pod)

	// Wait until pod got an IP
	err = waitForIP(clientset, spw.PatientNamespace, pod.Name)
	if err != nil {
		return fmt.Errorf("waiting on patient pod to get an IP: %w", err)
	}

	// TODO: Try to crawl pod until it succeeds for metric
	// TODO: Crawl pod until it has all metrics available, transported by http and give when timeout is reached

	// Delete pod
	err = clientset.CoreV1().Pods(spw.PatientNamespace).Delete(ctx, pod.Name, metav1.DeleteOptions{})
	if err != nil {
		return fmt.Errorf("deleting patient pod: %w", err)
	}

	return nil
}

// cleanupPod kills the patient pod when it's not needed anymore
func (spw *Spawner) cleanupPod(clientset kubernetes.Interface, pod *apiv1.Pod) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := clientset.CoreV1().Pods(spw.PatientNamespace).Delete(ctx, pod.Name, metav1.DeleteOptions{})
	if err != nil {
		// TODO: Add metric
		log.Printf("deleting patient pod: %s", err)
	}
}

// configurePod manipulates the tmpl Pod and sets all attributes required to
// create a patient pod
func (spw *Spawner) configurePod(tmpl *apiv1.Pod, parent *apiv1.Pod) {
	tmpl.ObjectMeta.Name = parent.GetName() + "-patient"
	tmpl.ObjectMeta.OwnerReferences = []metav1.OwnerReference{
		{
			APIVersion: "v1",
			Kind:       "Pod",
			Name:       parent.GetName(),
			UID:        parent.GetObjectMeta().GetUID(),
		},
	}

	if len(tmpl.Spec.Containers) == 0 {
		tmpl.Spec.Containers = []apiv1.Container{{
			Name: "patient",
			Args: []string{"patient"},
		}}
	}

	tmpl.Spec.Containers[0].Image = spw.PatientImage
	tmpl.Spec.Containers[0].Env = []apiv1.EnvVar{
		{
			Name:  "KUBENURSE_DIRECT_URL",
			Value: fmt.Sprintf("http://%s:8080", parent.Status.PodIP),
		},
		{
			Name:  "KUBENURSE_DNS_URL",
			Value: fmt.Sprintf("http://%s:8080", getPodSDHostname(parent)),
		},
		{
			Name:  "KUBENURSE_INGRESS_URL",
			Value: spw.IngressURL,
		},
		{
			Name:  "KUBENURSE_SERVICE_URL",
			Value: spw.ServiceURL,
		},
	}
}

// waitForIP waits until a pod has gotten an IP
// TODO Implement timeout
func waitForIP(clientset kubernetes.Interface, namespace string, podName string) error {
	c := make(chan struct{})
	defer close(c)

	informer := informers.NewSharedInformerFactoryWithOptions(
		clientset,
		0,
		informers.WithNamespace(namespace)).Core().V1().Pods().Informer()

	checkForIP := func(p *apiv1.Pod) {
		if p.Name == podName && p.Status.PodIP != "" {
			// Pod got an IP
			c <- struct{}{}
		}
	}

	informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(newObj interface{}) {
			p := newObj.(*apiv1.Pod)
			checkForIP(p)
		},
		UpdateFunc: func(oldObj interface{}, newObj interface{}) {
			p := newObj.(*apiv1.Pod)
			checkForIP(p)
		},
		DeleteFunc: func(deadObj interface{}) {
			p := deadObj.(*apiv1.Pod)
			checkForIP(p)
		},
	})

	informer.Run(c)

	return nil
}

// decodePodTemplate decodes the template yaml or json to a apiv1.Pod resource.
func decodePodTemplate(tmpl []byte) (*apiv1.Pod, error) {
	sch := runtime.NewScheme()

	err := clientgoscheme.AddToScheme(sch)
	if err != nil {
		return nil, fmt.Errorf("building scheme: %w", err)
	}

	pod := &apiv1.Pod{}
	decode := serializer.NewCodecFactory(sch).UniversalDeserializer().Decode

	_, groupVersionKind, err := decode(tmpl, nil, pod)
	if err != nil {
		return nil, fmt.Errorf("deserializing pod template: %w", err)
	}

	if groupVersionKind.GroupKind().Kind != "Pod" {
		return nil, fmt.Errorf("pod template is not of type Pod, got: %q", groupVersionKind)
	}

	return pod, nil
}

// getPodSDHostname returns the FQDN to reach a pod directly
func getPodSDHostname(p *apiv1.Pod) string {
	ip := strings.Replace(p.Status.PodIP, ".", "-", 4)
	return fmt.Sprintf("%s.default.pod.cluster.local", ip)
}

// getClientset creates the in-cluster client for kubernetes
func getClientset() (kubernetes.Interface, error) {
	// Create kubernetes client
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("getting kubernetes in-cluster config: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("creating kubernetes clientset: %w", err)
	}

	return clientset, err
}
