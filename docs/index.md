---
page_title: "Provider: Hakopod"
subcategory: ""
description: |-
  Manage Hakopod projects, applications and virtual networks.
---

# Hakopod Provider

The Hakopod provider manages [Hakopod](https://github.com/hakopod/hakopod) projects, applications and virtual networks through the Hakopod API. It works with self-hosted installations and Hakopod Cloud.

Configuration is the same application and network TOML that the Hakopod CLI and dashboard use. The server validates it with the same rules as the CLI, and every change goes through the same review and deployment as a CLI deploy.

## Example Usage

```terraform
terraform {
  required_providers {
    hakopod = {
      source = "hakopod/hakopod"
    }
  }
}

# url and api_key fall back to HAKOPOD_API_URL and HAKOPOD_API_KEY.
provider "hakopod" {
  url = "https://hakopod.example.com"
  # workspace = "ws_..." # Hakopod Cloud only; or HAKOPOD_WORKSPACE.
}
```

## Authentication

The provider authenticates with a Hakopod API key. Set it with `api_key` or the `HAKOPOD_API_KEY` environment variable; the CLI reads the same variables, so a shell configured for the CLI also configures the provider.

Permissions depend on the key:

- Destroying a `hakopod_project` needs an installation administrator.
- Creating, updating and destroying `hakopod_virtual_network` needs a project administrator.
- Destroying a `hakopod_application` needs a project administrator.
- CI machine keys can create and update applications, but cannot manage virtual networks or delete applications. A pipeline that only deploys applications can use a machine key; run network changes and destroys with an administrator key.

## Model

The provider follows Hakopod's own structure: a **project** contains **applications**, an application contains **services**, and **virtual networks** connect services in different applications of the same project and environment.

| Resource | Contains | Scope |
| --- | --- | --- |
| `hakopod_project` | environments, applications, virtual networks | installation or workspace |
| `hakopod_application` | services, local networks, volumes | one project and environment |
| `hakopod_virtual_network` | segments, each granting named applications | one project and environment |

### Project → applications

Set `project` and `environment` on applications and networks from the project resource:

```terraform
resource "hakopod_application" "catalog" {
  project     = hakopod_project.commerce.name
  environment = hakopod_project.commerce.environments[0]
  # ...
}
```

A literal environment name works too, as long as it is in the project's `environments` list; only the `project` reference is needed for ordering.

### Applications → services

Services are defined inside the application, in its TOML (`config`) or JSON specification (`spec`). There is no separate service resource. An application deploys as one revision, all services together; separate service resources would each deploy a partial revision and roll the application's pods once per resource. With `spec = jsonencode({...})` the services are plain HCL and can use references and variables.

### Virtual networks between applications

A virtual network grants applications access to its segments. A grant names an application and can be created before that application exists. An application joins by declaring an internal network with `virtual_network` and `segment`, and listing it in the `networks` of the services that need it.

Take the network name from the network resource instead of writing it out. The reference orders the application after the network; no `depends_on` is needed.

```terraform
# spec: reference the name directly.
networks = {
  data = {
    internal        = true
    virtual_network = hakopod_virtual_network.commerce.name
    segment         = "data"
  }
}

# config: pass the name into a TOML template.
config = templatefile("${path.module}/catalog/hakopod.toml.tftpl", {
  network = hakopod_virtual_network.commerce.name
})
```

with `virtual_network = "${network}"` in the template. A plain `file()` with the name written in the TOML is not a reference; Terraform may then deploy the application before the network or its grant exists, and the server rejects the deployment.

### Ordering and destroy

With these references Terraform creates the project, then the network, then the applications. Destroy runs in reverse: applications first (a network cannot be destroyed while an application uses its grants), then networks, then the project (which must be empty).

A complete configuration is in `examples/tree` in the provider repository.

## Schema

### Optional

- `api_key` (String, Sensitive) Hakopod API key. Defaults to the `HAKOPOD_API_KEY` environment variable. Required from one of the two sources.
- `url` (String) Hakopod API URL. Defaults to the `HAKOPOD_API_URL` environment variable. Required from one of the two sources.
- `workspace` (String) Hakopod Cloud workspace ID. Defaults to the `HAKOPOD_WORKSPACE` environment variable. Leave unset for self-hosted installations.
