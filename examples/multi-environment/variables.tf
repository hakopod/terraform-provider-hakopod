variable "project" {
  description = "Hakopod project name."
  type        = string
  default     = "multi-env"
}

variable "environments" {
  description = "Sizing per environment. Each key becomes an environment of the project and one instance of modules/stack."
  type = map(object({
    image_tag = string
    replicas  = number
    resources = object({
      cpu_request    = string
      cpu_limit      = string
      memory_request = string
      memory_limit   = string
    })
  }))
  default = {
    staging = {
      image_tag = "1.27-alpine"
      replicas  = 1
      resources = {
        cpu_request    = "50m"
        cpu_limit      = "250m"
        memory_request = "64Mi"
        memory_limit   = "128Mi"
      }
    }
    production = {
      image_tag = "1.26-alpine"
      replicas  = 2
      resources = {
        cpu_request    = "100m"
        cpu_limit      = "500m"
        memory_request = "128Mi"
        memory_limit   = "256Mi"
      }
    }
  }
}
