output "statuses" {
  description = "Status of each application, for example healthy or failed."
  value = {
    web       = hakopod_application.web.status
    api       = hakopod_application.api.status
    worker    = hakopod_application.worker.status
    scheduler = hakopod_application.scheduler.status
    db        = hakopod_application.db.status
    cache     = hakopod_application.cache.status
  }
}

output "revisions" {
  description = "Current revision of each application."
  value = {
    web       = hakopod_application.web.revision
    api       = hakopod_application.api.revision
    worker    = hakopod_application.worker.revision
    scheduler = hakopod_application.scheduler.revision
    db        = hakopod_application.db.revision
    cache     = hakopod_application.cache.revision
  }
}

output "private_addresses" {
  description = "Private addresses other applications use on the virtual network."
  value = {
    api   = "${local.api_host}:8080"
    db    = "${local.db_host}:5432"
    cache = "${local.cache_host}:6379"
  }
}
