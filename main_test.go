package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	phuslog "github.com/phuslu/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
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
			// With LABEL_ALL=false the desired state for non-primaries is "no
			// primary label". The fixtures start without one, so the no-op
			// removals are skipped and only the primary is patched.
			name:     "label all false",
			labelAll: false,
			expectedPrimaryByPod: map[string]any{
				"mongo-1": "true",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k8sClient := newMongoClientset("default", "mongo-0", "mongo-1", "mongo-2")
			labeler := newTestLabeler(k8sClient, tt.labelAll, "mongo-1")

			err := labeler.setPrimaryLabel()
			require.NoError(t, err)

			assert.Equal(t, tt.expectedPrimaryByPod, collectPrimaryPatchValues(t, k8sClient))
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
	require.ErrorContains(t, err, "resolve primary pod name")
	assert.Empty(t, k8sClient.Actions())
}

func TestSetPrimaryLabel_ListPodsError(t *testing.T) {
	k8sClient := newMongoClientset("default", "mongo-0")
	k8sClient.PrependReactor("list", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("forbidden")
	})
	labeler := newTestLabeler(k8sClient, false, "mongo-0")

	err := labeler.setPrimaryLabel()
	require.Error(t, err)
	require.ErrorContains(t, err, "list pods in namespace")
	require.ErrorContains(t, err, "forbidden")
}

func TestSetPrimaryLabel_StopsAfterPatchError(t *testing.T) {
	k8sClient := newMongoClientset("default", "mongo-0", "mongo-1", "mongo-2")
	patchErr := errors.New("patch failed for mongo-0")
	k8sClient.PrependReactor("patch", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		patchAction, ok := action.(k8stesting.PatchAction)
		if !ok {
			return false, nil, nil
		}
		if patchAction.GetName() == "mongo-0" {
			return true, nil, patchErr
		}
		return false, nil, nil
	})

	// mongo-1 is the primary, so the non-primary mongo-0 is demoted first. Failing
	// that patch must abort the reconcile before promoting the primary or touching
	// any remaining pod.
	labeler := newTestLabeler(k8sClient, true, "mongo-1")

	err := labeler.setPrimaryLabel()
	require.Error(t, err)
	require.ErrorIs(t, err, patchErr)
	require.ErrorContains(t, err, "patch pod \"mongo-0\" primary label")

	patchedPods := []string{}
	for _, action := range k8sClient.Actions() {
		if action.GetVerb() != "patch" || action.GetResource().Resource != "pods" {
			continue
		}
		patchAction, ok := action.(k8stesting.PatchAction)
		require.True(t, ok)
		patchedPods = append(patchedPods, patchAction.GetName())
	}

	assert.Equal(t, []string{"mongo-0"}, patchedPods)
	assert.NotContains(t, patchedPods, "mongo-1")
	assert.NotContains(t, patchedPods, "mongo-2")
}

func TestParsePrimaryPodName(t *testing.T) {
	tests := []struct {
		name        string
		hello       bson.M
		wantPod     string
		wantErr     bool
		errContains string
	}{
		{
			name:    "primary field set to FQDN with port",
			hello:   bson.M{"primary": "mongo-0.mongo.default.svc.cluster.local:27017"},
			wantPod: "mongo-0",
		},
		{
			name:    "primary empty, isWritablePrimary true, me set",
			hello:   bson.M{"primary": "", "isWritablePrimary": true, "me": "mongo-1.mongo.default.svc.cluster.local:27017"},
			wantPod: "mongo-1",
		},
		{
			name:    "primary absent, ismaster true (no isWritablePrimary), me set",
			hello:   bson.M{"ismaster": true, "me": "mongo-2.mongo.default.svc.cluster.local:27017"},
			wantPod: "mongo-2",
		},
		{
			name:        "secondary node: primary empty and neither flag true",
			hello:       bson.M{"primary": "", "isWritablePrimary": false, "ismaster": false},
			wantErr:     true,
			errContains: "invalid primary host",
		},
		{
			name:        "host missing port: SplitHostPort error",
			hello:       bson.M{"primary": "mongo-0.mongo.default.svc.cluster.local"},
			wantErr:     true,
			errContains: "invalid primary host",
		},
		{
			name:        "empty host with valid port: unable to derive pod name",
			hello:       bson.M{"primary": ":27017"},
			wantErr:     true,
			errContains: "unable to derive primary pod name",
		},
		{
			name:    "precedence: primary field wins over me",
			hello:   bson.M{"primary": "mongo-0.mongo.default.svc.cluster.local:27017", "isWritablePrimary": true, "me": "mongo-9.mongo.default.svc.cluster.local:27017"},
			wantPod: "mongo-0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parsePrimaryPodName(tt.hello)
			if tt.wantErr {
				require.ErrorContains(t, err, tt.errContains)
				assert.Empty(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantPod, got)
		})
	}
}

