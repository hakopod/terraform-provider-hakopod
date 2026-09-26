---
page_title: "hakopod_virtual_network Resource - hakopod"
subcategory: ""
description: |-
  Manages a Hakopod virtual network: named segments and the applications granted access to each.
---

# hakopod_virtual_network (Resource)

Manages a virtual network in one project and environment. A virtual network contains named segments; each segment lists the applications granted access to it. A grant permits an application to join a segment; it does not connect any service. Services join through their application configuration (`virtual_network` and `segment` on an internal network).

Development, staging and production have separate network scopes, even when they use the same names.

Creating, updating and destroying a network needs a project administrator. CI machine keys cannot manage virtual networks.

## Example Usage

```terraform
resource "hakopod_virtual_network" "commerce" {
  project     = hakopod_project.commerce.name
  environment = hakopod_project.commerce.environments[0]
  config      = file("${path.module}/network.toml")
}
```

With `network.toml`:

```toml
schema_version = 1
name = "commerce"
description = "Private catalog and data services"

[segments.data]
applications = ["catalog", "database"]
```

## Behaviour

- **Create** fails if a network with the same name already exists in the project and environment. Import it instead.
- On create, `name` is known only after apply, so applications that reference it are reviewed by the server at apply rather than at plan.
- **Update** saves the configuration against the reviewed revision, the one Terraform last read. A change made on the server after that read is not silently overwritten.
- **Destroy** fails while any application still uses one of the network's grants. Remove the network from those applications, or destroy them first.

Applications join a segment through their own configuration. Set `virtual_network` there from this resource's `name` (`hakopod_virtual_network.commerce.name`, directly in `spec` or through `templatefile` for TOML); the reference makes Terraform create the network before those applications and destroy it after them. A grant can name an application that does not exist yet. See `hakopod_application` for both forms.

## Schema

### Required

- `environment` (String) Environment name. Changing this forces a new resource.
- `project` (String) Project name. Changing this forces a new resource.

### Optional

Exactly one of `config` or `spec` must be set.

- `config` (String) Network TOML, for example `file("network.toml")`.
- `spec` (String) Network specification as JSON, for example `jsonencode({...})`.

### Read-Only

- `id` (String) Network ID assigned by the server.
- `name` (String) Network name, from the configuration. Changing it replaces the network.
- `revision` (Number) Server revision of the network definition.

## Import

Import an existing network by `project/environment/name`:

```shell
# Import a virtual network by project/environment/name.
terraform import hakopod_virtual_network.commerce commerce/production/commerce
```
