# Terraform Provider for Hakopod

Manages [Hakopod](https://github.com/hakopod/hakopod) projects, applications and virtual networks with Terraform. It uses the same application and network TOML as the Hakopod CLI and dashboard, and the server validates and deploys it the same way.

Status: pre-1.0. Resource schemas may change between minor versions.

Resources follow Hakopod's model, project → applications → services, with virtual networks between applications:

- `hakopod_project` — a project and its environments. Environments can be added, not removed. A destroyed project's name cannot be reused.
- `hakopod_application` — an application and all its services, from TOML (`config`) or JSON (`spec`). Create and update deploy it as one revision. Services are not separate resources.
- `hakopod_virtual_network` — a virtual network and its segment grants. Needs a project administrator.

Full documentation is in [docs/](docs/index.md).

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

References order everything: project, then network, then applications; destroy runs in reverse. No `depends_on` is needed. A complete example with a project, a network and two applications (one in HCL, one in TOML) is in [examples/tree](examples/tree).

## Building locally

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

1. Create a GPG key (RSA or DSA; the Registry does not accept ECC keys) and add its public key to the Registry namespace under **User Settings → Signing Keys**.
2. Add repository secrets `GPG_PRIVATE_KEY` (ASCII-armored private key) and `PASSPHRASE`.
3. Tag and push:

   ```sh
   git tag v0.1.0
   git push origin v0.1.0
   ```

The workflow publishes zip archives for each OS/architecture, a `SHA256SUMS` file, its signature and the registry manifest to a GitHub release, which the Registry picks up.

## License

Apache License 2.0. See [LICENSE](LICENSE).
