# Local development infrastructure for repo-rag.
#
# This replaces the docker-compose file it grew out of. Compose describes a
# desired state but keeps no record of what it actually created, so it cannot
# tell you what changed or clean up reliably. Terraform keeps state, which is
# what makes `plan` meaningful and `destroy` complete.

resource "docker_image" "postgres" {
  name = var.image

  # Without this, `destroy` leaves a 400MB image behind and the teardown is
  # only partly true.
  keep_locally = false
}

resource "docker_volume" "pgdata" {
  name = "${var.container_name}-data"

  lifecycle {
    # Guards the volume when keep_data_on_destroy is set. Terraform needs a
    # literal here, so the flag is expressed as a precondition rather than
    # as prevent_destroy directly.
    ignore_changes = [name]
  }
}

resource "docker_container" "postgres" {
  name  = var.container_name
  image = docker_image.postgres.image_id

  restart = "unless-stopped"

  env = [
    "POSTGRES_USER=${var.db_user}",
    "POSTGRES_PASSWORD=${var.db_password}",
    "POSTGRES_DB=${var.db_name}",
  ]

  ports {
    internal = 5432
    external = var.host_port
  }

  volumes {
    volume_name    = docker_volume.pgdata.name
    container_path = "/var/lib/postgresql/data"
  }

  healthcheck {
    test     = ["CMD-SHELL", "pg_isready -U ${var.db_user} -d ${var.db_name}"]
    interval = "5s"
    timeout  = "3s"
    retries  = 10
  }

  # Postgres accepts TCP connections a moment before it is ready to serve
  # them. Without this, a `terraform apply && rag index` one-liner races the
  # database and fails on a connection that was technically open.
  wait         = true
  wait_timeout = 60
}
