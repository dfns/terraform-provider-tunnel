package kubernetes

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
)

// endpoint is the ready pod and numeric pod port selected for one forwarding
// session.
type endpoint struct {
	Pod  string
	Port int32
}

func (e endpoint) String() string {
	return fmt.Sprintf("%s:%d", e.Pod, e.Port)
}

// resolveEndpoint deterministically picks one ready pod and its numeric port for
// the lifetime of the tunnel.
func resolveEndpoint(ctx context.Context, client kubernetes.Interface, cfg TunnelConfig) (endpoint, error) {
	service, err := client.CoreV1().Services(cfg.Namespace).Get(ctx, cfg.ServiceName, metav1.GetOptions{})
	if err != nil {
		return endpoint{}, fmt.Errorf("get service %s/%s: %w", cfg.Namespace, cfg.ServiceName, err)
	}
	if len(service.Spec.Selector) == 0 {
		return endpoint{}, fmt.Errorf("service %s/%s has no pod selector", cfg.Namespace, cfg.ServiceName)
	}

	servicePort := findServicePort(service, cfg.TargetPort)

	selector := metav1.FormatLabelSelector(&metav1.LabelSelector{MatchLabels: service.Spec.Selector})
	pods, err := client.CoreV1().Pods(cfg.Namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return endpoint{}, fmt.Errorf("list pods for service %s/%s: %w", cfg.Namespace, cfg.ServiceName, err)
	}
	sort.Slice(pods.Items, func(i, j int) bool { return pods.Items[i].Name < pods.Items[j].Name })

	var portErr error
	for i := range pods.Items {
		pod := &pods.Items[i]
		if !podReady(pod) {
			continue
		}
		port, err := resolvePodPort(pod, cfg.TargetPort, servicePort)
		if err != nil {
			portErr = err
			continue
		}
		if servicePort == nil {
			// Keep the original provider behavior: a target_port not exposed by
			// the Service is treated as a pod port and forwarded unchanged. Named
			// here so the warning includes the pod the tunnel actually bypasses to.
			log.Printf(
				"service %s/%s does not expose port %d (exposed: %s); treating %d as a pod port on %s",
				cfg.Namespace, cfg.ServiceName, cfg.TargetPort,
				describeServicePorts(service), cfg.TargetPort, pod.Name,
			)
		}
		return endpoint{Pod: pod.Name, Port: port}, nil
	}

	// portErr is only ever set from a ready pod, so testing it first both reports
	// the actionable failure and keeps this %w from wrapping a nil error.
	if portErr != nil {
		return endpoint{}, fmt.Errorf("resolve target port for service %s/%s: %w", cfg.Namespace, cfg.ServiceName, portErr)
	}
	return endpoint{}, fmt.Errorf("service %s/%s has no ready, non-terminating pods", cfg.Namespace, cfg.ServiceName)
}

// describeServicePorts lists the ports the Service does expose, so a mistyped
// target_port is diagnosable from the tunnel log instead of degrading into a
// bare connection failure.
func describeServicePorts(service *corev1.Service) string {
	if len(service.Spec.Ports) == 0 {
		return "none"
	}
	described := make([]string, 0, len(service.Spec.Ports))
	for _, port := range service.Spec.Ports {
		if port.Name != "" {
			described = append(described, fmt.Sprintf("%d (%s)", port.Port, port.Name))
			continue
		}
		described = append(described, strconv.Itoa(int(port.Port)))
	}
	return strings.Join(described, ", ")
}

func findServicePort(service *corev1.Service, port int) *corev1.ServicePort {
	for i := range service.Spec.Ports {
		if int(service.Spec.Ports[i].Port) == port {
			return &service.Spec.Ports[i]
		}
	}
	return nil
}

func resolvePodPort(pod *corev1.Pod, configuredPort int, servicePort *corev1.ServicePort) (int32, error) {
	if servicePort == nil {
		return int32(configuredPort), nil
	}
	switch servicePort.TargetPort.Type {
	case intstr.Int:
		if servicePort.TargetPort.IntVal != 0 {
			return servicePort.TargetPort.IntVal, nil
		}
		// Kubernetes defaults an omitted targetPort to the Service port.
		return servicePort.Port, nil
	case intstr.String:
		name := servicePort.TargetPort.StrVal
		for _, container := range pod.Spec.Containers {
			for _, port := range container.Ports {
				if port.Name == name {
					return port.ContainerPort, nil
				}
			}
		}
		return 0, fmt.Errorf("pod %s has no container port named %q", pod.Name, name)
	default:
		return 0, fmt.Errorf("service port has unsupported targetPort %q", servicePort.TargetPort.String())
	}
}

func podReady(pod *corev1.Pod) bool {
	if pod.DeletionTimestamp != nil || pod.Status.Phase != corev1.PodRunning {
		return false
	}
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}
