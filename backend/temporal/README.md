# Temporal + ZITADEL Local Development Setup

This guide describes how to run the Temporal + ZITADEL integrated setup locally using Docker Compose. This setup includes:

- ZITADEL with TLS enabled and a pre-configured machine user
- Temporal backend with a separate custom-auth frontend
- Temporal UI configured for OIDC login via ZITADEL

## Prerequisites

- [Docker](https://www.docker.com/products/docker-desktop) installed
- [Docker Compose](https://docs.docker.com/compose/install/) installed

## Steps to Run

1. **Run Local Setup Script**

Before starting the containers, run the `local-setup` script to initialize ZITADEL organization, project, and tenants:

```bash
task local-setup
```

This will:

- Create a ZITADEL org, project, and machine user
- Generate a local key file for Temporal auth
- Populate the `.env` file with required variables

At the end of the script execution, you will see a message like:

```
>>> Login at https://auth.local.pyck.cloud:8080 with username 'zitadel-admin@zitadel.auth.local.pyck.cloud' and password 'Password1!'
```

2. **Login to ZITADEL and Assign Roles**

Open your browser and go to:

```
https://auth.local.pyck.cloud:8080
```

Login with the credentials provided in the script output. Then:

- Navigate to **ZITADEL organizations** → **Projects** → **PYCK** → **Grants**
- Select the `local-dev` project member
- Add the following roles:
  - `temporal_reader`
  - `temporal_writer`

3. **Create Zitadel `temporal_auth` project**

- Navigate to **ZITADEL organizations** → **local-dev** → **Projects** → **New Project**
- Name it `temporal_auth`
- On the project creation page, enable the following checkboxes:
  - Assert Roles on Authentication
  - Check Authorization on Authentication
  - Check for Project on Authentication
- Click **Save**

Then:

- Click **New Application** → choose **Web**
- Name the application `temporal_auth_app`
- In the **Code** tab, fill in the following:
  - **Redirect URI**: value of `TEMPORAL_AUTH_CALLBACK_URL` from `docker-compose.yml`
  - **Post Logout Redirect URI**: value of `TEMPORAL_UI_BASE_URL` from `docker-compose.yml`
- Save the application
- Copy the **Client ID** and **Client Secret** and update them in `docker-compose.yml` under `temporal-ui-auth` service:
  - `TEMPORAL_AUTH_CLIENT_ID`
  - `TEMPORAL_AUTH_CLIENT_SECRET`

4. **Configure Token Settings**

- Go to the **Token Settings** tab in the `temporal_auth` project
- Set **Auth Token Type** to `JWT`
- Enable all checkboxes on the page

5. **Create a User for Login**

- In ZITADEL, go to **Users** → **Add User**
- Create a user account and assign the role `temporal_writer` to the user

6. **Build Custom Services**

```bash
docker compose build
```

This builds `temporal-auth` and `temporal-ui-auth` services.

7. **Start the Stack**

```bash
docker compose up -d
```

8. **Access the Services**

- **ZITADEL Admin Console**: [https://auth.local.pyck.cloud:8080](https://auth.local.pyck.cloud:8080)
- **Temporal UI (OIDC)**: [http://localhost:8083](http://localhost:8083)

9. **Run Tests**

Before running tests, ensure that your `.env` file contains the following variables:

```
TEMPORAL_TEST_AUTH_TOKEN=<token-for-tests>
TEMPORAL_TEST_NAMESPACE=<namespace-used-in-tests>
TEMPORAL_TEST_AUTH_ADDRESS=<temporal_address>
```

Then run the tests using:

```bash
task test
```



10. **Public APIs (no token required)**

These APIs are always allowed:

- `/grpc.health.v1.Health/Check`
- `/temporal.api.workflowservice.v1.WorkflowService/GetSystemInfo`

---

11. **APIs allowed with any valid JWT that includes at least one namespace claim**

- `/temporal.api.workflowservice.v1.WorkflowService/GetClusterInfo`
- `/temporal.api.workflowservice.v1.WorkflowService/ListNamespaces`
- `/temporal.api.operatorservice.v1.OperatorService/ListNexusEndpoints`

---

12. **Namespace-based authorization**

All other APIs require:

- A valid JWT with `claims.Namespaces`
- A matching namespace (from `target.Namespace` or `GetNamespace()`)
- A role of `READER` or `WRITER` for that namespace

Access is denied if any condition is missing.

13. **Explicitly denied**

These APIs are always denied, regardless of role:

- `DeleteNamespace`
- `UpdateNamespace`

---

## Roles

```go
ROLE_TEMPORAL_READER
ROLE_TEMPORAL_WRITER
