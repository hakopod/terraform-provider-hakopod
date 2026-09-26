# One project with staging and production environments. The same module,
# modules/stack, deploys a network, a secret and two applications into each
# environment; only the sizing and image tag differ.
#
# The module receives the project name from hakopod_project.this, so Terraform
# creates the project (and its environments) first and destroys it last.

terraform {
  required_providers {
    hakopod = {
      source = "hakopod/hakopod"
    }
  }
}

provider "hakopod" {}

resource "hakopod_project" "this" {
  name = var.project
  # Environments can be added later but not removed.
  environments = keys(var.environments)
}

module "stack" {
  source   = "./modules/stack"
  for_each = var.environments

  project     = hakopod_project.this.name
  environment = each.key
  image_tag   = each.value.image_tag
  replicas    = each.value.replicas
  resources   = each.value.resources
}
