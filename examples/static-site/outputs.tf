output "status" {
  description = "Application status reported by Hakopod."
  value       = hakopod_application.site.status
}

output "revision" {
  description = "Deployed application revision."
  value       = hakopod_application.site.revision
}
