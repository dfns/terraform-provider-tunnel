ephemeral "tunnel_azure_bastion" "postgres" {
  bastion_host_id = "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/network/providers/Microsoft.Network/bastionHosts/main"

  target_resource_id = "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/application/providers/Microsoft.Compute/virtualMachines/postgres"
  target_port        = 5432
}

provider "postgresql" {
  host     = ephemeral.tunnel_azure_bastion.postgres.local_host
  port     = ephemeral.tunnel_azure_bastion.postgres.local_port
  database = "my-database"
  username = "my-user"
  password = "my-password"
}
