# One project, one virtual network, and two applications that share the
# network's "data" segment.
#
# Ordering comes from references, not depends_on: applications and the network
# name the project through hakopod_project.commerce, and each application names
# the network through hakopod_virtual_network.commerce.name. Terraform creates
# project -> network -> applications and destroys them in reverse.
#
# catalog/.env must exist; its contents are passed through env_files.
# The database references a secret named postgres-password. If it does not
# exist, the database apply fails and names it; set it in the dashboard or with
# the CLI, then apply again.

terraform {
  required_providers {
    hakopod = {
      source = "hakopod/hakopod"
    }
  }
}

provider "hakopod" {}

resource "hakopod_project" "commerce" {
  name         = "commerce"
  environments = ["production"]
}

resource "hakopod_virtual_network" "commerce" {
  project     = hakopod_project.commerce.name
  environment = hakopod_project.commerce.environments[0]
  config      = file("${path.module}/network.toml")
}

# Application defined in HCL. Its services are part of the application and
# deploy together as one revision.
resource "hakopod_application" "database" {
  project     = hakopod_project.commerce.name
  environment = hakopod_project.commerce.environments[0]
  # A destroyed application leaves a retained-data record even with no volume,
  # and a project holding retained data cannot be deleted. True so that
  # `terraform destroy` finishes; use false where data must survive.
  delete_data_on_destroy = true
  spec = jsonencode({
    schema_version = 1
    name           = "database"
    networks = {
      data = {
        internal        = true
        virtual_network = hakopod_virtual_network.commerce.name
        segment         = "data"
      }
    }
    services = {
      postgres = {
        image    = "postgres:17"
        networks = ["data"]
        secrets = {
          POSTGRES_PASSWORD = { ref = "postgres-password" }
        }
        ports = [{
          name        = "sql"
          port        = 5432
          target_port = 5432
          protocol    = "TCP"
        }]
        network_access = {
          from              = []
          from_applications = ["catalog/api"]
        }
      }
    }
  })
}

# Application defined in TOML. The template receives the network name, which
# orders this application after the network.
resource "hakopod_application" "catalog" {
  project     = hakopod_project.commerce.name
  environment = hakopod_project.commerce.environments[0]
  # A destroyed application leaves a retained-data record even with no volume,
  # and a project holding retained data cannot be deleted. True so that
  # `terraform destroy` finishes; use false where data must survive.
  delete_data_on_destroy = true
  config = templatefile("${path.module}/catalog/hakopod.toml.tftpl", {
    network = hakopod_virtual_network.commerce.name
  })

  # The key is the filename the TOML uses; the value is its contents.
  env_files = {
    ".env" = file("${path.module}/catalog/catalog.env")
  }
}

output "catalog_status" {
  value = hakopod_application.catalog.status
}
