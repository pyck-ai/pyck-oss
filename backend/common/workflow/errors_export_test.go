package workflow

import "google.golang.org/grpc/codes"

// PermanentCodes exposes the retry verdict table so the external test
// package can assert that every gRPC status code carries a deliberate
// verdict rather than falling through to the retryable default.
var PermanentCodes map[codes.Code]bool = permanentCodes
