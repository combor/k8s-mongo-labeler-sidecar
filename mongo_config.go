package main

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/x/mongo/driver/connstring"
)

func mongoOptionsFromEnvironment(address string) (*options.ClientOptions, error) {
	username, usernameSet := os.LookupEnv("MONGO_USERNAME")
	password, passwordSet := os.LookupEnv("MONGO_PASSWORD")
	source, sourceSet := os.LookupEnv("MONGO_AUTH_SOURCE")
	envAuth := usernameSet || passwordSet || sourceSet
	if envAuth && (!usernameSet || !passwordSet || username == "" || password == "") {
		return nil, errors.New("MONGO_USERNAME and MONGO_PASSWORD must both be nonempty when authentication variables are set")
	}
	if sourceSet && source == "" {
		return nil, errors.New("MONGO_AUTH_SOURCE must be nonempty when set")
	}

	uri := address
	if strings.HasPrefix(uri, "mongodb+srv://") {
		return nil, errors.New("MONGO_ADDRESS must use a single direct endpoint; SRV is not supported")
	}
	if !strings.HasPrefix(uri, "mongodb://") {
		if strings.Contains(uri, "://") {
			return nil, errors.New("MONGO_ADDRESS has an unsupported scheme")
		}
		uri = "mongodb://" + uri
	}
	parsed, err := connstring.Parse(uri)
	if err != nil {
		return nil, errors.New("invalid MONGO_ADDRESS: unable to parse MongoDB connection settings")
	}
	if len(parsed.Hosts) != 1 {
		return nil, errors.New("MONGO_ADDRESS must contain exactly one host")
	}
	if envAuth && (parsed.UsernameSet || parsed.PasswordSet) {
		return nil, errors.New("MONGO_ADDRESS credentials cannot be combined with MONGO_USERNAME, MONGO_PASSWORD, or MONGO_AUTH_SOURCE")
	}

	var credential *options.Credential
	if envAuth {
		// Validate the combined credentials using the driver's MongoDB-specific
		// rules, then pass them with SetAuth, never by inserting them into a URI.
		parsed.Username, parsed.Password = username, password
		parsed.UsernameSet, parsed.PasswordSet = true, true
		if sourceSet {
			parsed.AuthSource = source
		} else if parsed.AuthSource == "" {
			parsed.AuthSource = parsed.Database
			if parsed.AuthSource == "" {
				parsed.AuthSource = "admin"
			}
		}
		credential = &options.Credential{
			Username: username, Password: password, PasswordSet: true,
			AuthSource: parsed.AuthSource, AuthMechanism: parsed.AuthMechanism,
			AuthMechanismProperties: parsed.AuthMechanismProperties,
		}
		// ApplyURI validates authentication before SetAuth can supply the user.
		// Move authentication options into Credential while preserving the order
		// and spelling of all other URI options (including ordered TLS options).
		uri = withoutURIAuthOptions(uri)
	}
	if err := parsed.Validate(); err != nil {
		return nil, errors.New("invalid MONGO_ADDRESS authentication or connection options")
	}
	clientOptions := options.Client().ApplyURI(uri).
		SetDirect(true).SetMinPoolSize(1).SetMaxPoolSize(1).
		SetLoggerOptions(options.Logger().SetSink(discardMongoLogs{}))
	if credential != nil {
		clientOptions.SetAuth(*credential)
	}
	if err := clientOptions.Validate(); err != nil {
		return nil, errors.New("invalid MONGO_ADDRESS client options")
	}
	return clientOptions, nil
}

func withoutURIAuthOptions(uri string) string {
	base, query, found := strings.Cut(uri, "?")
	if !found {
		return uri
	}
	var keep []string
	for _, option := range strings.FieldsFunc(query, func(r rune) bool { return r == '&' || r == ';' }) {
		key, _, _ := strings.Cut(option, "=")
		// The driver already checked escapes while parsing this URI.
		key, _ = url.QueryUnescape(key)
		switch strings.ToLower(key) {
		case "authsource", "authmechanism", "authmechanismproperties":
		default:
			keep = append(keep, option)
		}
	}
	if len(keep) == 0 {
		return base
	}
	return fmt.Sprintf("%s?%s", base, strings.Join(keep, "&"))
}

// Driver logs have an independent environment-controlled sink. Suppress them
// so all MongoDB diagnostics go through the sidecar's safe failure boundary.
type discardMongoLogs struct{}

func (discardMongoLogs) Info(_ int, _ string, _ ...any)    {}
func (discardMongoLogs) Error(_ error, _ string, _ ...any) {}
