---
page_title: "hakopod_application Resource - hakopod"
subcategory: ""
description: |-
  Manages a Hakopod application from application TOML or a JSON specification, deploying it on create and update.
---

# hakopod_application (Resource)

Manages a Hakopod application in one project and environment. The configuration is the application TOML the CLI uses (`config`) or the equivalent JSON specification (`spec`). The server validates either with the same rules as the CLI; an invalid configuration fails the plan or apply with the server's message. When an application joins a virtual network that the same plan creates or changes, the server cannot confirm the membership until the network is applied: the plan shows a warning and the check happens at apply.

Create and update deploy the application. A healthy application whose configuration has no changes is not redeployed.

## Example Usage

```terraform
# From an application TOML file.
resource "hakopod_application" "shop" {
  project     = hakopod_project.commerce.name
  environment = hakopod_project.commerce.environments[0]
  config      = file("${path.module}/shop/hakopod.toml")

  # Contents for env_file references in the TOML. Imported into secrets on review.
  env_files = {
    ".env" = file("${path.module}/shop/.env")
  }
}

# From a JSON specification. Services are defined inside the application.
resource "hakopod_application" "web" {
  project     = hakopod_project.commerce.name
  environment = hakopod_project.commerce.environments[0]
  spec = jsonencode({
    schema_version = 1
    name           = "web"
    services = {
      web = {
        image  = "nginx:stable-alpine"
        port   = 80
        public = true
      }
    }
  })

  wait_timeout = "10m"
}
```

Services are part of the application, not separate resources: an application deploys as one revision with all its services.

### Applications on a virtual network

Take the network name from the `hakopod_virtual_network` resource. The reference orders the application after the network and its grants; no `depends_on` is needed.

```terraform
resource "hakopod_virtual_network" "commerce" {
  project     = hakopod_project.commerce.name
  environment = hakopod_project.commerce.environments[0]
  config      = file("${path.module}/network.toml")
}

# JSON specification: reference the name directly.
resource "hakopod_application" "database" {
  project     = hakopod_project.commerce.name
  environment = hakopod_project.commerce.environments[0]
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
      }
    }
  })
}

# TOML: pass the name into a template that contains
#   virtual_network = "${network}"
resource "hakopod_application" "catalog" {
  project     = hakopod_project.commerce.name
  environment = hakopod_project.commerce.environments[0]
  config = templatefile("${path.module}/catalog/hakopod.toml.tftpl", {
    network = hakopod_virtual_network.commerce.name
  })
}
```

With `file()` and the network name written into the TOML there is no reference, and Terraform may deploy the application before the network exists.

## Behaviour

### Plan

During plan the provider asks the server to review the configuration and reports the number of changes in `pending_changes`. Changes made outside Terraform, for example in the dashboard, count as changes there and are reverted by the next apply. Drift is not detected while `env_files` is set: the provider does not contact the server at plan time then, because review imports the env files into secrets. Change the configuration or the env files to deploy again.

When `env_files` is set, plan does not contact the server, because reviewing the configuration would import the env files into secrets. `pending_changes` is then unknown until apply.

### Apply

- The deployment uses the reviewed configuration. With `wait = true` (default), apply waits up to `wait_timeout` for the deployment to finish and fails if it does not succeed in that time. With `wait = false`, apply returns once the deployment is submitted.
- If the first deployment of a new application fails, apply fails but the application is **not** saved to state, so Terraform does not taint it. Tainting would make the next apply destroy and recreate it, and a destroyed application's name can never be used again. Fix the configuration and apply again: an application with the same name that has never had a successful release is adopted and deployed. An existing application that has succeeded before is never adopted; import it instead.
- A failed deployment of an application already in state fails the apply and leaves it in state; the next apply deploys again.
- Secrets the configuration references must already exist. If any are missing, apply fails and lists their names; set them in the dashboard or with the CLI, then apply again.
- The application name comes from the configuration. Changing it replaces the application.

### Destroy

Destroy deploys an empty revision (no services), waits for it, then deletes the application. Persistent volumes are retained unless `delete_data_on_destroy = true`.

**Destroying an application retires its name permanently** in that project and environment: a new application can never use it. This also applies when a change of `name` replaces the application. Retained data of a destroyed application must be reclaimed (dashboard Storage) before its project can be destroyed, unless `delete_data_on_destroy = true`. Destroy needs a project administrator; CI machine keys cannot delete applications.

## Schema

### Required

- `environment` (String) Environment name. Changing this forces a new resource.
- `project` (String) Project name. Changing this forces a new resource.

### Optional

Exactly one of `config` or `spec` must be set.

- `config` (String) Application TOML, for example `file("app/hakopod.toml")`.
- `delete_data_on_destroy` (Boolean) Delete persistent volumes when the application is destroyed. Defaults to `false`; volumes are retained.
- `env_files` (Map of String, Sensitive) File contents for `env_file` references in `config`, keyed by the filename used in the TOML, for example `{ ".env" = file(".env") }`. Values are imported into application secrets and values during review. Use only with `config`; a JSON `spec` must already contain expanded values and secret references.
- `spec` (String) Application specification as JSON, for example `jsonencode({...})`.
- `wait` (Boolean) Wait for the deployment to finish before apply returns. Defaults to `true`.
- `wait_timeout` (String) Maximum time to wait, as a Go duration. Defaults to `"20m"`.

### Read-Only

- `deployment_id` (String) ID of the last deployment this resource made.
- `id` (String) Application ID assigned by the server.
- `name` (String) Application name, from the configuration.
- `pending_changes` (Number) Number of changes the server reported between the deployed application and the configuration in the last planned deployment.
- `revision` (Number) Current application revision.
- `service_hostnames` (Map of String) Private DNS name of each service, keyed by service name. Another application that joins a shared virtual network segment and is allowed by the service's `network_access` reaches it at this name. Known after the application is first created.
- `status` (String) Application status reported by the server, for example `healthy`, `recovered`, `failed` or `empty`.

## Import

Import an existing application by `project/environment/name`:

```shell
# Import an application by project/environment/name.
terraform import hakopod_application.shop commerce/production/shop
```

After import, set `config` or `spec` to match the deployed application; the next plan reports any difference in `pending_changes`.
