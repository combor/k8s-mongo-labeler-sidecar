package main

import (
	"errors"
	"net/url"
	"os"
	"strings"

	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// mongoOptionsFromEnvironment builds the MongoDB client options. Environment
// credentials are passed through SetAuth, never spliced into the URI: the driver
// retains the original URI string and echoes it in its own error messages.
func mongoOptionsFromEnvironment(address string) (*options.ClientOptions, error) {
	username, password, source, envAuth, err := mongoCredentialsFromEnvironment()
	if err != nil {
		return nil, err
	}

	uri := address
	switch {
	case strings.HasPrefix(uri, "mongodb+srv://"):
		return nil, errors.New("MONGO_ADDRESS must use a single direct endpoint; SRV is not supported")
	case strings.HasPrefix(uri, "mongodb://"):
	case strings.Contains(uri, "://"):
		return nil, errors.New("MONGO_ADDRESS has an unsupported scheme")
	default:
		uri = "mongodb://" + uri
	}

	// ApplyURI defers its parse error to Validate. Call Validate before SetDirect
	// so a multi-host URI reports the specific message below.
	clientOptions := options.Client().ApplyURI(uri)
	if err := clientOptions.Validate(); err != nil {
		return nil, errors.New("invalid MONGO_ADDRESS: unable to parse MongoDB connection settings")
	}
	if len(clientOptions.Hosts) != 1 {
		return nil, errors.New("MONGO_ADDRESS must contain exactly one host")
	}
	clientOptions.SetDirect(true).SetMinPoolSize(1).SetMaxPoolSize(1).
		SetLoggerOptions(options.Logger().SetSink(discardMongoLogs{}))

	if envAuth {
		// Auth is set from URI user info, an authentication mechanism, or
		// mechanism properties: each a conflicting source of credentials.
		if clientOptions.Auth != nil {
			return nil, errors.New("MONGO_ADDRESS credentials and authentication options cannot be combined with MONGO_USERNAME, MONGO_PASSWORD, or MONGO_AUTH_SOURCE")
		}
		if source == "" {
			source = uriAuthSource(uri)
		}
		clientOptions.SetAuth(options.Credential{
			Username:   username,
			Password:   password,
			AuthSource: source,
		})
	}
	// Some URI options are only invalid in combination with the settings above,
	// such as loadBalanced against a direct connection.
	if err := clientOptions.Validate(); err != nil {
		return nil, errors.New("invalid MONGO_ADDRESS: unsupported connection options")
	}
	return clientOptions, nil
}

// mongoCredentialsFromEnvironment reads the credential variables. Setting any
// one of them opts into authentication, so a partial pair is an error rather
// than a silent fallback to an unauthenticated connection.
func mongoCredentialsFromEnvironment() (username, password, source string, envAuth bool, err error) {
	username, usernameSet := os.LookupEnv("MONGO_USERNAME")
	password, passwordSet := os.LookupEnv("MONGO_PASSWORD")
	source, sourceSet := os.LookupEnv("MONGO_AUTH_SOURCE")
	envAuth = usernameSet || passwordSet || sourceSet
	switch {
	case envAuth && (username == "" || password == ""):
		err = errors.New("MONGO_USERNAME and MONGO_PASSWORD must both be nonempty when authentication variables are set")
	case sourceSet && source == "":
		err = errors.New("MONGO_AUTH_SOURCE must be nonempty when set")
	}
	return username, password, source, envAuth, err
}

// uriAuthSource returns the authentication database implied by a URI carrying no
// credentials of its own. Option names are compared case-insensitively because
// the driver lowercases them; ApplyURI has already accepted this URI.
func uriAuthSource(uri string) string {
	parsed, err := url.Parse(uri)
	if err != nil {
		return "admin"
	}
	for key, values := range parsed.Query() {
		if strings.EqualFold(key, "authSource") && values[0] != "" {
			return values[0]
		}
	}
	if database := strings.TrimPrefix(parsed.Path, "/"); database != "" {
		return database
	}
	return "admin"
}

// Driver logs have an independent environment-controlled sink. Suppress them
// so all MongoDB diagnostics go through the sidecar's safe failure boundary.
type discardMongoLogs struct{}

func (discardMongoLogs) Info(_ int, _ string, _ ...any)    {}
func (discardMongoLogs) Error(_ error, _ string, _ ...any) {}
