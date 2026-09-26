output "web_status" {
  description = "Status of the web application."
  value       = hakopod_application.web.status
}

output "api_status" {
  description = "Status of the api application."
  value       = hakopod_application.api.status
}
