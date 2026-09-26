# Five services, each its own Hakopod application, generated from one map.
#
# local.services is the only thing to edit. Adding an entry adds an
# application, grants it the "mesh" segment, and — through `calls` — opens
# exactly the ingress it needs on the services it calls. Every service has a
# `network_access` block, so a peer that is not listed as a caller is denied.
#
# Ordering comes from references: the network names the project, and every
# application names the network through hakopod_virtual_network.mesh.name.
# Terraform creates project -> network -> applications and destroys them in
# reverse. No depends_on.

terraform {
  required_providers {
    hakopod = {
      source = "hakopod/hakopod"
    }
  }
}

provider "hakopod" {}

locals {
  # traefik/whoami is a static binary that runs as any non-root UID (Hakopod's
  # default is 10001) and answers HTTP on the port given in args. Replace the
  # image with your own; keep port in step with what the image listens on.
  services = {
    gateway = {
      image  = "traefik/whoami:v1.11"
      port   = 8080
      public = true
      calls  = ["orders", "users"]
    }
    orders = {
      image  = "traefik/whoami:v1.11"
      port   = 8080
      public = false
      calls  = ["payments", "users"]
    }
    users = {
      image  = "traefik/whoami:v1.11"
      port   = 8080
      public = false
      calls  = []
    }
    payments = {
      image  = "traefik/whoami:v1.11"
      port   = 8080
      public = false
      calls  = ["notifications"]
    }
    notifications = {
      image  = "traefik/whoami:v1.11"
      port   = 8080
      public = false
      calls  = []
    }
  }

  # Each application runs one service with this name, so a peer is
  # "<application>/api".
  service_name = "api"
  segment      = "mesh"

  # Reverse of `calls`: who may call each service.
  callers = {
    for name, _ in local.services : name => sort([
      for caller, s in local.services : "${caller}/${local.service_name}" if contains(s.calls, name)
    ])
  }

  # Application specs without the network membership, which needs the
  # network's name from the resource (see hakopod_application.service).
  applications = {
    for name, s in local.services : name => {
      schema_version = 1
      name           = name
      services = {
        (local.service_name) = {
          image    = s.image
          args     = ["--port", tostring(s.port)]
          port     = s.port
          public   = s.public
          size     = "small"
          networks = [local.segment]
          network_access = {
            from              = []
            from_applications = local.callers[name]
          }
        }
      }
    }
  }
}

resource "hakopod_project" "this" {
  name         = var.project
  environments = [var.environment]
}

resource "hakopod_virtual_network" "mesh" {
  project     = hakopod_project.this.name
  environment = hakopod_project.this.environments[0]
  spec = jsonencode({
    schema_version = 1
    name           = var.network
    description    = "Private calls between the ${length(local.services)} services of ${var.project}"
    segments = {
      (local.segment) = { applications = sort(keys(local.services)) }
    }
  })

  lifecycle {
    precondition {
      condition     = alltrue([for s in local.services : alltrue([for c in s.calls : contains(keys(local.services), c)])])
      error_message = "Every entry in local.services[*].calls must name a key of local.services."
    }
  }
}

resource "hakopod_application" "service" {
  for_each = local.applications

  project     = hakopod_project.this.name
  environment = hakopod_project.this.environments[0]
  # A destroyed application leaves a retained-data record even with no volume,
  # and a project holding retained data cannot be deleted. True so that
  # `terraform destroy` finishes; use false where data must survive.
  delete_data_on_destroy = true
  spec = jsonencode(merge(each.value, {
    networks = {
      (local.segment) = {
        internal        = true
        virtual_network = hakopod_virtual_network.mesh.name
        segment         = local.segment
      }
    }
  }))
}
