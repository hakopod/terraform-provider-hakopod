# Static site

One project (`static-site`, environment `production`) and one application
(`site`) with a single public web service: two replicas, the `small` size
profile and an HTTP readiness check on `/`.

The default image, `nginxinc/nginx-unprivileged:1.27-alpine`, serves the nginx
welcome page on port 8080 as UID 101. To serve your own site, build an image
that runs as a non-root user and set:

```hcl
image       = "ghcr.io/your-team/site:1.0"
port        = 8080
run_as_user = 10001 # your image's non-root UID
```

## Apply

```shell
terraform init
terraform apply
```

The service gets a generated public hostname. Find it in the dashboard.

## Custom domain

1. Apply once without a domain so the application exists.
2. Add and verify the hostname under Custom domains in the Hakopod dashboard.
   An unverified hostname does not route.
3. Apply again with the domain:

```shell
terraform apply -var domain=www.example.com
```

Hostnames are lowercase DNS names without wildcards, schemes, paths or ports.

## Destroy

`terraform destroy` removes the application and the project. A destroyed
application's name (`site`) can never be reused in that project and environment.
