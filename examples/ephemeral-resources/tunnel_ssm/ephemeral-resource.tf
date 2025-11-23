ephemeral "tunnel_ssm" "rds" {
  target_host  = "rds-cluster.region.rds.amazonaws.com"
  target_port  = 443
  ssm_instance = "i-instanceid"
  ssm_region   = "us-east-1"
}

provider "postgresql" {
  host     = ephemeral.tunnel_ssm.rds.local_host
  port     = ephemeral.tunnel_ssm.rds.local_port
  database = "my-database"
  username = "my-user"
  password = "my-password"
}
