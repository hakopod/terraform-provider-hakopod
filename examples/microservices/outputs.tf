output "network" {
  value = hakopod_virtual_network.mesh.name
}

output "callers" {
  description = "Allowed callers of each service, derived from local.services[*].calls."
  value       = local.callers
}

output "addresses" {
  description = <<-EOT
    Private address of each service, reachable from its allowed callers.
    Instances of one for_each resource cannot reference each other, so a
    caller cannot read a peer's address from its own spec; pass it in as a
    variable from here, or split the caller out into its own resource as
    examples/full-stack does.
  EOT
  value = {
    for name, app in hakopod_application.service :
    name => "http://${app.service_hostnames[local.service_name]}:${local.services[name].port}"
  }
}

output "status" {
  description = "Deployment status of each application."
  value       = { for name, app in hakopod_application.service : name => app.status }
}