// collectPrimaryPatchValues returns the "primary" label value sent in each pod
// patch action recorded by the fake client, keyed by pod name.
func collectPrimaryPatchValues(t *testing.T, k8sClient *fake.Clientset) map[string]any {
	t.Helper()

	values := map[string]any{}
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

		values[patchAction.GetName()] = labels["primary"]
	}
	return values
}

func TestSetPrimaryLabel_PrimaryFailover(t *testing.T) {
	k8sClient := newMongoClientset("default", "mongo-0", "mongo-1", "mongo-2")
	labeler := newTestLabeler(k8sClient, true, "mongo-1")

	// Initial detection: mongo-1 is primary and lastPrimary was unset, so this
	// exercises the "primary detected" branch.
	require.NoError(t, labeler.setPrimaryLabel())
	require.Equal(t, "mongo-1", labeler.lastPrimary)

	// Failover: mongo-2 is promoted. lastPrimary is now non-empty, so this drives
	// the "primary changed" transition branch.
	k8sClient.ClearActions()
	labeler.primaryResolver = func() (string, error) {
		return "mongo-2", nil
	}
	require.NoError(t, labeler.setPrimaryLabel())

	assert.Equal(t, "mongo-2", labeler.lastPrimary, "lastPrimary should track the new primary after failover")
	// mongo-0 was already primary=false from the initial reconcile, so it is not
	// re-patched; only the real transitions are written.
	assert.Equal(t, map[string]any{
		"mongo-1": "false",
		"mongo-2": "true",
	}, collectPrimaryPatchValues(t, k8sClient), "failover should promote mongo-2, demote the former primary, and skip the already-correct pod")
}

func TestHomeDir(t *testing.T) {
	t.Setenv("HOME", "/home/tester")
	t.Setenv("USERPROFILE", `C:\Users\tester`)
	assert.Equal(t, "/home/tester", homeDir())

	// With HOME empty (e.g. Windows), fall back to USERPROFILE.
	t.Setenv("HOME", "")
	assert.Equal(t, `C:\Users\tester`, homeDir())
}

// newClientsetWithPrimary builds a fake clientset whose pods carry the given
// "primary" label values. An empty value means the pod has no "primary" label.
func newClientsetWithPrimary(namespace string, primaryByPod map[string]string) *fake.Clientset {
	objects := make([]runtime.Object, 0, len(primaryByPod))
	for podName, primary := range primaryByPod {
		labels := map[string]string{"role": "mongo"}
		if primary != "" {
			labels["primary"] = primary
		}
		objects = append(objects, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      podName,
				Namespace: namespace,
				Labels:    labels,
			},
		})
	}
	return fake.NewClientset(objects...)
}

