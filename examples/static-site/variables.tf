variable "project" {
  description = "Hakopod project name."
  type        = string
  default     = "static-site"
}

variable "image" {
  description = "Image that serves the site. It must run as a non-root user."
  type        = string
  default     = "nginxinc/nginx-unprivileged:1.27-alpine"
}

variable "port" {
  description = "Port the image listens on."
  type        = number
  default     = 8080
}

variable "run_as_user" {
  description = "Non-root UID (also used as the GID) the container runs as. 101 matches nginx-unprivileged."
  type        = number
  default     = 101
}

variable "healthcheck_path" {
  description = "HTTP path that returns 200 when the site is ready."
  type        = string
  default     = "/"
}

variable "domain" {
  description = "Optional custom hostname, for example www.example.com. Verify it in Hakopod before setting it; empty means no custom domain."
  type        = string
  default     = ""
}
