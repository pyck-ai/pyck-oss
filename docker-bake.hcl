variable "REGISTRY" {
  default = "localhost:5000"
}

variable "TAG" {
  default = "local"
}

variable "GIT_COMMIT_SHA" {
  default = "local"
}

variable "GIT_COMMIT_DATE" {
  default = "2024-01-01"
}

group "default" {
  targets = [
    "management",
    "inventory",
    "main-data",
    "picking",
    "receiving",
    "workflow",
    "file",
    "temporal",
    "gateway",
  ]
}

# Shared backend service target — all 7 services use the same Dockerfile
# with SERVICE_NAME build arg to select the binary.
target "_backend" {
  context    = "."
  dockerfile = "backend/Dockerfile"
  args = {
    GIT_COMMIT_SHA  = GIT_COMMIT_SHA
    GIT_COMMIT_DATE = GIT_COMMIT_DATE
  }
}

target "management" {
  inherits = ["_backend"]
  args     = { SERVICE_NAME = "management" }
  tags     = ["${REGISTRY}/pyck/management:${TAG}"]
  output   = ["type=registry"]
}

target "inventory" {
  inherits = ["_backend"]
  args     = { SERVICE_NAME = "inventory" }
  tags     = ["${REGISTRY}/pyck/inventory:${TAG}"]
  output   = ["type=registry"]
}

target "main-data" {
  inherits = ["_backend"]
  args     = { SERVICE_NAME = "main-data" }
  tags     = ["${REGISTRY}/pyck/main-data:${TAG}"]
  output   = ["type=registry"]
}

target "picking" {
  inherits = ["_backend"]
  args     = { SERVICE_NAME = "picking" }
  tags     = ["${REGISTRY}/pyck/picking:${TAG}"]
  output   = ["type=registry"]
}

target "receiving" {
  inherits = ["_backend"]
  args     = { SERVICE_NAME = "receiving" }
  tags     = ["${REGISTRY}/pyck/receiving:${TAG}"]
  output   = ["type=registry"]
}

target "workflow" {
  inherits = ["_backend"]
  args     = { SERVICE_NAME = "workflow" }
  tags     = ["${REGISTRY}/pyck/workflow:${TAG}"]
  output   = ["type=registry"]
}

target "file" {
  inherits = ["_backend"]
  args     = { SERVICE_NAME = "file" }
  tags     = ["${REGISTRY}/pyck/file:${TAG}"]
  output   = ["type=registry"]
}

target "temporal" {
  context    = "."
  dockerfile = "backend/temporal/Dockerfile"
  tags       = ["${REGISTRY}/pyck/temporal:${TAG}"]
  output     = ["type=registry"]
}

target "gateway" {
  context    = "."
  dockerfile = "backend/gateway/Dockerfile"
  tags       = ["${REGISTRY}/pyck/gateway:${TAG}"]
  output     = ["type=registry"]
}
