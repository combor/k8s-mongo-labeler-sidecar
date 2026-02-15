package main

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	phuslog "github.com/phuslu/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
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

func newMongoClientset(namespace string, podNames ...string) *fake.Clientset {
	objects := make([]runtime.Object, 0, len(podNames))
	for _, podName := range podNames {
		objects = append(objects, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      podName,
				Namespace: namespace,
				Labels: map[string]string{
					"role": "mongo",
				},
			},
		})
	}
	return fake.NewClientset(objects...)
}

func newTestLabeler(k8sClient *fake.Clientset, labelAll bool, primaryPodName string) *Labeler {
	return &Labeler{
		Config: &Config{
			LabelSelector:     "role=mongo",
			Namespace:         "default",
			LabelAll:          labelAll,
			K8sRequestTimeout: time.Second,
		},
		K8sClient: k8sClient,
		primaryResolver: func() (string, error) {
			return primaryPodName, nil
		},
	}
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
				require.NoError(t, err)
				assert.Equal(t, tt.expectedConfig, config)
			}
		})
	}
}

func TestSetPrimaryLabel_LabelAllVariants(t *testing.T) {
	tests := []struct {
		name                 string
		labelAll             bool
		expectedPrimaryByPod map[string]any
	}{
		{
			name:     "label all true",
			labelAll: true,
			expectedPrimaryByPod: map[string]any{
				"mongo-0": "false",
				"mongo-1": "true",
				"mongo-2": "false",
			},
		},
		{
			name:     "label all false",
			labelAll: false,
			expectedPrimaryByPod: map[string]any{
				"mongo-0": nil,
				"mongo-1": "true",
				"mongo-2": nil,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k8sClient := newMongoClientset("default", "mongo-0", "mongo-1", "mongo-2")
			labeler := newTestLabeler(k8sClient, tt.labelAll, "mongo-1")

			err := labeler.setPrimaryLabel()
			require.NoError(t, err)

			primaryValuesByPod := map[string]any{}
			for _, action := range k8sClient.Actions() {
				if action.GetVerb() != "patch" || action.GetResource().Resource != "pods" {
					continue
				}
				patchAction, ok := action.(k8stesting.PatchAction)
				require.True(t, ok)

				var patch map[string]any
				require.NoError(t, json.Unmarshal(patchAction.GetPatch(), &patch))

				metadata, ok := patch["metadata"].(map[string]any)
				require.True(t, ok)
				labels, ok := metadata["labels"].(map[string]any)
				require.True(t, ok)

				primaryValuesByPod[patchAction.GetName()] = labels["primary"]
			}

			assert.Equal(t, tt.expectedPrimaryByPod, primaryValuesByPod)
		})
	}
}

func TestSetPrimaryLabel_PrimaryNotFound(t *testing.T) {
	k8sClient := newMongoClientset("default", "mongo-0", "mongo-1", "mongo-2")
	labeler := newTestLabeler(k8sClient, true, "mongo-9")

	err := labeler.setPrimaryLabel()
	require.Error(t, err)
	require.ErrorContains(t, err, "primary not found")

	patchActions := 0
	for _, action := range k8sClient.Actions() {
		if action.GetVerb() == "patch" && action.GetResource().Resource == "pods" {
			patchActions++
		}
	}
	assert.Equal(t, 0, patchActions, "should not patch any pods when primary is not found")
}

func TestSetPrimaryLabel_PrimaryResolverError(t *testing.T) {
	k8sClient := newMongoClientset("default", "mongo-0", "mongo-1", "mongo-2")
	primaryErr := errors.New("mongo unavailable")
	labeler := newTestLabeler(k8sClient, true, "mongo-1")
	labeler.primaryResolver = func() (string, error) {
		return "", primaryErr
	}

	err := labeler.setPrimaryLabel()
	require.Error(t, err)
	require.ErrorIs(t, err, primaryErr)
	assert.Empty(t, k8sClient.Actions())
}

func TestSetPrimaryLabel_StopsAfterPatchError(t *testing.T) {
	k8sClient := newMongoClientset("default", "mongo-0", "mongo-1", "mongo-2")
	patchErr := errors.New("patch failed for mongo-1")
	k8sClient.PrependReactor("patch", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		patchAction, ok := action.(k8stesting.PatchAction)
		if !ok {
			return false, nil, nil
		}
		if patchAction.GetName() == "mongo-1" {
			return true, nil, patchErr
		}
		return false, nil, nil
	})

	labeler := newTestLabeler(k8sClient, true, "mongo-1")

	err := labeler.setPrimaryLabel()
	require.Error(t, err)
	require.ErrorIs(t, err, patchErr)

	patchedPods := []string{}
	for _, action := range k8sClient.Actions() {
		if action.GetVerb() != "patch" || action.GetResource().Resource != "pods" {
			continue
		}
		patchAction, ok := action.(k8stesting.PatchAction)
		require.True(t, ok)
		patchedPods = append(patchedPods, patchAction.GetName())
	}

	assert.Equal(t, []string{"mongo-0", "mongo-1"}, patchedPods)
	assert.NotContains(t, patchedPods, "mongo-2")
}
