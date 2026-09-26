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
