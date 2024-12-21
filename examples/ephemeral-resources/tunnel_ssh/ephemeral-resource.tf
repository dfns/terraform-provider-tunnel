ephemeral "tunnel_ssh" "k8s" {
  target_host = "localhost"
  target_port = 6443
  ssh_host    = "k8s-master.example.com"
  ssh_user    = "ec2-user"
}

provider "kubernetes" {
  host = "https://${ephemeral.tunnel_ssh.k8s.local_host}:${ephemeral.tunnel_ssh.k8s.local_port}"

  client_certificate     = file("~/.kube/client-cert.pem")
  client_key             = file("~/.kube/client-key.pem")
  cluster_ca_certificate = file("~/.kube/cluster-ca-cert.pem")
}
