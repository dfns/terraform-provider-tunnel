package kubernetes

import (
	"bytes"
	"context"
	"log"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"
)

const (
	testNamespace = "default"
	testService   = "web"
)

func serviceConfig() TunnelConfig {
	return TunnelConfig{Namespace: testNamespace, ServiceName: testService, TargetPort: 80}
}

func service(targetPort intstr.IntOrString) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: testService, Namespace: testNamespace},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": "web"},
			Ports:    []corev1.ServicePort{{Port: 80, TargetPort: targetPort}},
		},
	}
}

func pod(name string, ready bool, ports ...corev1.ContainerPort) *corev1.Pod {
	readyStatus := corev1.ConditionFalse
	if ready {
		readyStatus = corev1.ConditionTrue
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: testNamespace, Labels: map[string]string{"app": "web"},
		},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Ports: ports}}},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{{
				Type: corev1.PodReady, Status: readyStatus,
			}},
		},
	}
}

func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	captured := &bytes.Buffer{}
	previous := log.Writer()
	log.SetOutput(captured)
	t.Cleanup(func() { log.SetOutput(previous) })
	return captured
}

func TestResolveEndpointNumericTargetPort(t *testing.T) {
	client := fake.NewClientset(service(intstr.FromInt32(8080)), pod("web-1", true))

	got, err := resolveEndpoint(context.Background(), client, serviceConfig())
	if err != nil {
		t.Fatalf("resolveEndpoint() error = %v", err)
	}
	if want := (endpoint{Pod: "web-1", Port: 8080}); got != want {
		t.Fatalf("endpoint = %v, want %v", got, want)
	}
}

func TestResolveEndpointNamedTargetPort(t *testing.T) {
	client := fake.NewClientset(
		service(intstr.FromString("http")),
		pod("web-1", true, corev1.ContainerPort{Name: "http", ContainerPort: 9090}),
	)

	got, err := resolveEndpoint(context.Background(), client, serviceConfig())
	if err != nil {
		t.Fatalf("resolveEndpoint() error = %v", err)
	}
	if want := (endpoint{Pod: "web-1", Port: 9090}); got != want {
		t.Fatalf("endpoint = %v, want %v", got, want)
	}
}

func TestResolveEndpointSkipsUnreadyAndTerminatingPods(t *testing.T) {
	deleting := pod("web-0", true)
	now := metav1.Now()
	deleting.DeletionTimestamp = &now
	client := fake.NewClientset(
		service(intstr.FromInt32(8080)),
		deleting,
		pod("web-1", false),
		pod("web-2", true),
	)

	got, err := resolveEndpoint(context.Background(), client, serviceConfig())
	if err != nil {
		t.Fatalf("resolveEndpoint() error = %v", err)
	}
	if got.Pod != "web-2" {
		t.Fatalf("selected pod = %q, want web-2", got.Pod)
	}
}

func TestResolveEndpointIsDeterministic(t *testing.T) {
	client := fake.NewClientset(
		service(intstr.FromInt32(8080)),
		pod("web-z", true),
		pod("web-a", true),
	)

	got, err := resolveEndpoint(context.Background(), client, serviceConfig())
	if err != nil {
		t.Fatalf("resolveEndpoint() error = %v", err)
	}
	if got.Pod != "web-a" {
		t.Fatalf("selected pod = %q, want lexically first ready pod web-a", got.Pod)
	}
}

func TestResolveEndpointReportsNoReadyPod(t *testing.T) {
	client := fake.NewClientset(service(intstr.FromInt32(8080)), pod("web-1", false))

	_, err := resolveEndpoint(context.Background(), client, serviceConfig())
	if err == nil || !strings.Contains(err.Error(), "no ready") {
		t.Fatalf("resolveEndpoint() error = %v, want no-ready-pod error", err)
	}
}

func TestResolveEndpointKeepsDirectPodPortFallback(t *testing.T) {
	cfg := serviceConfig()
	cfg.TargetPort = 9153
	svc := service(intstr.FromInt32(8080))
	svc.Spec.Ports[0].Name = "http"
	client := fake.NewClientset(svc, pod("web-1", true))

	logs := captureLogs(t)
	got, err := resolveEndpoint(context.Background(), client, cfg)
	if err != nil {
		t.Fatalf("resolveEndpoint() error = %v", err)
	}
	if got.Port != 9153 {
		t.Fatalf("selected port = %d, want configured pod port 9153", got.Port)
	}
	// The warning is the only hint a mistyped target_port gets, so it has to name
	// the ports the Service does expose and the pod this session resolved to.
	for _, want := range []string{"does not expose port 9153", "80 (http)", "web-1"} {
		if logged := logs.String(); !strings.Contains(logged, want) {
			t.Fatalf("fallback warning = %q, want it to contain %q", logged, want)
		}
	}
}

func TestResolveEndpointTriesNextReadyPodForNamedPort(t *testing.T) {
	client := fake.NewClientset(
		service(intstr.FromString("http")),
		pod("web-a", true),
		pod("web-b", true, corev1.ContainerPort{Name: "http", ContainerPort: 9090}),
	)

	got, err := resolveEndpoint(context.Background(), client, serviceConfig())
	if err != nil {
		t.Fatalf("resolveEndpoint() error = %v", err)
	}
	if got.Pod != "web-b" || got.Port != 9090 {
		t.Fatalf("endpoint = %v, want web-b:9090", got)
	}
}
