// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/Ezzahhh/terraform-provider-tunnel/internal/provider"
	"github.com/Ezzahhh/terraform-provider-tunnel/internal/ssm"
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
		Address: "registry.terraform.io/Ezzahhh/tunnel",
		Debug:   debug,
	}

	return providerserver.Serve(context.Background(), provider.New(version), opts)
}

func StartSSM() error {
	if len(os.Args) < 7 {
		return fmt.Errorf("missing required arguments")
	}

	cfg := ssm.TunnelConfig{
		SSMRegion:   os.Args[1],
		SSMInstance: os.Args[2],
		TargetHost:  os.Args[3],
		TargetPort:  os.Args[4],
		LocalPort:   os.Args[5],
	}

	return ssm.StartRemoteTunnel(context.Background(), cfg, os.Args[6])
}

func main() {
	var err error

	if os.Getenv(ssm.DEFAULT_SSM_ENV_NAME) != "" {
		err = StartSSM()
	} else {
		err = StartServer()
	}

	if err != nil {
		log.Fatal(err.Error())
	}
}
