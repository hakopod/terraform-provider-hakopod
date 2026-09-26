# Full application stack

A complete stack on Hakopod, managed entirely by Terraform: a public web
frontend, an internal API, background workers, a nightly scheduled job,
PostgreSQL and Redis on persistent volumes, and one virtual network whose
segments decide who can reach what.

```text
                    internet
                       │ HTTPS
                 ┌─────▼─────┐
                 │ web  (x2) │ public :8080
                 └─────┬─────┘
                       │ segment frontend
                 ┌─────▼─────┐
     ┌───────────┤ api       ├────────────┐
     │           └───────────┘            │
     │ segment data                       │ segment cache
     │  ┌─────────────┐                   │
     ├──┤ worker (x2) ├───────────────────┤
     │  └─────────────┘                   │
     │  ┌─────────────────────────┐       │
     ├──┤ scheduler  03:00 VACUUM │       │
     │  └─────────────────────────┘       │
┌────▼─────────────────────┐    ┌─────────▼────────┐
│ db  PostgreSQL 17, 5 GiB │    │ cache  Redis 8,  │
│ volume                   │    │ 1 GiB volume     │
└──────────────────────────┘    └──────────────────┘
```

Each box is one Hakopod application with one service, all in project
`fullstack-demo` (environment `production`), joined by the virtual network
`fullstack` ([network.toml](network.toml)):

| Segment | Applications | Allowed traffic |
| --- | --- | --- |
| `frontend` | web, api | `web/web` → `api/api:8080` |
| `data` | api, worker, scheduler, db | `api/api`, `worker/worker`, `scheduler/cleanup` → `db/postgres:5432` |
| `cache` | api, worker, cache | `api/api`, `worker/worker` → `cache/redis:6379` |

A segment grant only lets an application join; each target service also lists
its exact callers in `network_access.from_applications`. Only `web` has a
non-internal network, so it is the only application with internet egress.

The example shows both authoring styles: `web`, `db` and `cache` are TOML
templates rendered with `templatefile`, and `api`, `worker` and `scheduler` are
`spec = jsonencode(...)`. `db` and `cache` use the settings of the Hakopod
PostgreSQL and Redis blueprints (non-root users, volume layout, pinned images).

Total requests: about 0.9 CPU and 1.2 GiB of memory, plus 6 GiB of storage. A
single small node is enough.

## Prerequisites

- Terraform 1.5 or later.
- A Hakopod installation and an API key of an installation administrator, or a
  project administrator for an existing project. Creating the network and
  destroying applications needs project administrator rights; CI machine keys
  cannot do either.
- A storage class for persistent volumes (the local provisioner of a single
  node cluster is enough).

## Deploy

```sh
export HAKOPOD_API_URL=https://hakopod.example.com
export HAKOPOD_API_KEY=...

terraform init
terraform apply
terraform output statuses
```

Terraform creates the project, then the network and the secrets, then `db`
and `cache`, then `api`, `worker` and `scheduler`, and `web` last. No
`depends_on` is used: the order comes from references. The applications that
connect to `db` and `cache` take their private addresses from those
applications' IDs, so they are deployed after them. The web URL is shown on
the `web` application's page in the dashboard.

Set `project` if the default name is taken (see [Destroy](#destroy)):

```sh
terraform apply -var project=shop-staging
```

## Replace the placeholder images

The images are placeholders that run as-is:

| Application | Placeholder | Replace with |
| --- | --- | --- |
| web | `nginxinc/nginx-unprivileged` | your frontend; must listen on 8080 as a non-root user |
| api | `traefik/whoami` | your API; must listen on 8080 and answer `GET /health` |
| worker | `busybox`, checks it can reach Redis and PostgreSQL | your worker |
| scheduler | `postgres` running `psql -c "VACUUM (ANALYZE);"` | your cleanup job |

Set `web_image`, `api_image` and `worker_image`, then remove the placeholder
lines: `args` of the API (the whoami `--port` flag) and `run_as_user` /
`run_as_group` of web (nginx's UID; Hakopod otherwise runs services as UID
10001). The API, worker and scheduler get the database connection in `PGHOST`,
`PGPORT`, `PGDATABASE`, `PGUSER` and `PGPASSWORD` (standard libpq variables),
Redis in `REDIS_HOST`, `REDIS_PORT` and `REDIS_PASSWORD`, and the API also a
`SESSION_KEY`. Web gets `API_URL`. Hakopod runs images without root and
without Linux capabilities.

## Secrets and rotation

Credentials are never written into TOML or `env`; Hakopod rejects recognised
credential names there. Services reference secrets by name, and secrets are
scoped to one application.

- **`session-key`** is needed only by the API, so `hakopod_secret` generates it
  on the server (`generate = true`). Its value never enters Terraform state.
- **`db-password`** and **`cache-password`** are needed by several applications
  (PostgreSQL itself and every client). A generated secret would differ per
  application, so `random_password` generates each value once and it is written
  to one secret per application. The trade-off: these values are in Terraform
  state, marked sensitive. Keep state in an encrypted backend with restricted
  access.

Every consumer puts the secrets' `value_version` in its `SECRET_VERSION`
environment variable. It changes when a value changes, so the application is
redeployed and picks up the new value. To rotate:

```sh
terraform apply -replace=random_password.cache        # Redis password
terraform apply -replace=hakopod_secret.session_key   # API session key
```

The Redis password rotates cleanly: Redis reads it at start, and `cache`
restarts with its clients. **The PostgreSQL password does not.** The
`postgres` image reads `POSTGRES_PASSWORD` only when it initialises an empty
data directory, so a new value would lock the clients out. Change it in the
database first, then in Terraform:

```sh
# 1. In the db application's terminal (dashboard), with the new password:
psql -U app -d app -c "ALTER USER app PASSWORD '...'"
# 2. Supply the same value to Terraform instead of random_password, and apply.
```

## Destroy

```sh
terraform destroy
```

Destroy runs in reverse: the applications (each first deploys an empty
revision), then the network and secrets, then the project. It needs a project
administrator key for the applications and an installation administrator key
for the project.

- **Names are retired permanently.** A destroyed application's name can never
  be used again in that project and environment, and a destroyed project's name
  can never be used again at all. To start over, pick a new `project`.
- **Data is deleted with the stack.** Every application sets
  `delete_data_on_destroy = var.delete_data_on_destroy`, which defaults to
  `true` so that a full `terraform destroy` works end to end — including the
  PostgreSQL and Redis volumes. It has to: a destroyed application leaves a
  retained-data record even when it has no volume, and a project that holds
  retained data cannot be deleted, so with the default `false` the destroy
  fails at the project until every record is reclaimed by hand (dashboard,
  Storage). For a stack whose database must survive, set
  `delete_data_on_destroy = false` and reclaim the records yourself before
  destroying the project.
