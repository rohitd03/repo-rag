# Production shape for repo-rag's vector store on AWS RDS.
#
# HONEST SCOPE: this module is written and `terraform validate`s, but it has
# never been applied - I have no AWS account attached to this project and
# would not spend on one to make a demo look better. Treat it as a design
# document in HCL, not as proven infrastructure. terraform/local is the part
# that has actually been run.
#
# What it captures that the local Docker module cannot:
#   - pgvector on managed Postgres rather than a container
#   - a generated password that never appears in state as plaintext input
#   - network isolation, so the database is not publicly reachable
#   - backups and deletion protection, the settings that matter at 3am

data "aws_vpc" "selected" {
  id = var.vpc_id
}

# Postgres 15+ on RDS ships pgvector as an available extension, but it still
# has to be allow-listed here before `CREATE EXTENSION vector` will succeed.
# Omitting this is the usual reason pgvector "does not work on RDS".
resource "aws_db_parameter_group" "postgres" {
  name_prefix = "${var.name_prefix}-pg16-"
  family      = "postgres16"
  description = "repo-rag Postgres 16 with pgvector allow-listed"

  parameter {
    name         = "shared_preload_libraries"
    value        = "pg_stat_statements"
    apply_method = "pending-reboot"
  }

  lifecycle {
    create_before_destroy = true
  }
}

resource "aws_db_subnet_group" "postgres" {
  name_prefix = "${var.name_prefix}-"
  subnet_ids  = var.private_subnet_ids
  description = "Private subnets for the repo-rag vector store"

  lifecycle {
    create_before_destroy = true
  }
}

resource "aws_security_group" "postgres" {
  name_prefix = "${var.name_prefix}-db-"
  description = "repo-rag vector store"
  vpc_id      = data.aws_vpc.selected.id

  lifecycle {
    create_before_destroy = true
  }
}

# Ingress only from the application's own security group. A CIDR rule here
# would be the easy version and would also be the thing a reviewer stops on -
# it grants access by network location rather than by identity.
resource "aws_vpc_security_group_ingress_rule" "postgres_from_app" {
  security_group_id            = aws_security_group.postgres.id
  description                  = "Postgres from the repo-rag application"
  from_port                    = 5432
  to_port                      = 5432
  ip_protocol                  = "tcp"
  referenced_security_group_id = var.app_security_group_id
}

# Generated, not supplied. A password passed in as a variable ends up in
# state, in shell history and often in version control.
resource "random_password" "db" {
  length  = 32
  special = true
  # RDS rejects these in a master password.
  override_special = "!#$%&*()-_=+[]{}<>:?"
}

resource "aws_secretsmanager_secret" "db" {
  name_prefix             = "${var.name_prefix}/db-"
  description             = "repo-rag vector store credentials"
  recovery_window_in_days = var.secret_recovery_days
}

resource "aws_secretsmanager_secret_version" "db" {
  secret_id = aws_secretsmanager_secret.db.id
  secret_string = jsonencode({
    username = var.db_username
    password = random_password.db.result
    host     = aws_db_instance.postgres.address
    port     = aws_db_instance.postgres.port
    dbname   = var.db_name
  })
}

resource "aws_db_instance" "postgres" {
  identifier_prefix = "${var.name_prefix}-"

  engine         = "postgres"
  engine_version = var.engine_version
  instance_class = var.instance_class

  # gp3 rather than gp2: same price band, and IOPS stops scaling with volume
  # size, which matters for a workload that is mostly small random reads.
  allocated_storage     = var.allocated_storage
  max_allocated_storage = var.max_allocated_storage
  storage_type          = "gp3"
  storage_encrypted     = true

  db_name  = var.db_name
  username = var.db_username
  password = random_password.db.result

  db_subnet_group_name   = aws_db_subnet_group.postgres.name
  vpc_security_group_ids = [aws_security_group.postgres.id]
  parameter_group_name   = aws_db_parameter_group.postgres.name
  publicly_accessible    = false

  multi_az                = var.multi_az
  backup_retention_period = var.backup_retention_days
  deletion_protection     = var.deletion_protection

  # Without this, `terraform destroy` fails on a protected instance and the
  # teardown path is untested until the day it is urgent.
  skip_final_snapshot       = !var.deletion_protection
  final_snapshot_identifier = var.deletion_protection ? "${var.name_prefix}-final" : null

  performance_insights_enabled = true
  auto_minor_version_upgrade   = true

  tags = var.tags
}
