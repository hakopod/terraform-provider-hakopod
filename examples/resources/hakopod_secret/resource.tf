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
