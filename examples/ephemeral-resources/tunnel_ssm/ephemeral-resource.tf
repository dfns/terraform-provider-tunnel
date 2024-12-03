# The following example shows how to create a tunnel for an AWS RDS database.

ephemeral "tunnel_ssm" "rds" {
  target_host  = "https://my-db.us-east-1.rds.amazonaws.com"
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
