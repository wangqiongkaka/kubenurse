package spawner

import (
	"context"
	"testing"
	"time"

	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestDecodePodTemplate(t *testing.T) {
	tmpl := `---
apiVersion: v1
kind: Pod
spec:
  containers:
  - imagePullPolicy: Always
  volumes:
  - name: nfs-check
    persistentVolumeClaim:
      claimName: nfs-check
`

	pod, err := decodePodTemplate([]byte(tmpl))
	if err != nil {
		t.Error(err)
	}

	if pod.Spec.Volumes[0].PersistentVolumeClaim.ClaimName != "nfs-check" {
		t.Error("Deserialized pod definition is missing values")
	}

	_, err = decodePodTemplate(nil)
	if err != nil {
		t.Error(err)
	}
}

func TestGetPodSDHostname(t *testing.T) {
	p := &apiv1.Pod{
		Status: apiv1.PodStatus{
			PodIP: "10.22.33.4",
		},
	}

	exp := "10-22-33-4.default.pod.cluster.local"

	addr := getPodSDHostname(p)

	if addr != exp {
		t.Errorf("Expected %q, got %q", exp, addr)
	}
}

func TestWaitForIP(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset()

	// Create dummy pod
	p := &apiv1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "my-pod"}}

	_, err := client.CoreV1().Pods("test-ns").Create(ctx, p, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}

	// Start waiting for IP
	waitErr := make(chan error, 1)
	go func() {
		waitErr <- waitForIP(client, "test-ns", "my-pod")
	}()

	// Inject IP
	p.Status.PodIP = "10.11.12.13"
	_, err = client.CoreV1().Pods("test-ns").UpdateStatus(ctx, p, metav1.UpdateOptions{})
	if err != nil {
		t.Fatal(err)
	}

	/* Show myPod
	myPod, err := client.CoreV1().Pods("test-ns").Get(ctx, "my-pod", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}

	t.Log(myPod)
	*/

	// Check the waiting mechanism
	select {
	case res := <-waitErr:
		if res != nil {
			t.Errorf("Got waitForIP err: %s", err)
		}
	case <-time.After(3 * time.Second):
		t.Error("timeout reached, waitForIP didn't return")
	}
}

func TestConfigurePod(t *testing.T) {
	//func (spw *Spawner) configurePod(tmpl *apiv1.Pod, parent *apiv1.Pod) {
	// TODO
	t.SkipNow()
}

func TestCleanupPod(t *testing.T) {
	//func (spw *Spawner) cleanupPod(clientset kubernetes.Interface, pod *apiv1.Pod) {
	// TODO
	t.SkipNow()
}

func TestRun(t *testing.T) {
	// func (spw *Spawner) Run(ctx context.Context, clientset kubernetes.Interface) error {
	// TODO
}
