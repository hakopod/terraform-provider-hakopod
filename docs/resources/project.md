---
page_title: "hakopod_project Resource - hakopod"
subcategory: ""
description: |-
  Manages a Hakopod project and its environments.
---

# hakopod_project (Resource)

Manages a Hakopod project and the environments in it. Applications and virtual networks belong to one project and one environment; set their `project` and `environment` from this resource so Terraform creates the project first and destroys it last.

## Example Usage

```terraform
resource "hakopod_project" "commerce" {
  name         = "commerce"
  display_name = "Commerce"
  description  = "Storefront, catalog and data services"

  # The first environment is created with the project. Environments can be
  # added later but not removed.
  environments = ["production", "staging"]
}

# Applications and networks name the project by reference, so Terraform
# creates the project first and destroys it last.
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
}
```

## Behaviour

- **Create** creates the project with the first entry of `environments`, then creates the rest. It fails if a project with the same name already exists; import it instead.
- **Destroy** succeeds only when the project is empty: no applications, networks or builds, and no data retained from destroyed applications. Reclaim retained data in the dashboard, or set `delete_data_on_destroy = true` on applications before destroying them.
- **Update** adds environments that are new in `environments`. Removing an environment from the list fails at plan: Hakopod has no environment delete. The order of the list does not matter after creation, but configurations that use `environments[0]` follow the first entry, so keep it stable; changing an application's `environment` replaces the application.
- **Destroy** deletes the project only when it is empty in every environment: its applications, virtual networks, build configurations, registries and secret-provider grants must be removed first. When those are managed in the same configuration, references to this resource already order their destroy before the project's.
- A deleted project's name can never be used again. Hakopod keeps deleted project IDs reserved so that old keys, webhooks and name-based network grants cannot reach a new project. Recreating the resource after a destroy, including a replacement caused by changing `name`, needs a new name.

## Schema

### Required

- `environments` (List of String) Environment names, at least one and unique, using the same character rules as `name`. The first is created with the project. Entries can be added; they cannot be removed.
- `name` (String) Project name and ID: 1–40 lowercase letters, digits and hyphens. Changing this forces a new resource.

### Optional

- `description` (String) Project description, at most 1000 characters. Can only be set at creation; the API cannot change it later.
- `display_name` (String) Name shown in the dashboard, 1–80 characters. Defaults to the project name. Can be changed in place.

### Read-Only

- `id` (String) Project ID assigned by the server.
- `metadata_revision` (Number) Server revision of the project's display metadata.

## Import

Import an existing project by name:

```shell
# Import a project by name.
terraform import hakopod_project.commerce commerce
```
