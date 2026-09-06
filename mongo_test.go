package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"testing"
	"time"

	phuslog "github.com/phuslu/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/x/mongo/driver"
	"go.mongodb.org/mongo-driver/v2/x/mongo/driver/description"
	"go.mongodb.org/mongo-driver/v2/x/mongo/driver/topology"
)

const testMongoUser = "sidecar-user@private"
const testMongoPassword = "password:@/%+?&=secret"

func captureLogs(t *testing.T, level phuslog.Level) *bytes.Buffer {
	t.Helper()
	var buffer bytes.Buffer
	original := phuslog.DefaultLogger
	phuslog.DefaultLogger = phuslog.Logger{
		Level: level,
		Writer: phuslog.WriterFunc(func(entry *phuslog.Entry) (int, error) {
			return buffer.Write(entry.Value())
		}),
	}
	t.Cleanup(func() { phuslog.DefaultLogger = original })
	return &buffer
}

func assertNoMongoSecrets(t *testing.T, text string) {
	t.Helper()
	for _, secret := range []string{testMongoUser, testMongoPassword, url.QueryEscape(testMongoUser), url.QueryEscape(testMongoPassword), url.PathEscape(testMongoUser), url.PathEscape(testMongoPassword)} {
		assert.NotContains(t, text, secret)
	}
}

func TestMongoAuthenticationConfiguration(t *testing.T) {
	userinfo := url.UserPassword(testMongoUser, testMongoPassword).String()
	tests := []struct {
		name    string
		address string
		env     map[string]string
		source  string
	}{
		{name: "unauthenticated default"},
		{name: "environment credentials", env: map[string]string{"MONGO_USERNAME": testMongoUser, "MONGO_PASSWORD": testMongoPassword}, source: "admin"},
		{name: "database fallback", address: "mongo:27017/application", env: map[string]string{"MONGO_USERNAME": testMongoUser, "MONGO_PASSWORD": testMongoPassword}, source: "application"},
		{name: "URI source", address: "mongo:27017/application?authSource=users", env: map[string]string{"MONGO_USERNAME": testMongoUser, "MONGO_PASSWORD": testMongoPassword}, source: "users"},
		{name: "environment source takes precedence", address: "mongodb://mongo:27017/application?authSource=users", env: map[string]string{"MONGO_USERNAME": testMongoUser, "MONGO_PASSWORD": testMongoPassword, "MONGO_AUTH_SOURCE": "accounts"}, source: "accounts"},
		{name: "explicit SCRAM with environment credentials", address: "mongo:27017/?authMechanism=SCRAM-SHA-256&appName=labeler", env: map[string]string{"MONGO_USERNAME": testMongoUser, "MONGO_PASSWORD": testMongoPassword}, source: "admin"},
		{name: "legacy credentials", address: userinfo + "@mongo:27017/application?authSource=users", source: "users"},
		{name: "full URI credentials", address: "mongodb://" + userinfo + "@mongo:27017/application", source: "application"},
		{name: "IPv6 URI", address: "mongodb://" + userinfo + "@[::1]:27017/?authSource=users", source: "users"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := map[string]string{"LABEL_SELECTOR": "role=mongo"}
			if tt.address != "" {
				env["MONGO_ADDRESS"] = tt.address
			}
			for key, value := range tt.env {
				env[key] = value
			}
			setConfigEnv(t, env)
			config, err := getConfigFromEnvironment()
			require.NoError(t, err)
			opts := config.mongoOptions
			require.NotNil(t, opts)
			assert.Equal(t, opts.Hosts[0], config.Address)
			assert.NotContains(t, config.Address, "@")
			assert.NotContains(t, config.Address, "?")
			assert.True(t, *opts.Direct)
			assert.Equal(t, uint64(1), *opts.MinPoolSize)
			assert.Equal(t, uint64(1), *opts.MaxPoolSize)
			if tt.source == "" {
				assert.Nil(t, opts.Auth)
			} else {
				require.NotNil(t, opts.Auth)
				assert.Equal(t, testMongoUser, opts.Auth.Username)
				assert.Equal(t, testMongoPassword, opts.Auth.Password)
				assert.Equal(t, tt.source, opts.Auth.AuthSource)
			}
			if tt.name == "explicit SCRAM with environment credentials" {
				assert.Equal(t, "SCRAM-SHA-256", opts.Auth.AuthMechanism)
				assert.Equal(t, "labeler", *opts.AppName)
			}
			for _, level := range []phuslog.Level{phuslog.InfoLevel, phuslog.DebugLevel} {
				logs := captureLogs(t, level)
				logConfiguration(config)
				assert.Contains(t, logs.String(), "mongo_auth_enabled")
				assert.Contains(t, logs.String(), config.Address)
				assertNoMongoSecrets(t, logs.String())
			}
		})
	}
}

