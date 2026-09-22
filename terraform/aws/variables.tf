variable "region" {
  description = "AWS region."
  type        = string
  default     = "eu-central-1"
}

variable "name_prefix" {
  description = "Prefix for all resource names."
  type        = string
  default     = "repo-rag"
}

variable "vpc_id" {
  description = "VPC to place the database in."
  type        = string
}

variable "private_subnet_ids" {
  description = "Private subnets for the DB subnet group. At least two AZs."
  type        = list(string)

  validation {
    condition     = length(var.private_subnet_ids) >= 2
    error_message = "RDS requires subnets in at least two availability zones."
  }
}

variable "app_security_group_id" {
  description = "Security group of the application allowed to reach Postgres."
  type        = string
}

variable "engine_version" {
  description = "Postgres version. Must be 15 or later for pgvector on RDS."
  type        = string
  default     = "16.4"
}

variable "instance_class" {
  description = "RDS instance class."
  type        = string
  default     = "db.t4g.micro"
}

variable "allocated_storage" {
  description = "Initial storage in GB."
  type        = number
  default     = 20
}

variable "max_allocated_storage" {
  description = "Autoscaling ceiling in GB."
  type        = number
  default     = 100
}

variable "db_name" {
  description = "Initial database name."
  type        = string
  default     = "rag"
}

variable "db_username" {
  description = "Master username. The password is generated, never supplied."
  type        = string
  default     = "rag"
}

variable "multi_az" {
  description = "Multi-AZ. Roughly doubles cost; a rebuildable index rarely justifies it."
  type        = bool
  default     = false
}

variable "backup_retention_days" {
  description = "Automated backup retention. The index is derived data and can be rebuilt, so this is deliberately short."
  type        = number
  default     = 7
}

variable "deletion_protection" {
  description = "Block accidental deletion. Should be true in production."
  type        = bool
  default     = true
}

variable "secret_recovery_days" {
  description = "Secrets Manager recovery window. 0 deletes immediately, which is only sensible in a scratch environment."
  type        = number
  default     = 7
}

variable "tags" {
  description = "Tags applied to taggable resources."
  type        = map(string)
  default = {
    Project   = "repo-rag"
    ManagedBy = "terraform"
  }
}
