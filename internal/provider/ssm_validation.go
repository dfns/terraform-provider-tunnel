package provider

import (
	"errors"
	"strconv"
	"strings"

	"github.com/dfns/terraform-provider-tunnel/internal/ssm"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func validateSSMTunnel(targetHost types.String, targetPort types.Int64, ssmDocument types.String) []error {
	doc := ssmDocument.ValueString()
	if doc != "" && doc != ssm.DefaultSSMDocument {
		return nil
	}

	var errs []error
	if targetHost.IsNull() || targetHost.ValueString() == "" {
		errs = append(errs, errors.New("`target_host` is required when `ssm_document` is unset or set to `AWS-StartPortForwardingSessionToRemoteHost`"))
	}
	if targetPort.IsNull() || targetPort.ValueInt64() == 0 {
		errs = append(errs, errors.New("`target_port` is required when `ssm_document` is unset or set to `AWS-StartPortForwardingSessionToRemoteHost`"))
	}
	return errs
}

func appendSSMTunnelValidationDiagnostics(diags *diag.Diagnostics, targetHost types.String, targetPort types.Int64, ssmDocument types.String) {
	for _, err := range validateSSMTunnel(targetHost, targetPort, ssmDocument) {
		switch {
		case strings.Contains(err.Error(), "`target_host`"):
			diags.AddError(
				"target_host is required for the default SSM port-forwarding document",
				err.Error(),
			)
		case strings.Contains(err.Error(), "`target_port`"):
			diags.AddError(
				"target_port is required for the default SSM port-forwarding document",
				err.Error(),
			)
		default:
			diags.AddError("Invalid SSM tunnel configuration", err.Error())
		}
	}
}

func ssmTargetPortString(port types.Int64) string {
	if port.IsNull() || port.ValueInt64() == 0 {
		return ""
	}
	return strconv.Itoa(int(port.ValueInt64()))
}
