# Federation gateway

<!-- toc
## Contents

- [Layout](#layout)
- [Regenerate the supergraph](#regenerate-the-supergraph)
- [Run](#run)
- [Add a subgraph](#add-a-subgraph)
-->

Apollo Router that serves the services' GraphQL subgraphs as one federated endpoint on port 4000.

## Layout

| Path | Purpose |
| --- | --- |
| `supergraph.graphql` | Composed supergraph, committed. The image bakes in a fresh one at build time. |
| `supergraph.yaml` | Rover composition config (federation version, subgraph `routing_url`s). Written by `compose-supergraph.sh`. |
| `compose-supergraph.sh` | Collects each service's `*.graphql` files and runs `rover supergraph compose`. `--help` lists its options. |
| `router-compose.yml` | Router config used in the container: subgraph URLs on the compose network, health check at `/health`. |
| `router-dev.yml` | Router config for a router started by hand next to services on the host. |
| `router.yml` | Unreferenced variant; it overrides a subgraph named `items`, which the supergraph does not contain. |
| `Dockerfile` | Runs `task generate` in a Rover builder stage, then starts `router -c router-compose.yml`. |

The per-service `<name>.graphql` files the script writes are gitignored.

## Regenerate the supergraph

```sh
task generate   # in backend/gateway/: recompose supergraph.graphql from the services' schemas
```

The root `task generate` runs this too. Commit the changed `supergraph.graphql`. Needs `rover` on the `PATH`.

## Run

The compose service `gateway` (container `pyck-gateway`, definition in `config/compose/third-party/gateway.yaml`) builds the image from the `Dockerfile` and listens on port 4000. From the repository root:

```sh
docker compose up --build --wait gateway   # or `task up` for the whole stack
```

No task or `Dockerfile` references `router-dev.yml` or `router.yml`. To run a router outside compose, pass one with `router -c <file> -s supergraph.graphql`; the subgraph URLs then come from `routing_url` in `supergraph.yaml` (`http://localhost:<port>/query`).

## Add a subgraph

1. Give the service a `backend/<service>/graph/` directory with its `*.graphql` files. `compose-supergraph.sh` finds every `backend/*/graph` directory on its own and adds the subgraph to `supergraph.yaml`.
2. Check `get_node_suffix` and `get_service_port` in `compose-supergraph.sh`. A service not listed there gets a capitalised service name as `Node` suffix and a port derived from a hash of its name; add it to both functions to pin them.
3. Add the service to `override_subgraph_url` in `router-compose.yml` with its compose host name and port.
4. Add the service's compose file under `config/compose/backend/` to the `include` list in the root `docker-compose.yml`, and its name to `UP_SERVICES` in the root `Taskfile.yaml` so `task up` starts it.
5. Run `task generate` and commit the new `supergraph.graphql`.
