package ssm

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
)

// TestCreateSessionInput verifies the AWS port-forwarding request is assembled
// from the tunnel config: the document name is fixed and the three parameters
// map to the configured target port, local port and host.
func TestCreateSessionInput(t *testing.T) {
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
	if in.DocumentName == nil || *in.DocumentName != "AWS-StartPortForwardingSessionToRemoteHost" {
		t.Errorf("DocumentName = %v, want AWS-StartPortForwardingSessionToRemoteHost", in.DocumentName)
	}

	wantParams := map[string]string{
		"portNumber":      "5432",
		"localPortNumber": "12345",
		"host":            "db.internal",
	}
	for key, want := range wantParams {
		got, ok := in.Parameters[key]
		if !ok {
			t.Errorf("missing parameter %q", key)
			continue
		}
		if len(got) != 1 || got[0] != want {
			t.Errorf("parameter %q = %v, want [%q]", key, got, want)
		}
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
