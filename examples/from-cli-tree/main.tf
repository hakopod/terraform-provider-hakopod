# Manage a `hakopod deploy --dir` folder with Terraform, using the same files.
#
# apps/
# ├── network.toml          optional: one virtual network
# ├── api/HAKOPOD.toml      one application per immediate subfolder;
# ├── web/hakopod.toml      the file name is matched case-insensitively,
# └── worker/hakopod.toml   as the CLI does
#
# Adding a folder adds an application; no Terraform change is needed.

terraform {
  required_providers {
    hakopod = {
      source = "hakopod/hakopod"
    }
  }
}

provider "hakopod" {}

variable "project" {
  description = "Name of an existing Hakopod project, as passed to `hakopod deploy --project`."
  type        = string
}

variable "environment" {
  description = "Environment, as passed to `hakopod deploy --environment`."
  type        = string
  default     = "production"
}

variable "root" {
  description = "Folder the CLI deploys with --dir."
  type        = string
  default     = "apps"
}

locals {
  root = "${path.module}/${var.root}"

  # fileset is case-sensitive on Linux, so list every file one level down and
  # compare names in lower case. Hidden folders are skipped, as by the CLI.
  # A folder with two case variants (hakopod.toml and HAKOPOD.toml) produces a
  # duplicate key and fails the plan, as it fails the CLI.
  apps = {
    for f in fileset(local.root, "*/*") :
    dirname(f) => "${local.root}/${f}"
    if lower(basename(f)) == "hakopod.toml" && !startswith(f, ".")
  }
}

resource "hakopod_virtual_network" "net" {
  count = fileexists("${local.root}/network.toml") ? 1 : 0

  project     = var.project
  environment = var.environment
  config      = file("${local.root}/network.toml")
}

resource "hakopod_application" "app" {
  for_each = local.apps

  project     = var.project
  environment = var.environment
  # A destroyed application leaves a retained-data record even with no volume,
  # and a project holding retained data cannot be deleted. True so that
  # `terraform destroy` finishes; use false where data must survive.
  delete_data_on_destroy = true
  config                 = file(each.value)

  # The TOML names the network literally (virtual_network = "shop"), which is
  # not a Terraform reference. depends_on restores the order the CLI uses:
  # network first, then applications; destroy runs in reverse.
  depends_on = [hakopod_virtual_network.net]
}

output "applications" {
  value = { for k, app in hakopod_application.app : k => app.status }
}
