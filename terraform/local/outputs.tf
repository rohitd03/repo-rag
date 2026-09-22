output "database_url" {
  description = "Connection string for RAG_DATABASE_URL."
  value       = "postgres://${var.db_user}:${var.db_password}@localhost:${var.host_port}/${var.db_name}?sslmode=disable"

  # Contains the password, so Terraform masks it in console output. Read it
  # deliberately with `terraform output -raw database_url`.
  sensitive = true
}

output "container_name" {
  description = "Name of the running container, for docker exec."
  value       = docker_container.postgres.name
}

output "host_port" {
  description = "Host port Postgres is listening on."
  value       = var.host_port
}
