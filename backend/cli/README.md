# Pyck CLI tool

pyck is a command-line utility that enables users to create, manage,
	and interact with simulated virtual storage facilities. It streamlines
	storage infrastructure testing, planning, and optimization, allowing
	developers to evaluate various
	configurations and performance scenarios effortlessly.

You can do with this tool:
* create repositories for current tenant
* create an initial stock for current tenant
* create orders

## Build

```sh
task build
# or
go build ./...
```

## Install
```sh
task install
# or
go install -v ./...
```

## Usage

Type **pyck** to get the help or use **--help** on specific commands for help.

## Setup
You can set the gateway url for the graphql-API either by **--gateway-url** argument on each command or set an environment variable **PYCK_GATEWAY_URL**, like *export PYCK_GATEWAY_URL=http://localhost:4000* for example.

## Commands

### Generate data for current tenant

```
pyck generate data --items=100 --customers 100 --suppliers 50
```
This will add 100 items, 100 customers and 50 suppliers for current tenant.

```
pyck generate data --items=100 --customers 100 --suppliers 50
```

### Generate a repository for current tenant

```
pyck generate repositories --type haufen
```

This will generate repositories with a root facility, two zones and two repositories for each zone:

```
Halle - 0dc9c504-f32f-4ebc-80ed-28e7171a58a8 count: true
  Zone B - 76a095ec-dd84-4438-8d72-a8c95ba1deb4 count: true
    Buffer - 55f0a94c-15bd-438c-8ff4-823f4401798f count: true
    Warenausgang - c1fadfec-52e3-47ca-b247-8de1d03959f8 count: true
  Zone A - 95be2502-467f-41af-9789-53f5379d110c count: true
    Haufen - 1d1c3fb8-2490-40a9-95cf-49d66b41cd6d count: true
    Wareneingang - ba6aef7d-8276-4442-b6b0-053bd9ebd541 count: false
```

### List repositories for current tenant

```
pyck list repositories
```

### Create an initial stock current tenant

```
pyck generate initial-stock --target-repo-id=1d1c3fb8-2490-40a9-95cf-49d66b41cd6d  --max-items-per-stock 100
```
This command will create item movements for every item of the current tenant from **Wareneingang** (check_stock=false) to the **Haufen** (1d1c3fb8-2490-40a9-95cf-49d66b41cd6d), with a maximal random stock of **--max-items-per-stock** (between 1 and 100 in this example). The movements will be set executed by the command, so stocks are getting generated for the items.


### Create orders for current tenant

```
pyck generate orders --count=100
```

This will create 100 orders with two items per order.
