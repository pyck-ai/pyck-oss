# File service

This service manages metadata about stored file on a s3-compatible storage.

### Env variables
```
PYCK_DATABASE_MASTER_URL
PYCK_DATABASE_SLAVE_URL
PYCK_DATABASE_DEBUG -> default value: false
PYCK_DATABASE_DRIVER -> default value: "postgres"
PYCK_GATEWAY_URL
PYCK_ZITADEL_AUDIENCE
PYCK_ZITADEL_ORG_ID
PYCK_ZITADEL_PROJECT_ID
PYCK_ZITADEL_APP_KEYFILE
PYCK_ZITADEL_TLS_INSECURE
PYCK_NATS_URL
PYCK_NATS_STREAM_NAME
PYCK_NATS_WS_URL
PYCK_NATS_REPLICAS_NO
PYCK_AWS_S3_BUCKET
PYCK_AWS_S3_REGION
PYCK_AWS_S3_ENDPOINT_URL
PYCK_AWS_S3_HTTP_ENDPOINT_URL
PYCK_AWS_ACCESS_KEY_ID
PYCK_AWS_SECRET_ACCESS_KEY
PYCK_SERVICE_TOKEN
PYCK_TX_RETRIES -> default value: 50
```

## Usage

First create a file in the database:

```graphql
mutation {
    createFile(
      input: {
        refid: "01919dd0-e3d5-7432-b24f-51fab27c631d"
        reftype: supplier
        filename: "test-supllier.png"
         description: "My Logo"
        size: 2216436
        contentType: "image/png"
        dataTypeID:"0191e5a9-5b97-7135-9bc1-80090f4fdf65",
        data: {
          name: "myName"
        }
      }) {
        id
        preSignedUploadUrl
        file {
          id
          filename
          createdAt
          createdBy
        }
    }
}
```

You'll get a response with a **preSignedUploadUrl**:

```json
{
  "data": {
    "createFile": {
      "id": "0191ea6f-7dd5-7479-b1af-6fee62d0154c",
      "preSignedUploadUrl": "http://localhost:9000/pyck-local-dev/d4b5319e-1daa-57ed-9676-c6bfc717cf76/01919dd0-e3d5-7432-b24f-51fab27c631d/test-supllier.png?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=pyck%2F20240913%2Fus-west-2%2Fs3%2Faws4_request&X-Amz-Date=20240913T081100Z&X-Amz-Expires=900&X-Amz-SignedHeaders=content-type%3Bhost&X-Amz-Signature=ed6954d2dc20c703ef0ac2f66526cfb2fcad889cd58fb0d26046d55461129997",
      "file": {
        "id": "0191ea6f-7dd5-7479-b1af-6fee62d0154c",
        "filename": "test-supllier.png",
        "createdAt": "2024-09-13T08:11:00.949273306Z",
        "createdBy": "8db3d8c4-affa-53f4-b542-7bfefee2a730"
      }
    }
  }
}
```

You can now use the **preSignedUploadUrl** to upload a file to a s3-compatibe store:

```sh
curl --request PUT -H "Content-Type: image/png" --upload-file test-supplier.png \
"http://localhost:9000/pyck-local-dev/d4b5319e-1daa-57ed-9676-c6bfc717cf76/01919dd0-e3d5-7432-b24f-51fab27c631d/test-supllier.png?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=pyck%2F20240913%2Fus-west-2%2Fs3%2Faws4_request&X-Amz-Date=20240913T081100Z&X-Amz-Expires=900&X-Amz-SignedHeaders=content-type%3Bhost&X-Amz-Signature=ed6954d2dc20c703ef0ac2f66526cfb2fcad889cd58fb0d26046d55461129997"
```

IMPORTANT: You have to provide the **Content-Type** when uploading a file; otherwise, they will be stored in S3 with the default MIME-type **binary/octet-stream**.

## Federation

For example, for suppliers:

```graphql
query Supplier {
  suppliers(
    first:2
) {
    totalCount
    edges {
      node {
        id
        data
        file {
          id
          filename
          size
          contentType
          url
        }
      }
    }
    pageInfo {
      hasNextPage
      hasPreviousPage
      startCursor
      endCursor
    }
  }
}
```

## Image Analyzer

