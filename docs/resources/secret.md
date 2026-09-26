---
page_title: "hakopod_secret Resource - hakopod"
subcategory: ""
description: |-
  Manages a Hakopod application secret.
---

# hakopod_secret (Resource)

Manages one secret of a Hakopod application. A service uses it by name: `ENV = { ref = "<name>" }` in `[services.<service>.secrets]`. Hakopod never returns secret values, so Terraform can set a value but cannot read one back.

The value comes from one of two places. Either you supply `value`, which is stored in Terraform state, or you set `generate = true` and Hakopod generates 32 random bytes that never enter Terraform state.

## Example Usage

```terraform
variable "database_password" {
  type      = string
  sensitive = true
}

# A value you supply. It is stored in Terraform state; keep state encrypted.
resource "hakopod_secret" "database_password" {
  project     = "shop"
  environment = "production"
  application = "api"
  name        = "database-password"
  value       = var.database_password
}

# A value Hakopod generates. It never enters Terraform state.
resource "hakopod_secret" "session_key" {
  project     = "shop"
  environment = "production"
  application = "api"
  name        = "session-key"
  generate    = true
  format      = "hex"
}

# Services reference secrets by name. value_version in env redeploys the
# application when a secret changes, because Hakopod needs a restart to
# pick up a new value.
resource "hakopod_application" "api" {
  project     = "shop"
  environment = "production"
  config      = <<-TOML
    schema_version = 1
    name = "api"

    [services.api]
    image = "ghcr.io/example/api:1.4.2"
    port = 8080

    [services.api.env]
    SECRET_VERSION = "${hakopod_secret.database_password.value_version}-${hakopod_secret.session_key.value_version}"

    [services.api.secrets]
    DATABASE_PASSWORD = { ref = "${hakopod_secret.database_password.name}" }
    SESSION_KEY = { ref = "${hakopod_secret.session_key.name}" }
  TOML
}
```

## Redeploying when a secret changes

A running application keeps using the old value until it restarts. Changing a secret does not restart the application. To redeploy it whenever a secret changes, put the secret's `value_version` in the application's `env`, for example `SECRET_VERSION = hakopod_secret.database_password.value_version`. The application then changes whenever the secret changes, so Terraform redeploys it after the secret has been written. `value_version` never reveals the value.

## Behaviour

- **Create** stores `value`, or asks Hakopod to generate a value. It fails if the application already has a secret with that name; import it instead. The application does not need to exist yet, so create secrets before the application that references them.
- **Update** writes a changed `value` in place and warns that the application must restart to use it. Changing `format`, or switching between `value` and `generate`, replaces the secret. A replacement generates a new value.
- **Read** checks only that the secret still exists. A secret deleted outside Terraform is created again on the next apply. Changes made to the value outside Terraform are not detected.
- **Destroy** deletes the secret. Hakopod refuses to delete a GitHub runner pool's credential while the pool still uses it.

## Schema

### Required

- `application` (String) Application name, using the same character rules as `name`. The application does not need to exist yet. Changing this forces a new resource.
- `environment` (String) Environment name. Changing this forces a new resource.
- `name` (String) Reference name used in `{ ref = "<name>" }`: 1–40 lowercase letters, digits and hyphens, starting with a letter and not ending with a hyphen. Changing this forces a new resource.
- `project` (String) Project name. Changing this forces a new resource.

### Optional

- `format` (String) Encoding of a generated value: `base64url` (the default) or `hex`. Only with `generate`. Changing this forces a new resource.
- `generate` (Boolean) Set to `true` to let Hakopod generate the value. Exactly one of `value` or `generate` is required.
- `value` (String, Sensitive) The secret value: 1 byte to 64 KiB, without NUL characters. It is stored in Terraform state. Exactly one of `value` or `generate` is required. Can be changed in place.

### Read-Only

- `id` (String) `project/environment/application/name`.
- `value_version` (String) Changes whenever the value changes. For `value` it is the first 16 hex characters of the value's SHA-256; for `generate` it is a random token set when the value is generated.

## Import

Import an existing secret by `project/environment/application/name`. Its value cannot be read, so on the next apply Terraform adopts your configuration: with `value` it writes that value once; with `generate` it records the existing secret without changing it. Importing never replaces or regenerates a secret.

```shell
# Import a secret by project/environment/application/name.
terraform import hakopod_secret.database_password shop/production/api/database-password
```
