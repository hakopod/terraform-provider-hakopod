variable "project" {
  description = "Hakopod project name. A destroyed project's name can never be used again."
  type        = string
  default     = "fullstack-demo"
}

variable "environments" {
  description = "Project environments. The stack deploys to the first; entries can be added, not removed."
  type        = list(string)
  default     = ["production"]
}

variable "web_image" {
  description = "Public frontend image. Must listen on 8080 as a non-root user."
  type        = string
  default     = "docker.io/nginxinc/nginx-unprivileged:1.27-alpine"
}

variable "api_image" {
  description = "API image. Must listen on 8080 and answer GET /health."
  type        = string
  default     = "docker.io/traefik/whoami:v1.10"
}

variable "worker_image" {
  description = "Background worker image."
  type        = string
  default     = "docker.io/library/busybox:1.37"
}

variable "postgres_image" {
  description = "PostgreSQL image, also used by the scheduled job for psql. Pinned as in the Hakopod PostgreSQL blueprint."
  type        = string
  default     = "docker.io/library/postgres:17.11-alpine@sha256:18cfe3ef5e6815560c98237d6216d1e5119702fb0f3894c8785dd58b8bbe5d73"
}

variable "redis_image" {
  description = "Redis image. Pinned as in the Hakopod Redis blueprint."
  type        = string
  default     = "docker.io/library/redis:8.6.6-alpine@sha256:75934ddb37bfaebe3b4082ba673cac39f66495244134f33dd0a502ce03cdcd36"
}

variable "db_volume_gib" {
  description = "PostgreSQL volume size in GiB. Cannot be changed by an ordinary deployment once created."
  type        = number
  default     = 5
}

variable "cleanup_cron" {
  description = "Five-field cron schedule for the nightly database cleanup."
  type        = string
  default     = "0 3 * * *"
}

variable "timezone" {
  description = "IANA timezone for cleanup_cron."
  type        = string
  default     = "UTC"
}

variable "delete_data_on_destroy" {
  description = <<-EOT
    Delete each application's retained data when it is destroyed. True here so
    that `terraform destroy` empties the project and can then delete it: a
    destroyed application leaves a retained-data record even when it has no
    volume, and a project with retained data cannot be deleted. Set it to false
    for anything whose database you want to survive, and reclaim the records by
    hand (dashboard Storage) before destroying the project.
  EOT
  type        = bool
  default     = true
}
