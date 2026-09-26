# Microservices from one map

Five services, each its own Hakopod application, all generated from
`local.services` in [main.tf](main.tf):

```hcl
gateway  = { image = "traefik/whoami:v1.11", port = 8080, public = true,  calls = ["orders", "users"] }
orders   = { ..., calls = ["payments", "users"] }
payments = { ..., calls = ["notifications"] }
users    = { ..., calls = [] }
notifications = { ..., calls = [] }
```

From that map Terraform derives:

- **One virtual network** (`hakopod_virtual_network.mesh`) with a single segment, `mesh`, that grants every application in the map.
- **One application per entry** (`hakopod_application.service["<name>"]`, via `for_each`). Each runs one service, `api`, which joins `mesh` through an internal network.
- **Ingress from `calls`.** Terraform reverses `calls` so that each service's `network_access.from_applications` lists exactly the applications that call it. For example, `users` accepts `gateway/api` and `orders/api` and denies everything else. `gateway` has no callers, so its list is empty and it is reachable only through its public HTTP route.

To add a service, add one entry. Terraform then grants it the segment, deploys it, and opens ingress on every service it names in `calls`. A `calls` entry that names no service fails the plan.

Ordering comes from references: project, then network, then applications. Destroy runs in reverse, with no `depends_on`. The `callers` output shows the derived ingress.

## Run

```sh
export HAKOPOD_API_URL=https://hakopod.example.com
export HAKOPOD_API_KEY=...   # project administrator: the network needs one
terraform init
terraform apply -var project=shop -var environment=production
```

`traefik/whoami` is a placeholder. It runs as Hakopod's default non-root UID and listens on the port passed in `args`. Replace `image`, and keep `port` matching the port your image listens on. Services find each other by the private addresses that the network inspector (dashboard **Networks**) shows.

Destroying an application permanently retires its name in that project and environment. Removing an entry from the map does this too.