// TestSetPrimaryLabel_SkipsNoOpPatches verifies that pods whose "primary" label
// already matches the desired state are not re-patched, while a pod that
// genuinely needs a change (here, removal of a stale label) still is.
func TestSetPrimaryLabel_SkipsNoOpPatches(t *testing.T) {
	tests := []struct {
		name            string
		labelAll        bool
		initial         map[string]string
		primaryPod      string
		wantPatches     map[string]any
		wantLastPrimary string
	}{
		{
			name:            "all pods already correct (label all true) -> no patches",
			labelAll:        true,
			initial:         map[string]string{"mongo-0": "false", "mongo-1": "true", "mongo-2": "false"},
			primaryPod:      "mongo-1",
			wantPatches:     map[string]any{},
			wantLastPrimary: "mongo-1",
		},
		{
			name:            "label all false patches only the stale label needing removal",
			labelAll:        false,
			initial:         map[string]string{"mongo-0": "true", "mongo-1": "true", "mongo-2": ""},
			primaryPod:      "mongo-1",
			wantPatches:     map[string]any{"mongo-0": nil},
			wantLastPrimary: "mongo-1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k8sClient := newClientsetWithPrimary("default", tt.initial)
			labeler := newTestLabeler(k8sClient, tt.labelAll, tt.primaryPod)

			require.NoError(t, labeler.setPrimaryLabel())
			assert.Equal(t, tt.wantPatches, collectPrimaryPatchValues(t, k8sClient))
			// Even when the primary is already labelled (no patch issued), the
			// transition is still recorded so later failovers log correctly.
			assert.Equal(t, tt.wantLastPrimary, labeler.lastPrimary)
		})
	}
}

// TestSetPrimaryLabel_DemotesBeforePromotes verifies that on a failover the old
// primary is demoted before the new primary is promoted, with the promotion
// issued as the final patch, so the cluster never briefly advertises two
// primaries.
func TestSetPrimaryLabel_DemotesBeforePromotes(t *testing.T) {
	// mongo-0 is the stale primary (still primary=true); mongo-2 is the new one.
	k8sClient := newClientsetWithPrimary("default", map[string]string{
		"mongo-0": "true",
		"mongo-1": "false",
		"mongo-2": "false",
	})
	labeler := newTestLabeler(k8sClient, true, "mongo-2")

	require.NoError(t, labeler.setPrimaryLabel())

	patchedPods := []string{}
	for _, action := range k8sClient.Actions() {
		if action.GetVerb() != "patch" || action.GetResource().Resource != "pods" {
			continue
		}
		patchAction, ok := action.(k8stesting.PatchAction)
		require.True(t, ok)
		patchedPods = append(patchedPods, patchAction.GetName())
	}

	// Only mongo-0 needs demotion (mongo-1 is already false), and the new primary
	// mongo-2 is promoted last.
	assert.Equal(t, []string{"mongo-0", "mongo-2"}, patchedPods)
}

// TestSetPrimaryLabel_StopsAfterPromotionError verifies that when the final
// promotion patch fails, the error is returned, the preceding non-primary
// demotions were still issued, and lastPrimary is NOT advanced (so the
// transition is retried and logged correctly on a later tick).
func TestSetPrimaryLabel_StopsAfterPromotionError(t *testing.T) {
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

	// mongo-1 is the primary and is promoted last; failing only its patch must
	// leave lastPrimary unset while the non-primary demotions still went out.
	labeler := newTestLabeler(k8sClient, true, "mongo-1")

	err := labeler.setPrimaryLabel()
	require.ErrorIs(t, err, patchErr)
	require.ErrorContains(t, err, "patch pod \"mongo-1\" primary label")

	patchedPods := []string{}
	for _, action := range k8sClient.Actions() {
		if action.GetVerb() != "patch" || action.GetResource().Resource != "pods" {
			continue
		}
		patchAction, ok := action.(k8stesting.PatchAction)
		require.True(t, ok)
		patchedPods = append(patchedPods, patchAction.GetName())
	}

	// Both non-primary demotions were issued before the promotion attempt failed.
	assert.Equal(t, []string{"mongo-0", "mongo-2", "mongo-1"}, patchedPods)
	assert.Empty(t, labeler.lastPrimary, "a failed promotion must not advance lastPrimary")
}

