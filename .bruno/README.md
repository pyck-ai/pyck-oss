# Bruno API Collections

This directory contains [Bruno](https://www.usebruno.com/) API collections for testing the pyck GraphQL APIs.

## Auto-Generated Content

**⚠️ These files are auto-generated via `brunogen`**

The Bruno collections in this directory are automatically generated based on the GraphQL schemas defined in each service's `api/graph` directory.

### Generation Process

- **Source**: GraphQL schema files in `backend/<service>/api/graph/*.graphql`
- **Tool**: `brunogen` (custom code generator)
- **Output**: Bruno collection files (`.bru`) organized by service

### Usage

1. Install [Bruno](https://www.usebruno.com/) desktop application
2. Open this directory as a collection in Bruno
3. Bring the stack up (`task up`), then run `task bruno:setup`
4. Select the **local** environment and run "Zitadel Login" with the
   credentials the setup printed
5. Test GraphQL queries and mutations against your local services

For headless runs, `task bruno:run` drives the same environment, setting it
up first, so a CI-style run and the GUI always act on the same tenant. Pass
a path to narrow it: `task bruno:run -- tests/inventory`. The underlying
`scripts/bruno-run.sh` provisions nothing and tells you which task to run if
the environment is missing.

`task bruno:setup` exists because there is no account to borrow. A token is
served only while a live tenant row backs it (see
[tenant-access-control.md](../docs/tenant-access-control.md)), the bootstrap
organization is deliberately not a tenant, and a tenant only comes into
existence through the `registerTenant` mutation. The task renders
`scripts/bruno-template.yaml` into `environments/local.yml`, filling
in the tenant, a PAT minted inside it, the OIDC client id and the system
token. The generated file is gitignored, since it carries credentials, and
the template lives outside this directory on purpose: an environment with no
client id and no token is one you can select by mistake, and the OAuth step
then fails with `Errors.App.NotFound`, which names neither.

The template quotes its client id, and so does the rendered file. Zitadel ids
are snowflakes past 2^53, so an unquoted one is read as a YAML number, lands
in a float64 and comes back with its last digits changed: the same
`Errors.App.NotFound`, from a value that looks right at a glance.

Two things about the credentials it gives you:

- The PAT in `env_pyck_token` (which the collection's auth hook prefers,
  so it is what every request actually sends) holds `admin` plus every
  per-service gate; the OAuth login holds `admin`. `admin` implies
  `writer` and `reader` on every consumer of the ladder, so that covers
  the bulk of the collection including the admin-gated `assignRoles`,
  `removeRoles`, `userServiceRoles` and `deleteTenant`. On a reused
  tenant the setup widens the tenant org's project grant (which caps what
  any of its users may hold) and the `bruno-runner` machine user's own
  authorization in place (that user is reused by name across runs, so one
  created writer-only under an older build would otherwise keep its
  roles). It does not widen the `bruno` human admin: a `bruno-local`
  tenant registered before the platform delegated `admin` keeps a
  writer-only login, so recreate the stack (`docker compose down -v &&
  task up`) if the OAuth login fails `assignRoles` with "admin role
  required".
- Holding `admin` is still not enough for `restoreTenant`,
  `setTenantExpiry` or the user-projection mutations (`createUser`,
  `updateUser`, `deleteUser`): those require a system caller. They are platform
  operations: run them with `env_pyck_system_token`, which the generated
  environment also carries.

### Regeneration

To regenerate the Bruno collections after schema changes:

```bash
task generate  # Regenerates all code including Bruno collections
```

**Do not manually edit** the `.bru` files in this directory - changes will be overwritten on the next generation. Instead, copy the collection to keep your modifications local-only.
