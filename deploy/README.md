# Deploy Configuration

Each `.env` file in this directory defines a deployment target. The CI pipeline scans these files to determine where to deploy.

## How It Works

Every `.env` file has a `DEPLOY_ON` key that maps a trigger to an environment:

| Trigger         | Value           | Fires When                    |
|-----------------|-----------------|-------------------------------|
| `push_main`     | Push to `main`  | Code merged into main branch  |
| `pre_release`   | Pre-release     | GitHub pre-release published  |
| `release`       | Full release    | GitHub release published      |
| `feature_branch`| Feature branch  | Any push to a non-main branch |

## Feature Branches

Every push to a non-main branch creates an ephemeral feature environment. Closing the PR triggers automatic cleanup.

## Adding a New Environment

Create a new file, e.g. `deploy/staging.env`:

```
DEPLOY_ON=release
```

No workflow changes needed — the resolve job picks it up automatically.

## Current Environments

| File          | Trigger           | Deploys To |
|---------------|-------------------|------------|
| `dev.env`     | `push_main`       | dev        |
| `test.env`    | `pre_release`     | test       |
| `feature.env` | `feature_branch`  | ephemeral  |
