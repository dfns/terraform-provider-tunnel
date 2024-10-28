---
page_title: "Provider: Tunnel"
description: |-
  The Tunnel provider is used to manage local network tunnels.
---

# Tunnel Provider

The Tunnel provider is used to manage local network tunnels. This enables users to
securely access and manage remote servers (databases, web servers, etc.) in private
networks without needing to open additional ports to the outside networks.

The provider is compatible with HashiCorp Cloud Platform (HCP)

## Available tunnel types

- [AWS Systems Manager (SSM)](https://docs.aws.amazon.com/systems-manager/latest/userguide/)

## Example Usage

```terraform
terraform {
  required_providers {
    tunnel = {
      source  = "dfns/tunnel"
      version = ">= 1.0.0"
    }
  }
}

data "tunnel_ssm" "eks" {
  target_host  = "https://eks-cluster.region.eks.amazonaws.com"
  target_port  = 443
  ssm_instance = "i-instanceid"
  ssm_region   = "us-east-1"
}

provider "kubernetes" {
  host = "https://${data.tunnel_ssm.eks.local_host}:${data.tunnel_ssm.eks.local_port}"

  tls_server_name = "eks-cluster.region.eks.amazonaws.com"

  client_certificate     = file("~/.kube/client-cert.pem")
  client_key             = file("~/.kube/client-key.pem")
  cluster_ca_certificate = file("~/.kube/cluster-ca-cert.pem")
}
```