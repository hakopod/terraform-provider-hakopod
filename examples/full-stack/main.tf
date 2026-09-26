# A complete application stack in one project and environment:
#
#   web (public)  -> api -> db (PostgreSQL, persistent volume)
#                       \-> cache (Redis, persistent volume)
#   worker (x2)   -> db, cache
#   scheduler     -> db   (nightly VACUUM ANALYZE)
#
# Six applications joined by one virtual network with three segments. web, db
# and cache are TOML templates; api, worker and scheduler are jsonencode specs.
#
# Ordering comes from references only: project -> network and secrets ->
# db and cache -> api, worker, scheduler -> web. Destroy runs in reverse.

terraform {
  required_providers {
    hakopod = {
      source = "hakopod/hakopod"
    }
    random = {
      source  = "hashicorp/random"
      version = "~> 3.6"
    }
  }
}

provider "hakopod" {} # HAKOPOD_API_URL and HAKOPOD_API_KEY

resource "hakopod_project" "this" {
  name         = var.project
  environments = var.environments
}

locals {
  project     = hakopod_project.this.name
  environment = hakopod_project.this.environments[0]

  database = "app"
  db_user  = "app"

  # Secrets are scoped per application, so the application names are written
  # out here. Referencing hakopod_application.x.name would be a cycle: the
  # application references its secrets.
  db_password_apps    = toset(["db", "api", "worker", "scheduler"])
  cache_password_apps = toset(["cache", "api", "worker"])

  # Private address of a service in another application. Referencing it also
  # makes Terraform create the target application first.
  db_host    = hakopod_application.db.service_hostnames["postgres"]
  cache_host = hakopod_application.cache.service_hostnames["redis"]
  api_host   = hakopod_application.api.service_hostnames["api"]

  # Connection settings shared by every PostgreSQL client. PGPASSWORD comes
  # from a secret, never from env.
  pg_env = {
    PGHOST     = local.db_host
    PGPORT     = "5432"
    PGDATABASE = local.database
    PGUSER     = local.db_user
  }

  # Each consuming application gets its own copy of a shared secret; see below.
  pg_secret    = { for app in local.db_password_apps : app => { PGPASSWORD = { ref = hakopod_secret.db_password[app].name } } }
  cache_secret = { for app in local.cache_password_apps : app => { REDIS_PASSWORD = { ref = hakopod_secret.cache_password[app].name } } }
}

resource "hakopod_virtual_network" "this" {
  project     = local.project
  environment = local.environment
  config      = file("${path.module}/network.toml")
}

# ---------------------------------------------------------------- secrets ----

# The PostgreSQL and Redis passwords are needed by several applications, and
# Hakopod secrets are per application. hakopod_secret's generate = true would
# give each application a different value, so the value is generated once here
# and written to one secret per application. Trade-off: the password is then
# in Terraform state (marked sensitive). Keep state in an encrypted backend.
resource "random_password" "db" {
  length  = 32
  special = false
}

resource "random_password" "cache" {
  length  = 32
  special = false
}

resource "hakopod_secret" "db_password" {
  for_each    = local.db_password_apps
  project     = local.project
  environment = local.environment
  application = each.key
  name        = "db-password"
  value       = random_password.db.result
}

resource "hakopod_secret" "cache_password" {
  for_each    = local.cache_password_apps
  project     = local.project
  environment = local.environment
  application = each.key
  name        = "cache-password"
  value       = random_password.cache.result
}

# Only the API needs its session key, so the server generates it and the value
# never reaches Terraform state.
resource "hakopod_secret" "session_key" {
  project     = local.project
  environment = local.environment
  application = "api"
  name        = "session-key"
  generate    = true
  format      = "hex"
}

# ------------------------------------------------------------ data tier ----

resource "hakopod_application" "db" {
  project                = local.project
  environment            = local.environment
  delete_data_on_destroy = var.delete_data_on_destroy
  config = templatefile("${path.module}/db/hakopod.toml.tftpl", {
    network         = hakopod_virtual_network.this.name
    image           = var.postgres_image
    volume_gib      = var.db_volume_gib
    database        = local.database
    user            = local.db_user
    password_secret = hakopod_secret.db_password["db"].name
  })

}

resource "hakopod_application" "cache" {
  project                = local.project
  environment            = local.environment
  delete_data_on_destroy = var.delete_data_on_destroy
  config = templatefile("${path.module}/cache/hakopod.toml.tftpl", {
    network         = hakopod_virtual_network.this.name
    image           = var.redis_image
    password_secret = hakopod_secret.cache_password["cache"].name
    secret_version  = hakopod_secret.cache_password["cache"].value_version
  })
}

