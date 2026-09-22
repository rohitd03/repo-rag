terraform {
  required_version = ">= 1.5"

  required_providers {
    docker = {
      source = "kreuzwerker/docker"
      # Pinned to a minor version. Unpinned providers turn an unrelated
      # `terraform init` into an unplanned upgrade, which is how a working
      # config starts failing on someone else's machine.
      version = "~> 3.0"
    }
  }
}

provider "docker" {}
