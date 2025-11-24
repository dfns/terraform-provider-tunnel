# Terraform Provider: Tunnel

[![GitHub Release](https://img.shields.io/github/v/release/dfns/terraform-provider-tunnel)](https://github.com/dfns/terraform-provider-tunnel/releases/latest)
[![Go Report Card](https://goreportcard.com/badge/github.com/dfns/terraform-provider-tunnel)](https://goreportcard.com/report/github.com/dfns/terraform-provider-tunnel)
[![Terraform Downloads](https://img.shields.io/terraform/provider/dt/5739?logo=terraform&logoColor=white&color=%23844FBA)](https://registry.terraform.io/providers/dfns/tunnel)
[![GitHub Downloads](https://img.shields.io/github/downloads/dfns/terraform-provider-tunnel/total?logo=github)](https://github.com/dfns/terraform-provider-tunnel/releases)

The Tunnel provider manages local network tunnels, enabling secure access to remote servers (databases, web servers, etc.) within private networks without exposing additional ports to the public internet.

It facilitates port forwarding (similar to `kubectl port-forward` or `ssh -L`).

The provider is compatible with HashiCorp Cloud Platform (HCP)

## Available tunnel types

- [AWS Systems Manager (SSM)](#aws-systems-manager-ssm)
- [SSH Tunneling](#ssh-tunneling)
- [Kubernetes Port Forwarding](#kubernetes-port-forwarding)

## Example Usage

### Terraform >= 1.10

> For optimal compatibility with HashiCorp Cloud Platform, use [Ephemeral Resources](https://developer.hashicorp.com/terraform/language/resources/ephemeral)

```terraform
terraform {
  required_providers {
    tunnel = {
      source  = "dfns/tunnel"
      version = ">= 1.3.0"
    }
  }
}

ephemeral "tunnel_ssm" "eks" {
  target_host  = "eks-cluster.region.eks.amazonaws.com"
  target_port  = 443
  ssm_instance = "i-instanceid"
  ssm_region   = "us-east-1"
}

ephemeral "tunnel_ssh" "rds" {
  target_host = "rds-cluster.region.rds.amazonaws.com"
  target_port = 5432
  ssh_host    = "bastion.example.com"
  ssh_user    = "ec2-user"
}

ephemeral "tunnel_kubernetes" "service" {
  service_name = "my-service"
  namespace    = "default"
  target_port  = 80
  kubernetes = {
    config_path    = "~/.kube/config"
    config_context = "my-context"
  }
}

provider "kubernetes" {
  host = format(
    "https://%s:%s",
    ephemeral.tunnel_ssm.eks.local_host,
    ephemeral.tunnel_ssm.eks.local_port,
  )

  tls_server_name = "eks-cluster.region.eks.amazonaws.com"

  client_certificate     = file("~/.kube/client-cert.pem")
  client_key             = file("~/.kube/client-key.pem")
  cluster_ca_certificate = file("~/.kube/cluster-ca-cert.pem")
}
```

### Terraform >= 1.0

```terraform
data "tunnel_ssm" "eks" {
  target_host  = "eks-cluster.region.eks.amazonaws.com"
  target_port  = 443
  ssm_instance = "i-instanceid"
  ssm_region   = "us-east-1"
}

data "tunnel_ssh" "rds" {
  target_host = "rds-cluster.region.rds.amazonaws.com"
  target_port = 5432
  ssh_host    = "bastion.example.com"
  ssh_user    = "ec2-user"
}

data "tunnel_kubernetes" "service" {
  service_name = "my-service"
  namespace    = "default"
  target_port  = 80
  kubernetes = {
    config_path    = "~/.kube/config"
    config_context = "my-context"
  }
}
```

## Tunnel Details

### AWS Systems Manager (SSM)

Establishes a secure tunnel to a remote host using [AWS Systems Manager Session Manager](https://docs.aws.amazon.com/systems-manager/latest/userguide/session-manager.html).
This method requires the SSM Agent to be installed and correctly configured with IAM permissions on the target instance.

### SSH Tunneling

Establishes a standard SSH tunnel via a bastion host to reach the target destination.
This provider uses a built-in SSH client and requires valid SSH credentials (key-based, password, etc.) to the bastion.

### Kubernetes Port Forwarding

Establishes a port-forwarding session to a service or pod within a Kubernetes cluster directly via the Kubernetes API.
This provider interacts directly with the Kubernetes API, supporting standard kubeconfig authentication.

## Requirements

- [Terraform](https://developer.hashicorp.com/terraform/downloads) >= 1.0
- [Go](https://golang.org/doc/install) >= 1.22

## Building The Provider

1. Clone the repository
1. Enter the repository directory
1. Build the provider using the Go `install` command:

```shell
go install
```

## Adding Dependencies

This provider uses [Go modules](https://github.com/golang/go/wiki/Modules).
Please see the Go documentation for the most up-to-date information about using Go modules.

To add a new dependency `github.com/author/dependency` to your Terraform provider:

```shell
go get github.com/author/dependency
go mod tidy
```

Then commit the changes to `go.mod` and `go.sum`.

## Developing the Provider

If you wish to work on the provider, you'll first need [Go](http://www.golang.org) installed on your machine (see [Requirements](#requirements) above).

To compile the provider, run `go install`. This will build the provider and put the provider binary in the `$GOPATH/bin` directory.

To generate or update documentation, run `make generate`.

In order to run the full suite of Acceptance tests, run `make testacc`.

_Note:_ Acceptance tests create real resources, and often cost money to run.

```shell
make testacc
```
