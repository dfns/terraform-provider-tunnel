package kubernetes

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"time"

	"github.com/dfns/terraform-provider-tunnel/internal/libs"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
)

var TunnelType string = "kubernetes"

type TunnelConfig struct {
	Namespace   string
	ServiceName string
	TargetPort  int
	LocalHost   string
	LocalPort   int

	// Kubernetes Configuration
	Host                  string
	Username              string
	Password              string
	Insecure              bool
	TLSServerName         string
	ClientCertificate     string
	ClientKey             string
	ClusterCACertificate  string
	ConfigPaths           []string
	ConfigPath            string
	ConfigContext         string
	ConfigContextAuthInfo string
	ConfigContextCluster  string
	Token                 string
	ProxyURL              string
	Exec                  *ExecConfig
}

type ExecConfig struct {
	APIVersion string
	Command    string
	Env        map[string]string
	Args       []string
}

func ForkRemoteTunnel(ctx context.Context, cfg TunnelConfig) (*exec.Cmd, error) {
	tunnelCfgJson, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}

	// Open a log file for the tunnel
	tunnelLogPath := filepath.Join(os.TempDir(), fmt.Sprintf("k8s-tunnel-%s-%s-%d.log", cfg.Namespace, cfg.ServiceName, cfg.TargetPort))
	tunnelLogFile, err := os.OpenFile(tunnelLogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, err
	}

	// Prepare the command
	cmd := exec.Command(os.Args[0], strconv.Itoa(os.Getppid()))

	// Append tunnel config environment variable to pass parameters to the child process
	cmd.Env = append(
		os.Environ(),
		fmt.Sprintf("%s=%s", libs.TunnelTypeEnv, TunnelType),
		fmt.Sprintf("%s=%s", libs.TunnelConfEnv, string(tunnelCfgJson)),
	)

	// Redirect stdout and stderr to log file
	cmd.Stdout = tunnelLogFile
	cmd.Stderr = tunnelLogFile

	// Run the command in the background
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	time.Sleep(2 * time.Second)

	if err = libs.CheckProcessExists(cmd.Process.Pid); err != nil {
		return nil, fmt.Errorf("tunnel process failed to start. check %s for more information", tunnelLogPath)
	}

	return cmd, nil
}