func TestKubeconfigFlag(t *testing.T) {
	origArgs := os.Args
	t.Cleanup(func() { os.Args = origArgs })

	const def = "/home/tester/.kube/config"

	// No flag: the default path is returned unchanged.
	os.Args = []string{"sidecar"}
	got, err := kubeconfigFlag(def)
	require.NoError(t, err)
	assert.Equal(t, def, got)

	// --kubeconfig=VALUE overrides the default (README-documented behavior).
	os.Args = []string{"sidecar", "--kubeconfig=/tmp/dev"}
	got, err = kubeconfigFlag(def)
	require.NoError(t, err)
	assert.Equal(t, "/tmp/dev", got)

	// The single-dash -kubeconfig VALUE form is also honored.
	os.Args = []string{"sidecar", "-kubeconfig", "/tmp/dev2"}
	got, err = kubeconfigFlag(def)
	require.NoError(t, err)
	assert.Equal(t, "/tmp/dev2", got)

	// Malformed invocations must fail fast, not silently use the default.
	os.Args = []string{"sidecar", "--kubeconfig"} // missing value
	_, err = kubeconfigFlag(def)
	require.Error(t, err)

	os.Args = []string{"sidecar", "--unknown-flag"} // unknown flag
	_, err = kubeconfigFlag(def)
	require.Error(t, err)
}

// withUnsetEnv unsets an environment variable for the duration of the test and
// restores its previous value afterward.
func withUnsetEnv(t *testing.T, key string) {
	t.Helper()
	orig, had := os.LookupEnv(key)
	require.NoError(t, os.Unsetenv(key))
	t.Cleanup(func() {
		if had {
			require.NoError(t, os.Setenv(key, orig))
		} else {
			require.NoError(t, os.Unsetenv(key))
		}
	})
}

func TestGetMongoPrimary(t *testing.T) {
	t.Run("parses primary pod from fetched hello response", func(t *testing.T) {
		l := &Labeler{
			Config: &Config{Address: "localhost:27017"},
			helloFetcher: func(context.Context) (bson.M, error) {
				return bson.M{"primary": "mongo-1.mongo.default.svc.cluster.local:27017"}, nil
			},
		}
		got, err := l.getMongoPrimary()
		require.NoError(t, err)
		assert.Equal(t, "mongo-1", got)
	})

	t.Run("propagates fetch error", func(t *testing.T) {
		fetchErr := errors.New("mongo unreachable")
		l := &Labeler{
			Config: &Config{Address: "localhost:27017"},
			helloFetcher: func(context.Context) (bson.M, error) {
				return nil, fetchErr
			},
		}
		_, err := l.getMongoPrimary()
		require.ErrorIs(t, err, fetchErr)
	})

	t.Run("surfaces parse error for a non-primary response", func(t *testing.T) {
		l := &Labeler{
			Config: &Config{Address: "localhost:27017"},
			helloFetcher: func(context.Context) (bson.M, error) {
				return bson.M{"isWritablePrimary": false}, nil
			},
		}
		_, err := l.getMongoPrimary()
		require.ErrorContains(t, err, "invalid primary host")
	})
}

func TestGetKubeClientSet_OutOfClusterError(t *testing.T) {
	// Force the out-of-cluster path and point --kubeconfig at a missing file so
	// loading fails deterministically.
	withUnsetEnv(t, "KUBERNETES_SERVICE_HOST")

	origArgs := os.Args
	t.Cleanup(func() { os.Args = origArgs })
	os.Args = []string{"sidecar", "--kubeconfig=" + filepath.Join(t.TempDir(), "missing-kubeconfig")}

	_, err := getKubeClientSet()
	require.Error(t, err)
}

func TestNew_KubeClientError(t *testing.T) {
	withUnsetEnv(t, "KUBERNETES_SERVICE_HOST")

	origArgs := os.Args
	t.Cleanup(func() { os.Args = origArgs })
	os.Args = []string{"sidecar", "--kubeconfig=" + filepath.Join(t.TempDir(), "missing-kubeconfig")}

	_, err := New(&Config{LabelSelector: "role=mongo"})
	require.Error(t, err)
}
