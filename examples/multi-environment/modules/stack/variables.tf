variable "project" {
  description = "Project name. Pass hakopod_project.<name>.name so the stack is created after the project."
  type        = string
}

variable "environment" {
  description = "Environment name; must be in the project's environments."
  type        = string
}

variable "image_tag" {
  description = "Tag of nginxinc/nginx-unprivileged used by both applications."
  type        = string
}

variable "replicas" {
  description = "Replicas per service."
  type        = number
}

variable "resources" {
  description = "Per-replica CPU/memory requests and limits for every service."
  type = object({
    cpu_request    = string
    cpu_limit      = string
    memory_request = string
    memory_limit   = string
  })
}
