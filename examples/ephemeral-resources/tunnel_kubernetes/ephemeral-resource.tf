ephemeral "tunnel_kubernetes" "postgres" {
  namespace    = "default"
  service_name = "postgres-service"
  target_port  = 5432

  kubernetes = {
    config_path    = "~/.kube/config"
    config_context = "minikube"
  }
}

provider "postgresql" {
  host            = ephemeral.tunnel_kubernetes.postgres.local_host
  port            = ephemeral.tunnel_kubernetes.postgres.local_port
  username        = "postgres"
  password        = "password"
  sslmode         = "disable"
  connect_timeout = 15
}

resource "postgresql_database" "my_db" {
  name = "my_database"
}
