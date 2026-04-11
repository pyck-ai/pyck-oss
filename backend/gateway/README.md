# Federation gateway

### Generating the supergraph

To generate the **supergraph** needed by the router, the various **.graphql** files from the different microservices
will be copied to the **composition/** folder. You can achieve this with 
```
task introspect
```

### Build and run locally
Install [rover CLI](https://www.apollographql.com/docs/rover/) before. See **router-dev.yml** for the router configuration.

```
TARGET=dev task supergraph
TARGET=dev task run
```

### Build and run in docker-compose
Docker images will be build with a supergraph based on the current code. See **router-compose.yml** for the router configuration.
```
docker-compose build gateway
docker compose up gateway
```

### Adding new subgraphs

1) Add the copy commands for the needed **.graphql** files from the new subgraph to the **task introspection** command in the **Makefile**
2) Add the subgraph to **composite/supergraph.yml** file.
3) Overwrite when necessary the subgraph-url in the **router.yml** or **router-\*.yml** files.
4) **TARGET=dev task supergraph** to test.
