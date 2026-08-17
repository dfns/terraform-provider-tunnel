package provider

import (
	"os/user"

	"github.com/dfns/terraform-provider-tunnel/internal/libs"
	"github.com/dfns/terraform-provider-tunnel/internal/ssh"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type SSHModel struct {
	LocalHost        types.String `tfsdk:"local_host"`
	LocalPort        types.Int64  `tfsdk:"local_port"`
	SSHHost          types.String `tfsdk:"ssh_host"`
	SSHKey           types.String `tfsdk:"ssh_key"`
	SSHKeyPassphrase types.String `tfsdk:"ssh_key_passphrase"`
	SSHPassword      types.String `tfsdk:"ssh_password"`
	SSHPort          types.Int64  `tfsdk:"ssh_port"`
	SSHUser          types.String `tfsdk:"ssh_user"`
	TargetHost       types.String `tfsdk:"target_host"`
	TargetPort       types.Int64  `tfsdk:"target_port"`
	TargetSocket     types.String `tfsdk:"target_socket"`
}

func validateSSHTarget(targetHost, targetSocket types.String, targetPort types.Int64) diag.Diagnostics {
	var diags diag.Diagnostics

	hasPort := !targetPort.IsNull()
	hasSocket := !targetSocket.IsNull() && targetSocket.ValueString() != ""
	switch {
	case hasPort && hasSocket:
		diags.AddError(
			"Conflicting SSH tunnel target",
			"`target_port` and `target_socket` are mutually exclusive",
		)
	case !hasPort && !hasSocket:
		diags.AddError(
			"Missing SSH tunnel target",
			"one of `target_port` or `target_socket` must be set",
		)
	case hasPort && (targetHost.IsNull() || targetHost.ValueString() == ""):
		diags.AddError(
			"Missing target_host",
			"`target_host` is required when `target_port` is set",
		)
	}

	return diags
}

func sshConfig(data *SSHModel) (ssh.TunnelConfig, diag.Diagnostics) {
	diags := validateSSHTarget(data.TargetHost, data.TargetSocket, data.TargetPort)
	if diags.HasError() {
		return ssh.TunnelConfig{}, diags
	}

	localPort := int(data.LocalPort.ValueInt64())
	if localPort == 0 {
		var err error
		localPort, err = libs.GetFreePort()
		if err != nil {
			diags.AddError("Failed to find open local port", err.Error())
			return ssh.TunnelConfig{}, diags
		}
		data.LocalPort = types.Int64Value(int64(localPort))
	}

	if data.LocalHost.IsNull() || data.LocalHost.ValueString() == "" {
		data.LocalHost = types.StringValue("localhost")
	}
	if data.SSHUser.IsNull() {
		currentUser, err := user.Current()
		if err != nil {
			diags.AddError("Failed to determine the current user", err.Error())
			return ssh.TunnelConfig{}, diags
		}
		data.SSHUser = types.StringValue(currentUser.Username)
	}
	if data.SSHPort.IsNull() {
		data.SSHPort = types.Int64Value(22)
	}

	return ssh.TunnelConfig{
		LocalHost:        data.LocalHost.ValueString(),
		LocalPort:        localPort,
		SSHHost:          data.SSHHost.ValueString(),
		SSHKey:           data.SSHKey.ValueString(),
		SSHKeyPassphrase: data.SSHKeyPassphrase.ValueString(),
		SSHPassword:      data.SSHPassword.ValueString(),
		SSHPort:          int(data.SSHPort.ValueInt64()),
		SSHUser:          data.SSHUser.ValueString(),
		TargetHost:       data.TargetHost.ValueString(),
		TargetPort:       int(data.TargetPort.ValueInt64()),
		TargetSocket:     data.TargetSocket.ValueString(),
	}, diags
}
