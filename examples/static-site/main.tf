# The smallest useful Hakopod project: one project and one public web
# application serving a static site, optionally on a custom domain.
#
# The application names the project through hakopod_project.site, so
# Terraform creates the project first and destroys it last.

terraform {
  required_providers {
    hakopod = {
      source = "hakopod/hakopod"
    }
  }
}

provider "hakopod" {}

resource "hakopod_project" "site" {
  name         = var.project
  environments = ["production"]
}

resource "hakopod_application" "site" {
  project     = hakopod_project.site.name
  environment = hakopod_project.site.environments[0]
  # A destroyed application leaves a retained-data record even with no volume,
  # and a project holding retained data cannot be deleted. True so that
  # `terraform destroy` finishes; use false where data must survive.
  delete_data_on_destroy = true

  spec = jsonencode(merge(
    {
      schema_version = 1
      name           = "site"
      services = {
        web = {
          image       = var.image
          port        = var.port
          public      = true
          healthcheck = var.healthcheck_path
          replicas    = 2
          size        = "small"
          # nginx-unprivileged runs as uid/gid 101. Set these to your
          # image's user, or remove them to use the default 10001.
          run_as_user  = var.run_as_user
          run_as_group = var.run_as_user
        }
      }
    },
    # A custom domain routes only after it is verified in Hakopod
    # (Custom domains). Leave var.domain empty until then.
    var.domain == "" ? {} : { domains = { (var.domain) = "web" } },
  ))
}
