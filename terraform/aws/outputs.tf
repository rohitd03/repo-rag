output "endpoint" {
  description = "Postgres endpoint, host:port."
  value       = aws_db_instance.postgres.endpoint
}

output "secret_arn" {
  description = "ARN of the Secrets Manager secret holding the credentials. The application reads the connection details from here rather than from a Terraform output."
  value       = aws_secretsmanager_secret.db.arn
}

output "security_group_id" {
  description = "Security group attached to the database."
  value       = aws_security_group.postgres.id
}

output "bootstrap_sql" {
  description = "Run once against the new database. RDS allow-lists pgvector but does not create the extension."
  value       = "CREATE EXTENSION IF NOT EXISTS vector;"
}