func StartRemoteTunnel(ctx context.Context, cfgJson string, parentPid int) (err error) {
	var cfg TunnelConfig
	if err := json.Unmarshal([]byte(cfgJson), &cfg); err != nil {
		return err
	}

	// Watch parent process lifecycle ie. main terraform process
	err = libs.WatchProcess(parentPid)
	if err != nil {
		return err
	}

	log.Printf("starting tunnel: %s:%d -> %s/%s:%d", cfg.LocalHost, cfg.LocalPort, cfg.Namespace, cfg.ServiceName, cfg.TargetPort)

	// Load kubeconfig
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if len(cfg.ConfigPaths) > 0 {
		loadingRules.Precedence = cfg.ConfigPaths
	} else if cfg.ConfigPath != "" {
		loadingRules.ExplicitPath = cfg.ConfigPath
	}

	configOverrides := &clientcmd.ConfigOverrides{}
	if cfg.ConfigContext != "" {
		configOverrides.CurrentContext = cfg.ConfigContext
	}
	if cfg.ConfigContextAuthInfo != "" {
		configOverrides.Context.AuthInfo = cfg.ConfigContextAuthInfo
	}
	if cfg.ConfigContextCluster != "" {
		configOverrides.Context.Cluster = cfg.ConfigContextCluster
	}
	if cfg.Token != "" {
		configOverrides.AuthInfo.Token = cfg.Token
	}
	if cfg.Username != "" {
		configOverrides.AuthInfo.Username = cfg.Username
	}
	if cfg.Password != "" {
		configOverrides.AuthInfo.Password = cfg.Password
	}
	if cfg.ClientCertificate != "" {
		configOverrides.AuthInfo.ClientCertificateData = []byte(cfg.ClientCertificate)
	}
	if cfg.ClientKey != "" {
		configOverrides.AuthInfo.ClientKeyData = []byte(cfg.ClientKey)
	}
	if cfg.ClusterCACertificate != "" {
		configOverrides.ClusterInfo.CertificateAuthorityData = []byte(cfg.ClusterCACertificate)
	}
	if cfg.Host != "" {
		configOverrides.ClusterInfo.Server = cfg.Host
	}
	if cfg.Insecure {
		configOverrides.ClusterInfo.InsecureSkipTLSVerify = true
	}
	if cfg.TLSServerName != "" {
		configOverrides.ClusterInfo.TLSServerName = cfg.TLSServerName
	}
	if cfg.ProxyURL != "" {
		configOverrides.ClusterInfo.ProxyURL = cfg.ProxyURL
	}
	if cfg.Exec != nil {
		configOverrides.AuthInfo.Exec = &clientcmdapi.ExecConfig{
			APIVersion:      cfg.Exec.APIVersion,
			Command:         cfg.Exec.Command,
			Args:            cfg.Exec.Args,
			Env:             make([]clientcmdapi.ExecEnvVar, 0, len(cfg.Exec.Env)),
			InteractiveMode: clientcmdapi.IfAvailableExecInteractiveMode,
		}
		for k, v := range cfg.Exec.Env {
			configOverrides.AuthInfo.Exec.Env = append(configOverrides.AuthInfo.Exec.Env, clientcmdapi.ExecEnvVar{
				Name:  k,
				Value: v,
			})
		}
	}

	kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)

	clientConfig, err := kubeConfig.ClientConfig()
	if err != nil {
		return fmt.Errorf("failed to load kubeconfig: %w", err)
	}

	// Create Kubernetes Clientset
	clientSet, err := kubernetes.NewForConfig(clientConfig)
	if err != nil {
		return fmt.Errorf("failed to create k8s client: %w", err)
	}

	transport, upgrader, err := spdy.RoundTripperFor(clientConfig)
	if err != nil {
		return fmt.Errorf("failed to create round tripper: %w", err)
	}

	// Get the service to find the selector
	service, err := clientSet.CoreV1().Services(cfg.Namespace).Get(ctx, cfg.ServiceName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get service %s: %w", cfg.ServiceName, err)
	}

	// List pods matching the service selector
	selector := metav1.FormatLabelSelector(&metav1.LabelSelector{MatchLabels: service.Spec.Selector})
	pods, err := clientSet.CoreV1().Pods(cfg.Namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return fmt.Errorf("failed to list pods for service %s: %w", cfg.ServiceName, err)
	}

	if len(pods.Items) == 0 {
		return fmt.Errorf("no pods found for service %s", cfg.ServiceName)
	}

	// Pick the first pod
	podName := pods.Items[0].Name
	log.Printf("forwarding to pod: %s", podName)

	// Create portforward request
	req := clientSet.RESTClient().Post().
		Prefix("api/v1").
		Resource("pods").
		Namespace(cfg.Namespace).
		Name(podName).
		SubResource("portforward")

	stopChan := make(chan struct{}, 1)
	readyChan := make(chan struct{})

	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, "POST", req.URL())

	pf, err := portforward.NewOnAddresses(
		dialer,
		[]string{cfg.LocalHost},
		[]string{fmt.Sprintf("%d:%d", cfg.LocalPort, cfg.TargetPort)},
		stopChan,
		readyChan,
		os.Stdout,
		os.Stderr,
	)
	if err != nil {
		return fmt.Errorf("failed to create port forwarder: %w", err)
	}

	// Handle interrupt signal
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	go func() {
		<-c
		log.Println("stopping tunnel: received interrupt signal")
		close(stopChan)
	}()

	go func() {
		<-readyChan
		log.Println("port forwarding is ready")
	}()

	if err := pf.ForwardPorts(); err != nil {
		log.Printf("port forwarding failed: %v", err)
		return err
	}

	return nil
}
