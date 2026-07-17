package ssm

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
)

// TestCreateSessionInput verifies the AWS port-forwarding request is assembled
// from the tunnel config: the document name defaults or follows SSMDocument,
// localPort is always set, and host/port are included only when configured.
func TestCreateSessionInput(t *testing.T) {
	t.Run("default document when SSMDocument is empty", func(t *testing.T) {
		cfg := TunnelConfig{
			LocalPort:   "12345",
			SSMInstance: "i-0abc123",
			TargetHost:  "db.internal",
			TargetPort:  "5432",
		}

		in := CreateSessionInput(cfg)

		if in.Target == nil || *in.Target != cfg.SSMInstance {
			t.Errorf("Target = %v, want %q", in.Target, cfg.SSMInstance)
		}
		if in.DocumentName == nil || *in.DocumentName != DefaultSSMDocument {
			t.Errorf("DocumentName = %v, want %q", in.DocumentName, DefaultSSMDocument)
		}

		assertPortForwardParams(t, in.Parameters, "5432", "db.internal")
	})

	t.Run("custom SSMDocument", func(t *testing.T) {
		const customDoc = "My-Custom-PortForwardDoc"
		cfg := TunnelConfig{
			LocalPort:   "12345",
			SSMInstance: "i-0abc123",
			SSMDocument: customDoc,
			TargetHost:  "db.internal",
			TargetPort:  "5432",
		}

		in := CreateSessionInput(cfg)
		if in.DocumentName == nil || *in.DocumentName != customDoc {
			t.Errorf("DocumentName = %v, want %q", in.DocumentName, customDoc)
		}
		assertPortForwardParams(t, in.Parameters, "5432", "db.internal")
	})

	t.Run("omits host when TargetHost is empty", func(t *testing.T) {
		cfg := TunnelConfig{
			LocalPort:   "12345",
			SSMInstance: "i-0abc123",
			SSMDocument: "My-Custom-PortForwardDoc",
			TargetPort:  "5432",
		}

		in := CreateSessionInput(cfg)
		assertPortForwardParams(t, in.Parameters, "5432", "")
	})

	t.Run("omits port when TargetPort is empty", func(t *testing.T) {
		cfg := TunnelConfig{
			LocalPort:   "12345",
			SSMInstance: "i-0abc123",
			SSMDocument: "My-Custom-PortForwardDoc",
			TargetHost:  "db.internal",
		}

		in := CreateSessionInput(cfg)
		assertPortForwardParams(t, in.Parameters, "", "db.internal")
	})

	t.Run("omits host and port when both are empty", func(t *testing.T) {
		cfg := TunnelConfig{
			LocalPort:   "12345",
			SSMInstance: "i-0abc123",
			SSMDocument: "My-Custom-PortForwardDoc",
		}

		in := CreateSessionInput(cfg)
		assertPortForwardParams(t, in.Parameters, "", "")
	})
}

func assertPortForwardParams(t *testing.T, params map[string][]string, wantPort, wantHost string) {
	t.Helper()

	gotLocalPort, ok := params["localPortNumber"]
	if !ok {
		t.Errorf("missing parameter %q", "localPortNumber")
	} else if gotLocalPort[0] != "12345" {
		t.Errorf("parameter %q = %v, want [%q]", "localPortNumber", gotLocalPort, "12345")
	}

	gotPort, hasPort := params["portNumber"]
	switch {
	case wantPort == "" && hasPort:
		t.Errorf("portNumber parameter = %v, want omitted", gotPort)
	case wantPort != "" && !hasPort:
		t.Errorf("missing parameter %q", "portNumber")
	case wantPort != "" && gotPort[0] != wantPort:
		t.Errorf("parameter %q = %v, want [%q]", "portNumber", gotPort, wantPort)
	}

	gotHost, hasHost := params["host"]
	switch {
	case wantHost == "" && hasHost:
		t.Errorf("host parameter = %v, want omitted", gotHost)
	case wantHost != "" && !hasHost:
		t.Errorf("missing parameter %q", "host")
	case wantHost != "" && gotHost[0] != wantHost:
		t.Errorf("parameter %q = %v, want [%q]", "host", gotHost, wantHost)
	}
}

// TestGetSDKConfigProfileAndRole checks that the profile and role ARN are read
// out of the resolved SDK config's shared-config source, and that unrelated
// sources are ignored rather than panicking the type assertion.
func TestGetSDKConfigProfileAndRole(t *testing.T) {
	t.Run("reads from shared config source", func(t *testing.T) {
		const wantProfile = "myprofile"
		const wantRole = "arn:aws:iam::123456789012:role/demo"

		cfg := aws.Config{
			ConfigSources: []interface{}{
				config.SharedConfig{Profile: wantProfile, RoleARN: wantRole},
			},
		}

		if got := GetSDKConfigProfile(cfg); got != wantProfile {
			t.Errorf("GetSDKConfigProfile = %q, want %q", got, wantProfile)
		}
		if got := GetSDKConfigRole(cfg); got != wantRole {
			t.Errorf("GetSDKConfigRole = %q, want %q", got, wantRole)
		}
	})

	t.Run("returns empty when no shared config is present", func(t *testing.T) {
		cfg := aws.Config{ConfigSources: []interface{}{"not-a-shared-config"}}

		if got := GetSDKConfigProfile(cfg); got != "" {
			t.Errorf("GetSDKConfigProfile = %q, want empty", got)
		}
		if got := GetSDKConfigRole(cfg); got != "" {
			t.Errorf("GetSDKConfigRole = %q, want empty", got)
		}
	})
}