# ------------------------------------------------------------ app tier ----

resource "hakopod_application" "api" {
  project                = local.project
  environment            = local.environment
  delete_data_on_destroy = var.delete_data_on_destroy
  spec = jsonencode({
    schema_version = 1
    name           = "api"
    networks = {
      frontend = { internal = true, virtual_network = hakopod_virtual_network.this.name, segment = "frontend" }
      data     = { internal = true, virtual_network = hakopod_virtual_network.this.name, segment = "data" }
      cache    = { internal = true, virtual_network = hakopod_virtual_network.this.name, segment = "cache" }
    }
    services = {
      api = {
        image       = var.api_image
        args        = ["--port", "8080"] # traefik/whoami placeholder; drop for a real image
        port        = 8080
        healthcheck = "/health"
        size        = "small"
        networks    = ["frontend", "data", "cache"]
        env = merge(local.pg_env, {
          REDIS_HOST = local.cache_host
          REDIS_PORT = "6379"
          # Changes on rotation, which redeploys the API with the new values.
          SECRET_VERSION = join("-", [
            hakopod_secret.db_password["api"].value_version,
            hakopod_secret.cache_password["api"].value_version,
            hakopod_secret.session_key.value_version,
          ])
        })
        secrets = merge(local.pg_secret["api"], local.cache_secret["api"], {
          SESSION_KEY = { ref = hakopod_secret.session_key.name }
        })
        network_access = {
          from              = []
          from_applications = ["web/web"]
        }
      }
    }
  })
}

resource "hakopod_application" "worker" {
  project                = local.project
  environment            = local.environment
  delete_data_on_destroy = var.delete_data_on_destroy
  spec = jsonencode({
    schema_version = 1
    name           = "worker"
    networks = {
      data  = { internal = true, virtual_network = hakopod_virtual_network.this.name, segment = "data" }
      cache = { internal = true, virtual_network = hakopod_virtual_network.this.name, segment = "cache" }
    }
    services = {
      worker = {
        image    = var.worker_image
        replicas = 2
        size     = "small"
        # Placeholder: checks it can reach Redis and PostgreSQL once a minute.
        command = ["sh", "-c", "while true; do for t in \"$REDIS_HOST 6379\" \"$PGHOST $PGPORT\"; do nc -z -w 3 $t && echo \"reachable: $t\" || echo \"unreachable: $t\"; done; sleep 60; done"]
        # No port: nothing connects to the worker.
        networks = ["data", "cache"]
        env = merge(local.pg_env, {
          REDIS_HOST = local.cache_host
          SECRET_VERSION = join("-", [
            hakopod_secret.db_password["worker"].value_version,
            hakopod_secret.cache_password["worker"].value_version,
          ])
        })
        secrets = merge(local.pg_secret["worker"], local.cache_secret["worker"])
      }
    }
  })
}

resource "hakopod_application" "scheduler" {
  project                = local.project
  environment            = local.environment
  delete_data_on_destroy = var.delete_data_on_destroy
  spec = jsonencode({
    schema_version = 1
    name           = "scheduler"
    networks = {
      data = { internal = true, virtual_network = hakopod_virtual_network.this.name, segment = "data" }
    }
    services = {
      cleanup = {
        # The PostgreSQL image carries psql. Replace with your own cleanup job.
        image    = var.postgres_image
        command  = ["psql", "--no-psqlrc", "-v", "ON_ERROR_STOP=1", "-c", "VACUUM (ANALYZE);"]
        size     = "small"
        networks = ["data"]
        env = merge(local.pg_env, {
          SECRET_VERSION = hakopod_secret.db_password["scheduler"].value_version
        })
        secrets = local.pg_secret["scheduler"]
        job = {
          timeout_seconds = 600
          retries         = 1
          schedule = {
            cron          = var.cleanup_cron
            timezone      = var.timezone
            history_limit = 1
          }
        }
      }
    }
  })
}

# ------------------------------------------------------------ edge ----

resource "hakopod_application" "web" {
  project                = local.project
  environment            = local.environment
  delete_data_on_destroy = var.delete_data_on_destroy
  config = templatefile("${path.module}/web/hakopod.toml.tftpl", {
    network = hakopod_virtual_network.this.name
    image   = var.web_image
    api_url = "http://${local.api_host}:8080"
  })
}
