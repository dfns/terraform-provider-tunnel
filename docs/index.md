---
page_title: "Provider: Tunnel"
description: |-
  The Tunnel provider is used to manage local network tunnels.
---

# Tunnel Provider

The Tunnel provider manages local network tunnels, enabling secure access to remote servers (databases, web servers, etc.) within private networks without exposing additional ports to the public internet.

It facilitates port forwarding (similar to `kubectl port-forward` or `ssh -L`).

The provider is compatible with HashiCorp Cloud Platform (HCP)

## Available tunnel types

- [AWS Systems Manager (SSM)](#aws-systems-manager-ssm)
- [SSH Tunneling](#ssh-tunneling)
- [Kubernetes Port Forwarding](#kubernetes-port-forwarding)

## Example Usage

### Terraform >= 1.10

~> **Note:** For optimal compatiblity with HashiCorp Cloud Platform, use [Ephemeral Resources](https://developer.hashicorp.com/terraform/language/resources/ephemeral).

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

```terraform
data "tunnel_ssm" "rds" {
  target_host  = "rds-cluster.region.rds.amazonaws.com"
  target_port  = 443
  ssm_instance = "i-instanceid"
  ssm_region   = "us-east-1"
}

provider "postgresql" {
  host     = data.tunnel_ssm.rds.local_host
  port     = data.tunnel_ssm.rds.local_port
  database = "my-database"
  username = "my-user"
  password = "my-password"
}
```

### SSH Tunneling

Establishes a standard SSH tunnel via a bastion host to reach the target destination.
This provider uses a built-in SSH client and requires valid SSH credentials (key-based, password, etc.) to the bastion.

```terraform
data "tunnel_ssh" "k8s" {
  target_host = "localhost"
  target_port = 6443
  ssh_host    = "k8s-master.example.com"
  ssh_user    = "ec2-user"
}

provider "kubernetes" {
  host = format(
    "https://%s:%s",
    data.tunnel_ssh.k8s.local_host,
    data.tunnel_ssh.k8s.local_port,
  )

  client_certificate     = file("~/.kube/client-cert.pem")
  client_key             = file("~/.kube/client-key.pem")
  cluster_ca_certificate = file("~/.kube/cluster-ca-cert.pem")
}
```

### Kubernetes Port Forwarding

Establishes a port-forwarding session to a service or pod within a Kubernetes cluster directly via the Kubernetes API.
This provider interacts directly with the Kubernetes API, supporting standard kubeconfig authentication.

```terraform
data "tunnel_kubernetes" "postgres" {
  namespace    = "default"
  service_name = "postgres-service"
  target_port  = 5432

  kubernetes = {
    config_path    = "~/.kube/config"
    config_context = "minikube"
  }
}

provider "postgresql" {
  host            = data.tunnel_kubernetes.postgres.local_host
  port            = data.tunnel_kubernetes.postgres.local_port
  username        = "postgres"
  password        = "password"
  sslmode         = "disable"
  connect_timeout = 15
}

resource "postgresql_database" "my_db" {
  name = "my_database"
}
```