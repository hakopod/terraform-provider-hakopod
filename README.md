# Terraform Provider for Hakopod

Manages [Hakopod](https://github.com/hakopod/hakopod) projects, applications and virtual networks with Terraform. It uses the same application and network TOML as the Hakopod CLI and dashboard, and the server validates and deploys it the same way.

Status: pre-1.0. Resource schemas may change between minor versions.

Resources follow Hakopod's model, project → applications → services, with virtual networks between applications:

- `hakopod_project` — a project and its environments. Environments can be added, not removed. A destroyed project's name cannot be reused.
- `hakopod_application` — an application and all its services, from TOML (`config`) or JSON (`spec`). Create and update deploy it as one revision. Services are not separate resources.
- `hakopod_virtual_network` — a virtual network and its segment grants. Needs a project administrator.
- `hakopod_secret` — a project, environment or application secret, either a literal value or one the server generates. Values are write-only; `value_version` changes on rotation, so an application that reads the secret can redeploy.

Full documentation is on the [Terraform Registry](https://registry.terraform.io/providers/hakopod/hakopod/latest/docs), and in [docs/](docs/index.md) in this repository.

## Install

The provider is published on the [Terraform Registry](https://registry.terraform.io/providers/hakopod/hakopod/latest) as `hakopod/hakopod`. Pin it in your configuration:

```terraform
terraform {
  required_providers {
    hakopod = {
      source  = "hakopod/hakopod"
      version = "~> 0.1"
    }
  }
}
```

Then download it:

```sh
terraform init
```

The provider reads two environment variables, the same ones the Hakopod CLI uses, so a shell configured for the CLI needs no `provider` block:

- `HAKOPOD_API_URL` — the Hakopod API URL, or the provider's `url` attribute.
- `HAKOPOD_API_KEY` — a Hakopod API key, or the provider's `api_key` attribute.

Hakopod Cloud also reads `HAKOPOD_WORKSPACE` (or `workspace`); leave it unset for self-hosted installations.

## Example

```terraform
provider "hakopod" {} # reads HAKOPOD_API_URL and HAKOPOD_API_KEY

resource "hakopod_project" "commerce" {
  name         = "commerce"
  environments = ["production"]
}

resource "hakopod_virtual_network" "commerce" {
  project     = hakopod_project.commerce.name
  environment = hakopod_project.commerce.environments[0]
  config      = file("network.toml")
}

resource "hakopod_application" "catalog" {
  project     = hakopod_project.commerce.name
  environment = hakopod_project.commerce.environments[0]
  config = templatefile("catalog/hakopod.toml.tftpl", {
    network = hakopod_virtual_network.commerce.name # orders catalog after the network
  })
}
```

References order everything: project, then network, then applications; destroy runs in reverse. No `depends_on` is needed.

## Examples

| Example | What it shows |
| --- | --- |
| [tree](examples/tree) | A project, a network and two applications — one in HCL, one in TOML. |
| [static-site](examples/static-site) | The smallest useful configuration: one public application, no network. |
| [full-stack](examples/full-stack) | web, api, worker, scheduler, PostgreSQL and Redis on one network with three segments, generated secrets and persistent volumes. |
| [microservices](examples/microservices) | Five services generated from one map, with ingress derived from who calls whom. |
| [multi-environment](examples/multi-environment) | One module instantiated per environment, sized differently in each. |
| [from-cli-tree](examples/from-cli-tree) | A directory tree of `hakopod.toml` files deployed as-is, matching `hakopod deploy --dir`. |

## Developing the provider

Use the registry release above to *use* the provider. This section is for working on the provider's own source, where Terraform has to run a locally built binary instead of a published one.

Requires Go (version in `go.mod`) and Terraform 1.x.

```sh
go install .
```

This installs `terraform-provider-hakopod` into `$(go env GOPATH)/bin`. Point Terraform at it with `~/.terraformrc`:

```hcl
provider_installation {
  dev_overrides {
    "hakopod/hakopod" = "/Users/you/go/bin" # output of: go env GOPATH, plus /bin
  }
  direct {}
}
```

With `dev_overrides`, skip `terraform init` and run `terraform plan` directly. Terraform prints a warning that overrides are active.

Tests:

```sh
go test ./...
```

## Releasing

Releases are built by GoReleaser from `.github/workflows/release.yml` when a `v*` tag is pushed. The Terraform Registry requires the checksum file to be signed.

The signing key and the repository secrets are already in place; publishing a new version is only the tag:

```sh
git tag v0.2.0
git push origin v0.2.0
```

To set this up again from scratch, or to rotate the key:

1. Create a GPG key (RSA or DSA; the Registry does not accept ECC keys) and add its public key to the Registry namespace under **Public namespaces → hakopod → Settings → GPG Keys**.
2. Add repository secrets `GPG_PRIVATE_KEY` (ASCII-armored private key) and `PASSPHRASE`.
3. Tag and push as above.

The workflow publishes zip archives for each OS/architecture, a `SHA256SUMS` file, its signature and the registry manifest to a GitHub release, which the Registry picks up.

## License

Apache License 2.0. See [LICENSE](LICENSE).