func TestMongoConfigurationErrorsDoNotExposeValues(t *testing.T) {
	userinfo := url.UserPassword(testMongoUser, testMongoPassword).String()
	tests := []struct {
		name string
		env  map[string]string
	}{
		{"missing password", map[string]string{"MONGO_USERNAME": testMongoUser}},
		{"missing username", map[string]string{"MONGO_PASSWORD": testMongoPassword}},
		{"empty password", map[string]string{"MONGO_USERNAME": testMongoUser, "MONGO_PASSWORD": ""}},
		{"empty username", map[string]string{"MONGO_USERNAME": "", "MONGO_PASSWORD": testMongoPassword}},
		{"source without credentials", map[string]string{"MONGO_AUTH_SOURCE": testMongoPassword}},
		{"empty source", map[string]string{"MONGO_USERNAME": testMongoUser, "MONGO_PASSWORD": testMongoPassword, "MONGO_AUTH_SOURCE": ""}},
		{"mixed credentials", map[string]string{"MONGO_ADDRESS": userinfo + "@mongo:27017", "MONGO_USERNAME": testMongoUser, "MONGO_PASSWORD": testMongoPassword}},
		{"URI and environment source", map[string]string{"MONGO_ADDRESS": userinfo + "@mongo:27017", "MONGO_AUTH_SOURCE": "admin"}},
		{"malformed credential escape", map[string]string{"MONGO_ADDRESS": testMongoUser + ":%invalid@mongo:27017"}},
		{"option contains password", map[string]string{"MONGO_ADDRESS": "mongo:27017/?connectTimeoutMS=" + url.QueryEscape(testMongoPassword)}},
		{"TLS option contains password", map[string]string{"MONGO_ADDRESS": "mongo:27017/?tlsCAFile=" + url.QueryEscape(testMongoPassword)}},
		{"unsupported scheme", map[string]string{"MONGO_ADDRESS": "https://" + userinfo + "@mongo:27017"}},
		{"SRV", map[string]string{"MONGO_ADDRESS": "mongodb+srv://" + userinfo + "@mongo.invalid"}},
		{"multiple hosts", map[string]string{"MONGO_ADDRESS": userinfo + "@mongo:27017,other:27017"}},
		{"empty address", map[string]string{"MONGO_ADDRESS": ""}},
		{"invalid boolean", map[string]string{"DEBUG": testMongoPassword}},
		{"invalid duration", map[string]string{"K8S_REQUEST_TIMEOUT": testMongoPassword}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.env["LABEL_SELECTOR"] = "role=mongo"
			setConfigEnv(t, tt.env)
			config, err := getConfigFromEnvironment()
			require.Error(t, err)
			assert.Nil(t, config)
			logs := captureLogs(t, phuslog.DebugLevel)
			phuslog.Error().Err(err).Msg("failed to read configuration")
			assertNoMongoSecrets(t, logs.String())
			assertNoMongoSecrets(t, fmt.Sprintf("%+v", err))
		})
	}
}

