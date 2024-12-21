// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"os"

	"github.com/dfns/terraform-provider-tunnel/internal/libs"
	"github.com/dfns/terraform-provider-tunnel/internal/provider"
	"github.com/dfns/terraform-provider-tunnel/internal/ssh"
	"github.com/dfns/terraform-provider-tunnel/internal/ssm"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
)

var (
	// these will be set by the goreleaser configuration
	// to appropriate values for the compiled binary.
	version string = "dev"

	// goreleaser can pass other information to the main package, such as the specific commit
	// https://goreleaser.com/cookbooks/using-main.version/
)

func StartServer() error {
	var debug bool

	flag.BoolVar(&debug, "debug", false, "set to true to run the provider with support for debuggers like delve")
	flag.Parse()

	opts := providerserver.ServeOpts{
		Address: "registry.terraform.io/dfns/tunnel",
		Debug:   debug,
	}

	return providerserver.Serve(context.Background(), provider.New(version), opts)
}

func StartTunnel(tun string) error {
	cfgJson := os.Getenv(libs.TunnelConfEnv)
	if cfgJson == "" {
		return errors.New("missing tunnel configuration")
	}
	if err := os.Unsetenv(libs.TunnelConfEnv); err != nil {
		return err
	}

	switch tun {
	case ssh.TunnelType:
		return ssh.StartRemoteTunnel(context.Background(), cfgJson, os.Args[1])
	case ssm.TunnelType:
		return ssm.StartRemoteTunnel(context.Background(), cfgJson, os.Args[1])
	default:
		return errors.New("unknown tunnel type")
	}
}

func main() {
	var err error

	if tun := os.Getenv(libs.TunnelTypeEnv); tun != "" {
		err = StartTunnel(tun)
	} else {
		err = StartServer()
	}

	if err != nil {
		log.Fatal(err.Error())
	}
}
