# Azure Public Heimdal infrastructure example

Read the [bootstrap design](../../../docs/heimdal-bootstrap.md) before deploying.
This is a reference configuration, not output from an implemented
`bivrost heimdal bootstrap` command. No metadata is published by these files.

## Prerequisites

- Existing resource group, private endpoint subnet and Blob private DNS zone in
  the metadata subscription. The subnet VNet must be in the selected region.
- Working DNS links/forwarding and routes for the publisher and consumer path.
- Existing consumer groups, separately governed publisher groups and CI principals.
- Explicit Azure tenant/subscription and an appropriately authorized deployment identity.
- Pre-registered resource providers and an independent, protected IaC state backend.

The account is new and dedicated to metadata. Resource group, network, DNS zone,
groups and CI federation remain externally managed. All three principal sets
are explicit; use `[]` where no grant of that kind is desired. IDs are object IDs,
not application/client IDs. Human publisher PIM is a prerequisite, not something
this example configures.

## Validate without deploying

Use a disposable copy of this directory. These commands initialize providers
and validate configuration without an Azure plan or apply:

```sh
tofu init -backend=false
tofu fmt -check
tofu validate
```

The equivalent `terraform` commands can be used in a separate copy. Do not mix
engines against one deployment's state. Review and commit the generated provider
lock file downstream; the example itself supplies provider version constraints.

## Prepare an adopter-owned deployment

Copy into your private infrastructure repository, add your reviewed remote backend,
then create `terraform.tfvars` from `terraform.tfvars.example` and replace its
fictional values. Do not apply with the implicit local backend. Authenticate via
your approved Azure CLI/workload identity process, without credentials in files
or command arguments. Review the plan through the normal infrastructure workflow.
Keep saved plans and state private.

`source_locator` is an informational output, **not** a supported user-onboarding
schema. Combine it with reviewed bootstrap connectivity only when the consumer
contract is implemented. Current Bivrost profiles can use the locator fields;
see the [current configuration documentation](../../../README.md#fetch-metadata-when-connecting-development).

## Limitations

Static validation does not verify Azure policy, effective RBAC, PIM governance,
private DNS, network routes or repeatable deployment. These require disposable
live QA. The example covers Azure Public Cloud only. It creates no diagnostic
logging destination, publishing workflow, groups or activation policies.

Private storage creation uses management-plane APIs; publishing and consumption
still require data-plane connectivity and authorization. Versioning and 14-day
soft delete are recovery defaults, not immutable retention or a publication
renewal schedule. Review retention, region and redundancy against your needs.

## Static validation recorded

Terraform 1.16.4 and OpenTofu 1.12.6 formatting and configuration validation passed with
AzAPI 2.13.0 and AzureRM 5.7.0. Each engine used provider initialization with
`-backend=false`; no live plan or apply was performed.

Account replacement/deletion is guarded by `prevent_destroy`; an intentional
teardown needs a reviewed change to that guard. Removing the resource block or
deleting the account outside IaC bypasses it. Blob recovery features do not
protect against account deletion. Where Azure Policy owns DNS zone associations,
adapt the example to avoid competing ownership.

The supplied `.gitignore` protects this public example. Downstream repositories
should choose their own policy for reviewed, non-secret input files; keep state,
plans and credentials excluded regardless.
