# From a CLI tree to Terraform

Manages the folder that `hakopod deploy --dir` deploys, with the same TOML files.
Nothing in `apps/` changes when you switch: the CLI can still validate it.

```sh
hakopod validate --dir apps                 # still works, locally
terraform apply -var project=shop           # instead of hakopod deploy --dir apps ...
```

## How applications are found

`local.apps` lists every file one folder below `apps/` and keeps those named
`hakopod.toml` in any case, so `api/HAKOPOD.toml` is found on Linux too
(`fileset` itself is case-sensitive there). The map key is the folder name:
`hakopod_application.app["api"]`. Deeper and hidden folders are ignored, as
by the CLI. A new folder becomes a new application on the next apply; a
removed folder is **destroyed** by Terraform, which the CLI never does, and a
destroyed application's name cannot be used again.

`network.toml` is optional: without it `hakopod_virtual_network.net` has no
instance.

## Ordering

The applications' TOML names the network literally
(`virtual_network = "shop"`), so Terraform sees no reference between them and
could deploy an application before its grant exists. The application resource
therefore has `depends_on = [hakopod_virtual_network.net]`. That is the order
`hakopod deploy --dir` uses (network, then applications) and, reversed, the
order destroy needs. It only orders operations; it does not force replacement
or defer plans. The alternative, a `templatefile` that injects the network's
name, would mean editing the TOML, and the CLI could no longer read it.

## Adopting what the CLI already deployed

The project must already exist; this example does not manage it. Applications
and the network deployed by the CLI also exist already, and the provider does
not take over an application that has had a successful release: import them
first. The import ID is `project/environment/name`, where `name` is the `name`
in the TOML; in this example it equals the folder name that keys `for_each`.

```sh
terraform init
terraform import 'hakopod_virtual_network.net[0]' shop/production/shop
terraform import 'hakopod_application.app["api"]' shop/production/api
terraform import 'hakopod_application.app["web"]' shop/production/web
terraform import 'hakopod_application.app["worker"]' shop/production/worker
terraform plan -var project=shop
```

The first plan after import shows `pending_changes` for any difference
between the deployed revision and the files; with the same files the CLI last
deployed, it should be zero.

## Differences from the CLI

- Applications deploy in parallel, not one at a time in folder-name order.
- `env_file` in the TOML needs the file contents in `env_files`; none of the
  sample applications use it. Add `env_files` per application if yours do.
- Network changes need a project administrator key. A CI machine key, which
  the CLI uses with `--no-network`, cannot apply this configuration while it
  manages `network.toml`.
