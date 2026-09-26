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
