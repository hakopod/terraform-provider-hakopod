resource "hakopod_virtual_network" "commerce" {
  project     = hakopod_project.commerce.name
  environment = hakopod_project.commerce.environments[0]
  config      = file("${path.module}/network.toml")
}
