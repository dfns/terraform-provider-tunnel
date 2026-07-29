package azurebastion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/cloud"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v10"
	"github.com/dfns/terraform-provider-tunnel/internal/libs"
)

const TunnelType = "azure_bastion"

// DefaultLocalHost avoids binding every interface when local_host is unset.
const DefaultLocalHost = "localhost"

type TunnelConfig struct {
	BastionHostID    string `json:"bastion_host_id"`
	TargetResourceID string `json:"target_resource_id,omitempty"`
	TargetIPAddress  string `json:"target_ip_address,omitempty"`
	TargetPort       int    `json:"target_port"`
	LocalHost        string `json:"local_host"`
	LocalPort        int    `json:"local_port"`
}

type bastionInfo struct {
	DNSName         string
	SKU             string
	EnableTunneling bool
	EnableIPConnect bool
}

type bastionHostGetter interface {
	Get(
		context.Context,
		string,
		string,
		*armnetwork.BastionHostsClientGetOptions,
	) (armnetwork.BastionHostsClientGetResponse, error)
}

// tunnelPlan is the normalized, validated tunnel configuration.
type tunnelPlan struct {
	Bastion          *arm.ResourceID
	TargetResourceID string
	Hostname         string
	TargetPort       int
	LocalHost        string
	LocalPort        int
}

const bastionResourceType = "Microsoft.Network/bastionHosts"

// parseBastionHostID rejects non-Bastion and nested ARM resource IDs.
func parseBastionHostID(id string) (*arm.ResourceID, error) {
	parsed, err := arm.ParseResourceID(id)
	if err != nil || parsed.SubscriptionID == "" || parsed.ResourceGroupName == "" ||
		parsed.Name == "" || !strings.EqualFold(parsed.ResourceType.String(), bastionResourceType) {
		return nil, errors.New("bastion_host_id must identify a Microsoft.Network/bastionHosts resource")
	}
	return parsed, nil
}

func (c TunnelConfig) Validate() error {
	_, err := c.resolve()
	return err
}

func (c TunnelConfig) resolve() (tunnelPlan, error) {
	bastionID, err := parseBastionHostID(c.BastionHostID)
	if err != nil {
		return tunnelPlan{}, err
	}
	hasResource := strings.TrimSpace(c.TargetResourceID) != ""
	hasIP := strings.TrimSpace(c.TargetIPAddress) != ""
	if hasResource == hasIP {
		return tunnelPlan{}, errors.New("exactly one of target_resource_id or target_ip_address must be set")
	}
	localHost := strings.TrimSpace(c.LocalHost)
	if localHost == "" {
		localHost = DefaultLocalHost
	}
	plan := tunnelPlan{
		Bastion:    bastionID,
		TargetPort: c.TargetPort,
		LocalHost:  localHost,
		LocalPort:  c.LocalPort,
	}
	if hasResource {
		target, err := arm.ParseResourceID(c.TargetResourceID)
		if err != nil {
			return tunnelPlan{}, fmt.Errorf("target_resource_id: %w", err)
		}
		if target.Name == "" {
			return tunnelPlan{}, fmt.Errorf("target_resource_id: missing resource name in %q", c.TargetResourceID)
		}
		plan.TargetResourceID = c.TargetResourceID
	} else {
		if net.ParseIP(c.TargetIPAddress) == nil {
			return tunnelPlan{}, errors.New("target_ip_address must be a valid IPv4 or IPv6 address")
		}
		plan.Hostname = c.TargetIPAddress
		// Mirrors the Azure CLI's synthetic ARM ID for IP Connect, last checked at
		// https://github.com/Azure/azure-cli-extensions/blob/273739924ff4cd31a56ac789e40c44d0e2fdd649/src/bastion/azext_bastion/custom.py
		plan.TargetResourceID = fmt.Sprintf(
			"/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/bh-hostConnect/%s",
			bastionID.SubscriptionID,
			bastionID.ResourceGroupName,
			c.TargetIPAddress,
		)
	}
	if c.TargetPort < 1 || c.TargetPort > 65535 {
		return tunnelPlan{}, errors.New("target_port must be between 1 and 65535")
	}
	if c.LocalPort < 1 || c.LocalPort > 65535 {
		return tunnelPlan{}, errors.New("local_port must be between 1 and 65535")
	}
	if hasIP && c.TargetPort != 22 && c.TargetPort != 3389 {
		return tunnelPlan{}, errors.New("target_port must be 22 or 3389 when target_ip_address is set")
	}
	return plan, nil
}

