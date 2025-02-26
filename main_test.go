package main

import (
	"os"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
)

func TestGetConfigFromEnvironment(t *testing.T) {
	tests := []struct {
		name           string
		env            map[string]string
		expectedConfig *Config
		expectedError  string
	}{
		{
			name: "all environment variables set",
			env: map[string]string{
				"LABEL_SELECTOR": "app=mongo",
				"NAMESPACE":      "test-namespace",
				"MONGO_ADDRESS":  "mongo:27017",
				"LABEL_ALL":      "true",
				"DEBUG":          "true",
			},
			expectedConfig: &Config{
				LabelSelector: "app=mongo",
				Namespace:     "test-namespace",
				Address:       "mongo:27017",
				LabelAll:      true,
				LogLevel:      logrus.DebugLevel,
			},
			expectedError: "",
		},
		{
			name: "missing LABEL_SELECTOR",
			env: map[string]string{
				"NAMESPACE":     "test-namespace",
				"MONGO_ADDRESS": "mongo:27017",
				"LABEL_ALL":     "true",
				"DEBUG":         "true",
			},
			expectedConfig: nil,
			expectedError:  "please export LABEL_SELECTOR",
		},
		{
			name: "default values",
			env: map[string]string{
				"LABEL_SELECTOR": "app=mongo",
			},
			expectedConfig: &Config{
				LabelSelector: "app=mongo",
				Namespace:     "default",
				Address:       "localhost:27017",
				LabelAll:      false,
				LogLevel:      logrus.InfoLevel,
			},
			expectedError: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.env {
				os.Setenv(k, v)
			}
			defer func() {
				for k := range tt.env {
					os.Unsetenv(k)
				}
			}()

			config, err := getConfigFromEnvironment()
			if tt.expectedError != "" {
				assert.EqualError(t, err, tt.expectedError)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedConfig, config)
			}
		})
	}
}
