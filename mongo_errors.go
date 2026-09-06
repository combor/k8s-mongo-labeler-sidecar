package main

import (
	"context"
	"errors"
	"fmt"
	"net"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/x/mongo/driver"
	"go.mongodb.org/mongo-driver/v2/x/mongo/driver/auth"
	"go.mongodb.org/mongo-driver/v2/x/mongo/driver/topology"
)

// mongoFailure retains the cause for errors.Is/As but never formats driver or
// server text. Those messages may contain credentials or arbitrary URI values.
type mongoFailure struct {
	operation string
	endpoint  string
	category  string
	code      int32
	cause     error
}

func newMongoFailure(operation, endpoint string, cause error) *mongoFailure {
	failure := &mongoFailure{operation: operation, endpoint: endpoint, category: "operation_failed", cause: cause}
	// A failed handshake is stored in the server description, rather than the
	// selection timeout's unwrap chain. Inspect the single direct server's cause
	// so an incorrect password is reported as authentication failure, not timeout.
	var selectionError topology.ServerSelectionError
	if errors.As(cause, &selectionError) && len(selectionError.Desc.Servers) == 1 {
		cause = errors.Join(cause, selectionError.Desc.Servers[0].LastError)
	}
	var commandError mongo.CommandError
	var driverError driver.Error
	switch {
	case errors.As(cause, &commandError):
		failure.code = commandError.Code
	case errors.As(cause, &driverError):
		failure.code = driverError.Code
	}
	var authError *auth.Error
	var networkError net.Error
	var poolError driver.RetryablePoolError
	switch {
	case failure.code == 18 || errors.As(cause, &authError):
		failure.category = "authentication_failed"
	case failure.code == 13:
		failure.category = "authorization_failed"
	case errors.Is(cause, context.Canceled):
		failure.category = "canceled"
	case mongo.IsTimeout(cause):
		failure.category = "timeout"
	case errors.As(cause, &poolError):
		failure.category = "connection_pool_unavailable"
	case mongo.IsNetworkError(cause) || errors.As(cause, &networkError):
		failure.category = "network_error"
	case failure.code != 0:
		failure.category = "command_failed"
	}
	return failure
}

func (e *mongoFailure) Error() string {
	message := fmt.Sprintf("mongo %s at %q: %s", e.operation, e.endpoint, e.category)
	if e.code != 0 {
		message += fmt.Sprintf(" (code %d)", e.code)
	}
	return message
}

func (e *mongoFailure) Unwrap() error { return e.cause }