Inputs:
```
Example Image: https://marketplace.canva.com/EAETpJ0lmjg/2/0/1131w/canva-fashion-invoice-zvoLwRH8Wys.jpg
Example DataType: {
  "$schema": "http://json-schema.org/draft-07/schema#",
  "$id": "http://json-schema.org/draft-07/schema#",
  "title": "Invoice Schema",
  "description": "A schema representing an invoice with details about the issuing and receiving companies, invoice summary, and items included in the invoice.",
  "type": "object",
  "properties": {
    "invoiceNumber": {
      "type": "string"
    },
    "issueDate": {
      "type": "string",
      "format": "date"
    },
    "dueDate": {
      "type": "string",
      "format": "date"
    },
    "summary": {
      "type": "object",
      "properties": {
        "currency": {
          "type": "string"
        },
        "subtotal": {
          "type": "number"
        },
        "tax": {
          "type": "number"
        },
        "total": {
          "type": "number"
        }
      },
      "required": [
        "currency",
        "subtotal",
        "tax",
        "total"
      ]
    },
    "companyFrom": {
      "type": "object",
      "properties": {
        "name": {
          "type": "string"
        },
        "address": {
          "type": "string"
        },
        "city": {
          "type": "string"
        },
        "postalCode": {
          "type": "string"
        },
        "country": {
          "type": "string"
        },
        "email": {
          "type": "string",
          "format": "email"
        },
        "phoneNumber": {
          "type": "string"
        },
        "bankAccount": {
          "type": "string"
        }
      },
      "required": [
        "name",
        "address",
        "city",
        "postalCode",
        "country",
        "email",
        "phoneNumber",
        "bankAccount"
      ]
    },
    "companyTo": {
      "type": "object",
      "properties": {
        "name": {
          "type": "string"
        },
        "address": {
          "type": "string"
        },
        "city": {
          "type": "string"
        },
        "postalCode": {
          "type": "string"
        },
        "country": {
          "type": "string"
        },
        "email": {
          "type": "string",
          "format": "email"
        },
        "phoneNumber": {
          "type": "string"
        },
        "bankAccount": {
          "type": "string"
        }
      },
      "required": [
        "name",
        "address",
        "city",
        "postalCode",
        "country",
        "email",
        "phoneNumber",
        "bankAccount"
      ]
    },
    "invoiceItems": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "name": {
            "type": "string"
          },
          "quantity": {
            "type": "integer"
          },
          "unitPrice": {
            "type": "number"
          },
          "subTotal": {
            "type": "number"
          }
        },
        "required": [
          "name",
          "quantity",
          "unitPrice",
          "subTotal"
        ]
      }
    }
  },
  "required": [
    "invoiceNumber",
    "issueDate",
    "dueDate",
    "summary",
    "companyFrom",
    "companyTo",
    "invoiceItems"
  ]
}
```

Mutation:
```
mutation AnalyzeImageFile {
    analyzeImageFile(id: "file_id") {
        jsonData
    }
}
```

Response:
```
{
    "data": {
        "analyzeImageFile": {
            "jsonData": "{\n  \"companyFrom\": {\n    \"name\": \"Samira Hadid\",\n    \"address\": \"123 Anywhere St., Any City, ST 12345\",\n    \"email\": \"\",\n    \"phoneNumber\": \"\",\n    \"country\": \"\",\n    \"city\": \"\",\n    \"postalCode\": \"\",\n    \"bankAccount\": \"123-456-7890\"\n  },\n  \"companyTo\": {\n    \"name\": \"Imani Olowe\",\n    \"address\": \"63 Ivy Road, Hawkville, GA, USA 31036\",\n    \"email\": \"\",\n    \"phoneNumber\": \"+123-456-7890\",\n    \"country\": \"USA\",\n    \"city\": \"Hawkville\",\n    \"postalCode\": \"31036\",\n    \"bankAccount\": \"\"\n  },\n  \"invoiceNumber\": \"12345\",\n  \"issueDate\": \"2025-06-16\",\n  \"dueDate\": \"2025-07-05\",\n  \"invoiceItems\": [\n    {\n      \"name\": \"Eggshell Camisole Top\",\n      \"quantity\": 1,\n      \"unitPrice\": 123,\n      \"subTotal\": 123\n    },\n    {\n      \"name\": \"Cuban Collar Shirt\",\n      \"quantity\": 2,\n      \"unitPrice\": 127,\n      \"subTotal\": 254\n    },\n    {\n      \"name\": \"Floral Cotton Dress\",\n      \"quantity\": 1,\n      \"unitPrice\": 123,\n      \"subTotal\": 123\n    }\n  ],\n  \"summary\": {\n    \"subtotal\": 500,\n    \"tax\": 0,\n    \"total\": 500,\n    \"currency\": \"USD\"\n  }\n}"
        }
    }
}
```

Result json:
```
{
    "companyFrom": {
        "name": "Samira Hadid",
        "address": "123 Anywhere St., Any City, ST 12345",
        "email": "",
        "phoneNumber": "",
        "country": "",
        "city": "",
        "postalCode": "",
        "bankAccount": "123-456-7890"
    },
    "companyTo": {
        "name": "Imani Olowe",
        "address": "63 Ivy Road, Hawkville, GA, USA 31036",
        "email": "",
        "phoneNumber": "+123-456-7890",
        "country": "USA",
        "city": "Hawkville",
        "postalCode": "31036",
        "bankAccount": ""
    },
    "invoiceNumber": "12345",
    "issueDate": "2025-06-16",
    "dueDate": "2025-07-05",
    "invoiceItems": [
        {
            "name": "Eggshell Camisole Top",
            "quantity": 1,
            "unitPrice": 123,
            "subTotal": 123
        },
        {
            "name": "Cuban Collar Shirt",
            "quantity": 2,
            "unitPrice": 127,
            "subTotal": 254
        },
        {
            "name": "Floral Cotton Dress",
            "quantity": 1,
            "unitPrice": 123,
            "subTotal": 123
        }
    ],
    "summary": {
        "subtotal": 500,
        "tax": 0,
        "total": 500,
        "currency": "USD"
    }
}
```