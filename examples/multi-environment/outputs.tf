output "web_status" {
  description = "Status of the web application, per environment."
  value       = { for env, stack in module.stack : env => stack.web_status }
}

output "api_status" {
  description = "Status of the api application, per environment."
  value       = { for env, stack in module.stack : env => stack.api_status }
}
