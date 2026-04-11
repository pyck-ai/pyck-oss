# Master Prototype

### Env variables
```
PYCK_DATABASE_MASTER_URL
PYCK_DATABASE_SLAVE_URL
PYCK_DATABASE_DEBUG -> default value: false
PYCK_DATABASE_DRIVER -> default value: "postgres"
PYCK_GATEWAY_URL
PYCK_SERVICES_PATH -> default value: ""
PYCK_ZITADEL_AUDIENCE
PYCK_ZITADEL_ORG_ID
PYCK_ZITADEL_PROJECT_ID
PYCK_ZITADEL_APP_KEYFILE
PYCK_ZITADEL_TLS_INSECURE
PYCK_NATS_URL
PYCK_NATS_STREAM_NAME
PYCK_NATS_WS_URL
PYCK_NATS_REPLICAS_NO
PYCK_SERVICE_TOKEN
PYCK_TX_RETRIES -> default value: 50
```

## Run

```
docker-compose up -d db_1
docker-compose up main-data   # build docker
```

* http://localhost:8080  - CockroachDB UI
* http://localhost:8081  - GraphQL

## GraphQL

This [tutorial](https://entgo.io/docs/tutorial-todo-gql) was followed, with specific modifications for pyck.

### Describe schemas on the command line

```
go tool entgo.io/ent/cmd/entc  describe ./ent/schema
```

### JWT-Token header usable for requests

Use [JWT-Debugger](https://jwt.io/) to generate tokens for development.

Current payload looks like this:
```
{
  "tenant_id": "b98b88eb-ce77-4e9a-a224-d37443a9c5c1",
  "user_id": "753aef02-ea82-416c-8b9d-4e4794037ec8",
  "role": "writer"
}
```

Generate tokens by modifying the payload for your needs. JWT-Secret is **PickyTest** (base64 **UGlja2x5VGVzdA==**)

There are three preliminary roles:

* reader
* writer
* admin

### list items
```
query AllItems {
    items {
        sku
    }
}
```

### create items
```
mutation CreateItem {
    createItem(input: {
      sku: "MK-ENT-D1",
      gtin: "9099998001015",
      customData: {
        type: "custom",
        sum: 5,
        meta: {
          name: "Testitem",
          weight: 100,
          tags: ["test", "foo"]
        }
      }
    }) {
        id
        sku
        gtin
        customData
    }
}

mutation CreateItem {
    createItem(input: {
      sku: "MK-ENT-X2",
      gtin: "9099998011015",
      customData: {
        type: "custom",
        sum: 15,
        meta: {
          name: "Testitem2",
          weight: 25,
          tags: ["testtag", "foobar"]
        }
      }
    }) {
        id
        sku
        gtin
        customData
    }
}
```


### query by id

```
{
  node(id: "01130819-5b37-4dd4-aa73-ccd4e0e91939") {
    id
    ... on Item {
      id
      sku
      customData
    }
  }
}
```

### query by ids

```
{
  nodes(ids: ["01130819-5b37-4dd4-aa73-ccd4e0e91939", "51b6c337-bcda-48a6-a56b-e5c768ed981b"]) {
    id
    ... on Item {
      id
      sku
      customData
    }
  }
}
```

### query by filters
 See [ent.graphql](graph/ent.graphql) for available filters in **ItemWhereInput** for normal db fields, \
 have a look in [item.graphql](graph/item.graphql) for JSON specific filters.
```
query QueryItemsWithFilter {
  items(first:200,
    after null,
    where: {
      skuIn: ["MK-ENT-D1","MK-ENT-X2"],
      Data: ["type", "custom"],
      DataHasKey: "meta",
      DataIn: ["meta.weight", "25", "50", "100"],
      or: [
        {DataContains: ["meta.tags", "foo"]},
        {DataContains: ["meta.tags", "foobar"]},
      ]
		}
  ) {
      totalCount
      edges {
        node {
          sku
          customData
          createdAt
        }
     }
     pageInfo {
       hasPreviousPage
       startCursor
       endCursor
     }
  }
}
```