func discoverBastion(ctx context.Context, client bastionHostGetter, id *arm.ResourceID) (bastionInfo, error) {
	response, err := client.Get(ctx, id.ResourceGroupName, id.Name, nil)
	if err != nil {
		return bastionInfo{}, fmt.Errorf("read Azure Bastion resource: %w", err)
	}
	info := bastionInfo{}
	if response.SKU != nil && response.SKU.Name != nil {
		info.SKU = string(*response.SKU.Name)
	}
	if response.Properties != nil {
		if response.Properties.DNSName != nil {
			info.DNSName = *response.Properties.DNSName
		}
		if response.Properties.EnableTunneling != nil {
			info.EnableTunneling = *response.Properties.EnableTunneling
		}
		if response.Properties.EnableIPConnect != nil {
			info.EnableIPConnect = *response.Properties.EnableIPConnect
		}
	}
	return info, nil
}

func validateBastion(info bastionInfo, usesIPConnect bool) error {
	if info.SKU != string(armnetwork.BastionHostSKUNameStandard) &&
		info.SKU != string(armnetwork.BastionHostSKUNamePremium) {
		return fmt.Errorf("unsupported Azure Bastion SKU %q: must be Standard or Premium", info.SKU)
	}
	if !info.EnableTunneling {
		return errors.New("native client tunneling is not enabled on the Azure Bastion host")
	}
	if usesIPConnect && !info.EnableIPConnect {
		return errors.New("IP Connect is not enabled on the Azure Bastion host")
	}
	if info.DNSName == "" {
		return errors.New("no DNS endpoint on the Azure Bastion resource")
	}
	return nil
}

func ForkRemoteTunnel(ctx context.Context, cfg TunnelConfig) (*exec.Cmd, error) {
	plan, err := cfg.resolve()
	if err != nil {
		return nil, err
	}
	logName := fmt.Sprintf("azure-bastion-tunnel-%s-%d.log", plan.Bastion.Name, plan.TargetPort)
	return libs.ForkTunnel(ctx, TunnelType, logName, cfg)
}

func StartRemoteTunnel(ctx context.Context, configJSON string, parentPID int) error {
	var cfg TunnelConfig
	if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil {
		return err
	}
	plan, err := cfg.resolve()
	if err != nil {
		return err
	}
	if err := libs.WatchProcess(parentPID); err != nil {
		return err
	}

	credential, err := azidentity.NewDefaultAzureCredential(&azidentity.DefaultAzureCredentialOptions{
		ClientOptions: azcore.ClientOptions{Cloud: cloud.AzurePublic},
	})
	if err != nil {
		return fmt.Errorf("initialize Azure credential chain: %w", err)
	}
	client, err := armnetwork.NewBastionHostsClient(plan.Bastion.SubscriptionID, credential, nil)
	if err != nil {
		return fmt.Errorf("initialize Azure Bastion ARM client: %w", err)
	}
	info, err := discoverBastion(ctx, client, plan.Bastion)
	if err != nil {
		return err
	}
	if err := validateBastion(info, plan.Hostname != ""); err != nil {
		return err
	}

	session, err := newSessionClient(info.DNSName, plan, credential)
	if err != nil {
		return err
	}
	defer func() {
		if err := session.close(); err != nil {
			log.Printf("Azure Bastion session cleanup failed: %v", err)
		}
	}()

	listener, err := net.Listen("tcp", net.JoinHostPort(plan.LocalHost, strconv.Itoa(plan.LocalPort)))
	if err != nil {
		return fmt.Errorf("listen on local address: %w", err)
	}
	defer listener.Close()

	runCtx, cancel := signal.NotifyContext(ctx, os.Interrupt)
	defer cancel()
	server := newServer(listener, session)
	defer server.Close()

	if err := libs.SignalReadyIfRequested(); err != nil {
		return err
	}
	log.Printf("Azure Bastion tunnel listening on %s", listener.Addr())
	return server.Serve(runCtx)
}
