# One environment's stack: a virtual network with one segment, a generated
# secret, an internal "api" application reading the secret, and a public "web"
# application that reaches api over the segment. Names are per environment, so
# every instance of this module uses the same ones.

terraform {
  required_providers {
    hakopod = {
      source = "hakopod/hakopod"
    }
  }
}

locals {
  image = "nginxinc/nginx-unprivileged:${var.image_tag}"

  # Settings shared by both services. nginx-unprivileged listens on 8080 as
  # uid/gid 101.
  service = {
    image        = local.image
    port         = 8080
    healthcheck  = "/"
    run_as_user  = 101
    run_as_group = 101
    replicas     = var.replicas
    resources    = var.resources
    networks     = ["backend"]
  }

  # Local network joining the shared segment. The name reference orders both
  # applications after the network and its grant.
  networks = {
    backend = {
      internal        = true
      virtual_network = hakopod_virtual_network.this.name
      segment         = "backend"
    }
  }
}

resource "hakopod_virtual_network" "this" {
  project     = var.project
  environment = var.environment
  spec = jsonencode({
    schema_version = 1
    name           = "backend"
    segments = {
      backend = { applications = ["web", "api"] }
    }
  })
}

# Secrets belong to an application. "api" is a literal: referencing
# hakopod_application.api here would be a cycle, because api reads this secret.
resource "hakopod_secret" "api_token" {
  project     = var.project
  environment = var.environment
  application = "api"
  name        = "api-token"
  generate    = true
}

resource "hakopod_application" "api" {
  project     = var.project
  environment = var.environment
  # A destroyed application leaves a retained-data record even with no volume,
  # and a project holding retained data cannot be deleted. True so that
  # `terraform destroy` finishes; use false where data must survive.
  delete_data_on_destroy = true
  spec = jsonencode({
    schema_version = 1
    name           = "api"
    networks       = local.networks
    services = {
      api = merge(local.service, {
        secrets = {
          API_TOKEN = { ref = hakopod_secret.api_token.name }
        }
        # Changes when the secret is rotated, so the application redeploys and
        # picks up the new value.
        env = {
          SECRET_VERSION = hakopod_secret.api_token.value_version
        }
        network_access = {
          from              = []
          from_applications = ["web/web"]
        }
      })
    }
  })
}

resource "hakopod_application" "web" {
  project     = var.project
  environment = var.environment
  # A destroyed application leaves a retained-data record even with no volume,
  # and a project holding retained data cannot be deleted. True so that
  # `terraform destroy` finishes; use false where data must survive.
  delete_data_on_destroy = true
  spec = jsonencode({
    schema_version = 1
    name           = "web"
    networks       = local.networks
    services = {
      web = merge(local.service, {
        public = true
        # Referencing api's private address also orders web after api.
        env = {
          API_URL = "http://${hakopod_application.api.service_hostnames["api"]}:8080"
        }
      })
    }
  })
}
