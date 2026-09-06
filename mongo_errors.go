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
	category  string
	code      int32
	cause     error
}

func newMongoFailure(operation string, cause error) *mongoFailure {
	// A failed handshake is stored in the server description, rather than the
	// selection timeout's unwrap chain. Inspect the single direct server's cause
	// so an incorrect password is reported as authentication failure, not timeout.
	classified := cause
	var selectionError topology.ServerSelectionError
	if errors.As(cause, &selectionError) && len(selectionError.Desc.Servers) == 1 {
		classified = errors.Join(cause, selectionError.Desc.Servers[0].LastError)
	}

	code := serverErrorCode(classified)
	var authError *auth.Error
	var networkError net.Error
	var poolError driver.RetryablePoolError
	category := "operation_failed"
	switch {
	case code == 18 || errors.As(classified, &authError):
		category = "authentication_failed"
	case code == 13:
		category = "authorization_failed"
	case errors.Is(classified, context.Canceled):
		category = "canceled"
	case mongo.IsTimeout(classified):
		category = "timeout"
	case errors.As(classified, &poolError):
		category = "connection_pool_unavailable"
	// IsNetworkError only checks driver error labels, not plain dialer errors.
	case mongo.IsNetworkError(classified) || errors.As(classified, &networkError):
		category = "network_error"
	case code != 0:
		category = "command_failed"
	}
	return &mongoFailure{operation: operation, category: category, code: code, cause: cause}
}

// serverErrorCode reads only the numeric MongoDB error code, never the message.
func serverErrorCode(err error) int32 {
	var commandError mongo.CommandError
	if errors.As(err, &commandError) {
		return commandError.Code
	}
	var driverError driver.Error
	if errors.As(err, &driverError) {
		return driverError.Code
	}
	return 0
}

func (e *mongoFailure) Error() string {
	if e.code != 0 {
		return fmt.Sprintf("mongo %s: %s (code %d)", e.operation, e.category, e.code)
	}
	return fmt.Sprintf("mongo %s: %s", e.operation, e.category)
}

func (e *mongoFailure) Unwrap() error { return e.cause }
