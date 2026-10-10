---
page_title: "hakopod_clickhouse_access_profile Resource - Hakopod"
description: |-
  Enable tenant administration on an existing standalone ClickHouse database.
---

# hakopod_clickhouse_access_profile

Enable `tenant_admin` on an existing managed ClickHouse database. Requires Hakopod `v0.1.0-alpha.59` or later with this API contract.

```hcl
resource "hakopod_clickhouse_access_profile" "analytics" {
  database_id                 = "your-existing-database-id"
  access_profile              = "tenant_admin"
  allow_tenant_administration = true
}
```

The application login can manage SQL users and quotas. It can delegate `SELECT` on `app.*`.
Backups exclude tenant users, quotas and row policies. Provision those entities again after a restore.

This resource changes privileges only. It never creates a database or changes its capacity.
Only standalone ClickHouse supports this profile. The provider requires a healthy, current revision before it confirms activation.

## Apply and recovery

1. The provider reads the current spec and requests a server review.
2. It rejects blocked, expired or changed reviews. It preserves every unrelated spec field.
3. It submits the reviewed revision once and waits up to ten minutes.
4. It verifies the completed operation and a healthy observation of the same revision.

An error after submission can mean the server accepted the operation. Inspect the managed database operations before retrying.
If activation completed, import the profile or refresh existing state. The provider does not repeat an unconfirmed POST automatically.

A successful Terraform apply confirms activation at that time. Later refreshes report the current profile and revision, without certifying runtime health.

## Removal

Removing this resource only removes Terraform management. The database, data and tenant privileges remain.
Hakopod cannot disable `tenant_admin` in place. Migrate to a separate database to remove that capability.

Changing `database_id` replaces this resource. The previous database keeps its privileges.

## Arguments

| Name | Required | Meaning |
| --- | --- | --- |
| `database_id` | Yes | Existing standalone ClickHouse database ID. |
| `access_profile` | Yes | Must be `tenant_admin`. |
| `allow_tenant_administration` | Yes | Must be `true` to permit the privilege change. |

Computed attributes: `id` is the database ID. `revision` is its last observed revision.
The configured API identity must have permission to manage the database. The provider never retrieves database credentials.

## Import

```sh
terraform import hakopod_clickhouse_access_profile.analytics your-existing-database-id
```

Import reads the current profile. It does not activate tenant administration or grant consent.
Review the configuration before applying it. An imported `application` profile will show an activation change when configured as `tenant_admin`.
