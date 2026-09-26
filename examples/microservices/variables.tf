variable "project" {
  description = "Project that holds every application and the network."
  type        = string
  default     = "shop"
}

variable "environment" {
  description = "Environment the applications and network are deployed to."
  type        = string
  default     = "production"
}

variable "network" {
  description = "Virtual network name."
  type        = string
  default     = "shop-mesh"
}