func TestMongoFailureLogging(t *testing.T) {
	tests := []struct {
		name     string
		cause    error
		category string
		code     int32
	}{
		{"authentication", mongo.CommandError{Code: 18, Name: testMongoUser, Message: testMongoPassword}, "authentication_failed", 18},
		{"authorization", driver.Error{Code: 13, Name: testMongoUser, Message: testMongoPassword}, "authorization_failed", 13},
		{"command", mongo.CommandError{Code: 123, Message: testMongoPassword}, "command_failed", 123},
		{"timeout", fmt.Errorf("%s: %w", testMongoPassword, context.DeadlineExceeded), "timeout", 0},
		{"cancel", fmt.Errorf("%s: %w", testMongoPassword, context.Canceled), "canceled", 0},
		{"network", &net.OpError{Op: testMongoUser, Net: "tcp", Err: errors.New(testMongoPassword)}, "network_error", 0},
		{"unknown", errors.New(testMongoUser + ":" + url.QueryEscape(testMongoPassword)), "operation_failed", 0},
		{"opaque pool failure", testPoolError{}, "connection_pool_unavailable", 0},
		{"handshake authentication hidden in topology", topology.ServerSelectionError{
			Wrapped: context.DeadlineExceeded,
			Desc: description.Topology{Servers: []description.Server{{
				LastError: driver.Error{Code: 18, Message: testMongoPassword},
			}}},
		}, "authentication_failed", 18},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, operation := range []string{"connect", "ping", "hello", "disconnect"} {
				nested := fmt.Errorf("%s: %w", testMongoPassword, tt.cause)
				failure := newMongoFailure(operation, "mongo:27017", nested)
				require.ErrorIs(t, failure, nested)
				var commandCause mongo.CommandError
				if errors.As(tt.cause, &commandCause) {
					var original mongo.CommandError
					require.ErrorAs(t, failure, &original)
					assert.Equal(t, tt.code, original.Code)
				}
				assert.Equal(t, tt.category, failure.category)
				assert.Equal(t, tt.code, failure.code)
				var decoded *mongoFailure
				require.ErrorAs(t, failure, &decoded)
				for _, level := range []phuslog.Level{phuslog.InfoLevel, phuslog.DebugLevel} {
					logs := captureLogs(t, level)
					phuslog.Error().Err(fmt.Errorf("resolve primary: %w", failure)).Msg("failed to set primary label")
					phuslog.Debug().Err(failure).Msg("MongoDB failure")
					assert.Contains(t, logs.String(), tt.category)
					assert.Contains(t, logs.String(), operation)
					assertNoMongoSecrets(t, logs.String())
					assertNoMongoSecrets(t, fmt.Sprintf("%+v", failure))
				}
			}
		})
	}
}

type failingMongoDialer struct{}

type testPoolError struct{}

func (testPoolError) Error() string   { return testMongoPassword }
func (testPoolError) Retryable() bool { return true }

func (failingMongoDialer) DialContext(context.Context, string, string) (net.Conn, error) {
	return nil, errors.New(testMongoUser + ":" + testMongoPassword)
}

func TestMongoConnectionFailuresDoNotPatchOrLeak(t *testing.T) {
	for _, invalidOptions := range []bool{false, true} {
		t.Run(fmt.Sprintf("invalid_options_%t", invalidOptions), func(t *testing.T) {
			setConfigEnv(t, map[string]string{"LABEL_SELECTOR": "role=mongo", "MONGO_USERNAME": testMongoUser, "MONGO_PASSWORD": testMongoPassword})
			config, err := getConfigFromEnvironment()
			require.NoError(t, err)
			config.mongoOptions.SetDialer(failingMongoDialer{}).SetServerSelectionTimeout(20 * time.Millisecond)
			if invalidOptions {
				config.mongoOptions.ApplyURI("mongodb://localhost/?connectTimeoutMS=" + url.QueryEscape(testMongoPassword))
			}
			t.Setenv("MONGODB_LOG_ALL", "debug")
			t.Setenv("MONGODB_LOG_PATH", t.TempDir()+"/driver.log")
			logs := captureLogs(t, phuslog.DebugLevel)
			clientset := newMongoClientset("default", "mongo-0")
			labeler := &Labeler{Config: config, K8sClient: clientset}
			err = labeler.setPrimaryLabel()
			require.Error(t, err)
			var failure *mongoFailure
			require.ErrorAs(t, err, &failure)
			assert.Empty(t, clientset.Actions())
			phuslog.Error().Err(err).Msg("failed to set primary label")
			labeler.closeMongo(context.Background())
			assertNoMongoSecrets(t, logs.String())
			assertNoMongoSecrets(t, err.Error())
			assert.NoFileExists(t, envString("MONGODB_LOG_PATH", ""))
		})
	}
}

func TestMalformedHelloDoesNotExposeServerData(t *testing.T) {
	setConfigEnv(t, map[string]string{"LABEL_SELECTOR": "role=mongo"})
	config, err := getConfigFromEnvironment()
	require.NoError(t, err)
	clientset := newMongoClientset("default", "mongo-0")
	labeler := &Labeler{
		Config: config, K8sClient: clientset,
		helloFetcher: func(context.Context) (bson.M, error) {
			return bson.M{"primary": "mongodb://" + url.UserPassword(testMongoUser, testMongoPassword).String() + "@mongo:27017"}, nil
		},
	}
	err = labeler.setPrimaryLabel()
	require.ErrorContains(t, err, "parse_primary")
	assert.Empty(t, clientset.Actions())
	logs := captureLogs(t, phuslog.DebugLevel)
	phuslog.Error().Err(err).Msg("failed to set primary label")
	assertNoMongoSecrets(t, logs.String())
}
