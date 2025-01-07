# Terraform Provider: Tunnel

The Tunnel provider is used to manage local network tunnels. This enables users to
securely access and manage remote servers (databases, web servers, etc.) in private
networks without needing to open additional ports to the outside networks.

The provider is compatible with HashiCorp Cloud Platform (HCP)

## Available tunnel types

- [AWS Systems Manager (SSM)](https://docs.aws.amazon.com/systems-manager/latest/userguide/)

## Example Usage

### Terraform >= 1.10

> For optimal compatiblity with HashiCorp Cloud Platform, use [Ephemeral Resources](https://developer.hashicorp.com/terraform/language/resources/ephemeral)

```terraform
terraform {
  required_providers {
    tunnel = {
      source  = "Ezzahhh/tunnel"
      version = ">= 1.3.0"
    }
  }
}

ephemeral "tunnel_ssm" "eks" {
  target_host  = "https://eks-cluster.region.eks.amazonaws.com"
  target_port  = 443
  ssm_instance = "i-instanceid"
  ssm_region   = "us-east-1"
  ssm_profile  = "your-aws-profile"
}

provider "kubernetes" {
  host = "https://${ephemeral.tunnel_ssm.eks.local_host}:${ephemeral.tunnel_ssm.eks.local_port}"

  tls_server_name = "eks-cluster.region.eks.amazonaws.com"

  client_certificate     = file("~/.kube/client-cert.pem")
  client_key             = file("~/.kube/client-key.pem")
  cluster_ca_certificate = file("~/.kube/cluster-ca-cert.pem")
}
```

### Terraform >= 1.0

```terraform
data "tunnel_ssm" "eks" {
  target_host  = "https://eks-cluster.region.eks.amazonaws.com"
  target_port  = 443
  ssm_instance = "i-instanceid"
  ssm_region   = "us-east-1"
  ssm_profile  = "your-aws-profile"
}
```

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
Please see the Go documentation for the most up to date information about using Go modules.

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
