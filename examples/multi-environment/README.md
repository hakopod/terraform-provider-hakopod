# Multi-environment

One project (`multi-env`) with two environments, `staging` and `production`.
The module in [modules/stack](modules/stack) is instantiated once per
environment and creates, in that environment:

- a virtual network `backend` with one segment, `backend`, granted to `web` and `api`;
- a generated secret `api-token` belonging to `api`;
- `api`, an internal application that reads the secret as `API_TOKEN` and
  accepts traffic only from `web/web` on the segment;
- `web`, a public application on the same segment.

Networks, applications and secrets are per environment, so both environments
use the same names.

## Sizing

`var.environments` holds one entry per environment. Its keys become the
project's environments; its values size every service in that environment:

| Environment | Image tag | Replicas | CPU request / limit | Memory request / limit |
| --- | --- | --- | --- | --- |
| staging | `1.27-alpine` | 1 | 50m / 250m | 64Mi / 128Mi |
| production | `1.26-alpine` | 2 | 100m / 500m | 128Mi / 256Mi |

Both applications run `nginxinc/nginx-unprivileged` on port 8080 as UID 101.
Replace the image in `modules/stack/main.tf` with your own; it must run as a
non-root user. To promote a tag, change it in `production`.

Adding an environment is a new map entry. Removing one fails at plan: Hakopod
cannot delete environments.

## Secret rotation

`api` receives the secret's `value_version` as `SECRET_VERSION`. When the
secret is rotated, the version changes, the application spec changes, and the
next apply redeploys `api` with the new value.

## Apply

```shell
terraform init
terraform apply
```

## Destroy

`terraform destroy` removes the applications, then the secrets and networks,
then the project. Destroyed application names (`web`, `api`) and the project
name can never be reused.
