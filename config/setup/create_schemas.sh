#!/bin/sh
#
set -e

docker exec -it db cockroach sql --insecure --execute "CREATE SCHEMA IF NOT EXISTS inventory; \
CREATE SCHEMA IF NOT EXISTS main_data; \
CREATE SCHEMA IF NOT EXISTS management; \
CREATE SCHEMA IF NOT EXISTS picking; \
CREATE SCHEMA IF NOT EXISTS receiving;"
