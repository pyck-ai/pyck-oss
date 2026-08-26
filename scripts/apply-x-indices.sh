#!/usr/bin/env bash
set -euo pipefail

# Adds the x-indices block (serialNumbers -> data_ix_list1, sideSerialNumbers ->
# data_ix_list2) to the picking-order datatype, preserving the rest of its
# schema. Idempotent, and validated by the ValidateHook.
#
# Usage:
#   GATEWAY=http://localhost:4000 TOKEN=<bearer> TENANT=<uuid> \
#     scripts/apply-x-indices.sh
#
# SLUG defaults to hellmann-default-pickingorder; override to target another.

: "${GATEWAY:?set GATEWAY, e.g. http://localhost:4000}"
: "${TOKEN:?set TOKEN (bearer)}"
: "${TENANT:?set TENANT (uuid)}"
export SLUG="${SLUG:-hellmann-default-pickingorder}"

# POST a GraphQL body (stdin) and print the JSON response. --fail-with-body turns
# an HTTP error into a non-zero exit; GraphQL errors return HTTP 200 and are
# checked by the python steps below, which exit non-zero on an "errors" field.
gql() { curl -sS --fail-with-body -X POST "$GATEWAY" \
  -H "Authorization: Bearer $TOKEN" -H "X-Pyck-Tenant-Id: $TENANT" \
  -H "Content-Type: application/json" --data-binary @-; }

# Read the datatype's id and current schema.
RESP=$(printf '{"query":"query($s:String!){ dataTypes(where:{slug:$s}){ edges{ node{ id jsonSchema } } } }","variables":{"s":"%s"}}' "$SLUG" | gql)
DTID=$(echo "$RESP" | python3 -c '
import json,sys
r=json.load(sys.stdin)
if r.get("errors"): sys.exit("read failed: "+r["errors"][0]["message"])
e=r["data"]["dataTypes"]["edges"]
if not e: sys.exit("datatype not found for slug")
print(e[0]["node"]["id"])')
echo "datatype: $DTID"

# Merge in x-indices and the serial properties (only if absent), then update.
echo "$RESP" | python3 -c '
import json,sys
d=json.loads(json.load(sys.stdin)["data"]["dataTypes"]["edges"][0]["node"]["jsonSchema"])
props=d.setdefault("properties",{})
for k in ("MainItemSerialNumber","SideComponentsSerialNumbers"):
    props.setdefault(k, {"type":"array"})   # no items: numeric serials must not be rejected
# Merge rather than replace: a binding added outside this script would otherwise
# be dropped, and the frozen-binding hook then rejects the whole update over an
# index the operator never touched -- unrecoverable without hand-editing.
ix=d.setdefault("x-indices",{})
for name,binding in (
  ("serialNumbers",     {"source":"/MainItemSerialNumber","slot":"data_ix_list1"}),
  ("sideSerialNumbers", {"source":"/SideComponentsSerialNumbers","slot":"data_ix_list2"}),
):
    have=ix.get(name)
    if have is not None and have != binding:
        sys.exit("refusing to rebind %s: %r is already bound to %r" % (name, name, have))
    ix[name]=binding
print(json.dumps({"query":"mutation($id:ID!,$in:UpdateDataTypeInput!){ updateDataType(id:$id,input:$in){ id } }",
  "variables":{"id":sys.argv[1],"in":{"jsonSchema":json.dumps(d)}}}))' "$DTID" | gql \
  | python3 -c '
import json,sys
r=json.load(sys.stdin)
if r.get("errors"): sys.exit("update rejected: "+r["errors"][0]["message"])
print("  x-indices applied")'

# Verify.
printf '{"query":"query($s:String!){ dataTypes(where:{slug:$s}){ edges{ node{ jsonSchema } } } }","variables":{"s":"%s"}}' "$SLUG" | gql \
  | python3 -c '
import json,sys
r=json.load(sys.stdin)
if r.get("errors"): sys.exit("verify failed: "+r["errors"][0]["message"])
sch=json.loads(r["data"]["dataTypes"]["edges"][0]["node"]["jsonSchema"])
print(json.dumps(sch.get("x-indices",{}),indent=2))'
