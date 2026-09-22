variable "container_name" {
  description = "Name of the Postgres container."
  type        = string
  default     = "rag-postgres"
}

variable "image" {
  description = "Postgres image. Must include the pgvector extension - a plain postgres image fails at CREATE EXTENSION vector."
  type        = string
  default     = "pgvector/pgvector:pg16"
}

variable "host_port" {
  description = "Host port for Postgres. Deliberately not 5432, so this can run alongside the cdc-pipeline database."
  type        = number
  default     = 5433

  validation {
    condition     = var.host_port > 1024 && var.host_port < 65536
    error_message = "host_port must be an unprivileged port between 1025 and 65535."
  }
}

variable "db_name" {
  description = "Database name."
  type        = string
  default     = "rag"
}

variable "db_user" {
  description = "Database user."
  type        = string
  default     = "rag"
}

variable "db_password" {
  description = "Database password. Local development only - see terraform/aws for how this is handled with a real secret store."
  type        = string
  default     = "rag"
  sensitive   = true
}

variable "keep_data_on_destroy" {
  description = "Keep the Docker volume when destroying. Re-indexing takes about 6 seconds, so the default is to remove it and keep destroy genuinely clean."
  type        = bool
  default     = false
}
