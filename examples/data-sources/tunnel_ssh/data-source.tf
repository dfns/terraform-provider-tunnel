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
