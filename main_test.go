package main

import (
	"os"
	"testing"
	"time"

	phuslog "github.com/phuslu/log"
	"github.com/stretchr/testify/assert"
)

type envState struct {
	Value string
	Set   bool
}

func setConfigEnv(t *testing.T, env map[string]string) {
	t.Helper()

	keys := []string{"LABEL_SELECTOR", "NAMESPACE", "MONGO_ADDRESS", "LABEL_ALL", "DEBUG", "K8S_REQUEST_TIMEOUT"}
	original := make(map[string]envState, len(keys))
	for _, key := range keys {
		value, ok := os.LookupEnv(key)
		original[key] = envState{Value: value, Set: ok}
		if err := os.Unsetenv(key); err != nil {
			t.Fatalf("failed to unset %s: %v", key, err)
		}
	}
	for key, value := range env {
		if err := os.Setenv(key, value); err != nil {
			t.Fatalf("failed to set %s: %v", key, err)
		}
	}
	t.Cleanup(func() {
		for _, key := range keys {
			state := original[key]
			var err error
			if state.Set {
				err = os.Setenv(key, state.Value)
			} else {
				err = os.Unsetenv(key)
			}
			assert.NoError(t, err)
		}
	})
}

func TestGetConfigFromEnvironment(t *testing.T) {
	tests := []struct {
		name                  string
		env                   map[string]string
		expectedConfig        *Config
		expectedErrorContains string
	}{
		{
			name: "all environment variables set",
			env: map[string]string{
				"LABEL_SELECTOR":      "app=mongo",
				"NAMESPACE":           "test-namespace",
				"MONGO_ADDRESS":       "mongo:27017",
				"LABEL_ALL":           "true",
				"DEBUG":               "true",
				"K8S_REQUEST_TIMEOUT": "7s",
			},
			expectedConfig: &Config{
				LabelSelector:     "app=mongo",
				Namespace:         "test-namespace",
				Address:           "mongo:27017",
				LabelAll:          true,
				LogLevel:          phuslog.DebugLevel,
				K8sRequestTimeout: 7 * time.Second,
			},
			expectedErrorContains: "",
		},
		{
			name: "missing LABEL_SELECTOR",
			env: map[string]string{
				"NAMESPACE":     "test-namespace",
				"MONGO_ADDRESS": "mongo:27017",
				"LABEL_ALL":     "true",
				"DEBUG":         "true",
			},
			expectedConfig:        nil,
			expectedErrorContains: "please export LABEL_SELECTOR",
		},
		{
			name: "default values",
			env: map[string]string{
				"LABEL_SELECTOR": "app=mongo",
			},
			expectedConfig: &Config{
				LabelSelector:     "app=mongo",
				Namespace:         "default",
				Address:           "localhost:27017",
				LabelAll:          false,
				LogLevel:          phuslog.InfoLevel,
				K8sRequestTimeout: defaultK8sRequestTimeout,
			},
			expectedErrorContains: "",
		},
		{
			name: "boolean false values are respected",
			env: map[string]string{
				"LABEL_SELECTOR": "app=mongo",
				"LABEL_ALL":      "false",
				"DEBUG":          "false",
			},
			expectedConfig: &Config{
				LabelSelector:     "app=mongo",
				Namespace:         "default",
				Address:           "localhost:27017",
				LabelAll:          false,
				LogLevel:          phuslog.InfoLevel,
				K8sRequestTimeout: defaultK8sRequestTimeout,
			},
			expectedErrorContains: "",
		},
		{
			name: "invalid DEBUG value",
			env: map[string]string{
				"LABEL_SELECTOR": "app=mongo",
				"DEBUG":          "not-a-bool",
			},
			expectedConfig:        nil,
			expectedErrorContains: "invalid DEBUG value",
		},
		{
			name: "invalid LABEL_ALL value",
			env: map[string]string{
				"LABEL_SELECTOR": "app=mongo",
				"LABEL_ALL":      "not-a-bool",
			},
			expectedConfig:        nil,
			expectedErrorContains: "invalid LABEL_ALL value",
		},
		{
			name: "invalid K8S_REQUEST_TIMEOUT value",
			env: map[string]string{
				"LABEL_SELECTOR":      "app=mongo",
				"K8S_REQUEST_TIMEOUT": "not-a-duration",
			},
			expectedConfig:        nil,
			expectedErrorContains: "invalid K8S_REQUEST_TIMEOUT value",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setConfigEnv(t, tt.env)

			config, err := getConfigFromEnvironment()
			if tt.expectedErrorContains != "" {
				assert.ErrorContains(t, err, tt.expectedErrorContains)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedConfig, config)
			}
		})
	}
}
