package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	phuslog "github.com/phuslu/log"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

type Config struct {
	LabelSelector     string
	Namespace         string
	Address           string
	LabelAll          bool
	LogLevel          phuslog.Level
	K8sRequestTimeout time.Duration
}

type Labeler struct {
	Config          *Config
	K8sClient       kubernetes.Interface
	primaryResolver func() (string, error)
	helloFetcher    func(ctx context.Context) (bson.M, error)
	lastPrimary     string
	mongoClient     *mongo.Client
}

const (
	defaultK8sRequestTimeout = 10 * time.Second
	mongoCommandTimeout      = 10 * time.Second
)

func configureLogger(level phuslog.Level) phuslog.Logger {
	logger := phuslog.DefaultLogger
	if phuslog.IsTerminal(os.Stderr.Fd()) {
		logger = phuslog.Logger{
			TimeFormat: "15:04:05",
			Caller:     1,
			Writer: &phuslog.ConsoleWriter{
				ColorOutput:    true,
				QuoteString:    true,
				EndWithMessage: true,
			},
		}
	}
	logger.SetLevel(level)
	return logger
}

func New(config *Config) (*Labeler, error) {
	k8sClient, err := getKubeClientSet()
	if err != nil {
		return nil, err
	}
	return &Labeler{
		Config:    config,
		K8sClient: k8sClient,
	}, nil
}

func (l *Labeler) setPrimaryLabel() error {
	primaryResolver := l.primaryResolver
	if primaryResolver == nil {
		primaryResolver = l.getMongoPrimary
	}
	primaryPodName, err := primaryResolver()
	if err != nil {
		return fmt.Errorf("resolve primary pod name: %w", err)
	}

	listCtx, cancel := context.WithTimeout(context.Background(), l.Config.K8sRequestTimeout)
	defer cancel()
	podsClient := l.K8sClient.CoreV1().Pods(l.Config.Namespace)
	pods, err := podsClient.List(listCtx, metav1.ListOptions{LabelSelector: l.Config.LabelSelector})
	if err != nil {
		return fmt.Errorf(
			"list pods in namespace %q with selector %q: %w",
			l.Config.Namespace,
			l.Config.LabelSelector,
			err,
		)
	}
	phuslog.Debug().Msgf("Found %d pods", len(pods.Items))

	primaryFound := false
	primaryAlreadyTrue := false
	for _, pod := range pods.Items {
		if pod.GetName() == primaryPodName {
			primaryFound = true
			primaryAlreadyTrue = pod.Labels["primary"] == "true"
			break
		}
	}
	if !primaryFound {
		return fmt.Errorf("primary not found")
	}

	// Demote (or unlabel) every non-primary pod before promoting the primary, so
	// that during a failover the old primary loses primary=true before the new one
	// gains it. This favors a brief window with no primary over one with two.
	for _, pod := range pods.Items {
		podName := pod.GetName()
		if podName == primaryPodName {
			continue
		}
		removePrimaryLabel := !l.Config.LabelAll
		currentValue, hasLabel := pod.Labels["primary"]

		// Skip pods already in the desired state to avoid needless PATCH calls.
		alreadyDesired := (removePrimaryLabel && !hasLabel) || (!removePrimaryLabel && currentValue == "false")
		if alreadyDesired {
			continue
		}

		// LABEL_ALL=false removes the label (nil); otherwise demote to "false".
		var desired any
		if !removePrimaryLabel {
			desired = "false"
		}
		if err := l.patchPrimaryLabel(podName, desired); err != nil {
			return err
		}
	}

	// Promote the primary last, and only if it is not already labelled.
	if !primaryAlreadyTrue {
		if err := l.patchPrimaryLabel(primaryPodName, "true"); err != nil {
			return err
		}
	}

	// Record the transition only after the primary's label is confirmed (it was
	// already true, or the promotion patch above succeeded), so a failed promotion
	// is retried and logged on a later tick rather than being silently recorded.
	if primaryPodName != l.lastPrimary {
		if l.lastPrimary == "" {
			phuslog.Info().Str("pod", primaryPodName).Msg("primary detected")
		} else {
			phuslog.Info().Str("from", l.lastPrimary).Str("to", primaryPodName).Msg("primary changed")
		}
		l.lastPrimary = primaryPodName
	}
	return nil
}

// patchPrimaryLabel applies a strategic-merge patch that sets pod's "primary"
// label to the given value (or removes it when label is nil), using a fresh
// per-call timeout so each request has an independent deadline rather than
// sharing one budget across the whole reconcile.
func (l *Labeler) patchPrimaryLabel(podName string, label any) error {
	patchBytes, err := json.Marshal(primaryLabelPatch(label))
	if err != nil {
		return fmt.Errorf("marshal primary label patch for pod %q: %w", podName, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), l.Config.K8sRequestTimeout)
	defer cancel()

	phuslog.Debug().Msgf("Patching pod %s with: %s", podName, string(patchBytes))
	_, err = l.K8sClient.CoreV1().Pods(l.Config.Namespace).Patch(
		ctx,
		podName,
		types.StrategicMergePatchType,
		patchBytes,
		metav1.PatchOptions{},
	)
	if err != nil {
		return fmt.Errorf("patch pod %q primary label: %w", podName, err)
	}
	return nil
}

// primaryLabelPatch builds a strategic-merge patch that sets the "primary" label
// to the given value, or removes it (null patch) when label is nil.
func primaryLabelPatch(label any) map[string]any {
	return map[string]any{
		"metadata": map[string]any{
			"labels": map[string]any{
				"primary": label,
			},
		},
	}
}

func getConfigFromEnvironment() (*Config, error) {
	labelSelector, ok := os.LookupEnv("LABEL_SELECTOR")
	if !ok {
		return nil, fmt.Errorf("please export LABEL_SELECTOR")
	}

	config := &Config{
		LabelSelector:     labelSelector,
		Namespace:         envString("NAMESPACE", "default"),
		Address:           envString("MONGO_ADDRESS", "localhost:27017"),
		LogLevel:          phuslog.InfoLevel,
		K8sRequestTimeout: defaultK8sRequestTimeout,
	}

	labelAll, err := envBool("LABEL_ALL", false)
	if err != nil {
		return nil, err
	}
	config.LabelAll = labelAll

	debug, err := envBool("DEBUG", false)
	if err != nil {
		return nil, err
	}
	if debug {
		config.LogLevel = phuslog.DebugLevel
	}

	timeout, err := envDuration("K8S_REQUEST_TIMEOUT", defaultK8sRequestTimeout)
	if err != nil {
		return nil, err
	}
	config.K8sRequestTimeout = timeout

	return config, nil
}

// envString returns the value of the environment variable key, or def if unset.
func envString(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}

// envBool parses a boolean environment variable, returning def if unset.
func envBool(key string, def bool) (bool, error) {
	v, ok := os.LookupEnv(key)
	if !ok {
		return def, nil
	}
	parsed, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("invalid %s value %q: %w", key, v, err)
	}
	return parsed, nil
}

// envDuration parses a Go duration environment variable, returning def if unset.
func envDuration(key string, def time.Duration) (time.Duration, error) {
	v, ok := os.LookupEnv(key)
	if !ok {
		return def, nil
	}
	parsed, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("invalid %s value %q: %w", key, v, err)
	}
	return parsed, nil
}

func getKubeClientSet() (*kubernetes.Clientset, error) {
	if _, ok := os.LookupEnv("KUBERNETES_SERVICE_HOST"); ok {
		config, err := rest.InClusterConfig()
		if err != nil {
			return nil, err
		}
		return kubernetes.NewForConfig(config)
	}

	// Outside a cluster, default to ~/.kube/config, overridable with the
	// --kubeconfig flag (see README).
	defaultKubeconfig := ""
	if home := homeDir(); home != "" {
		defaultKubeconfig = filepath.Join(home, ".kube", "config")
	}

	kubeconfig, err := kubeconfigFlag(defaultKubeconfig)
	if err != nil {
		return nil, err
	}
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, err
	}
	return kubernetes.NewForConfig(config)
}

// kubeconfigFlag parses the optional --kubeconfig flag from the process
// arguments using a local flag set, so it does not mutate the global
// flag.CommandLine and is safe to call more than once. A malformed invocation
// (an unknown flag, or --kubeconfig without a value) returns an error so the
// sidecar fails fast rather than silently falling back to the default kubeconfig.
func kubeconfigFlag(defaultPath string) (string, error) {
	fs := flag.NewFlagSet(os.Args[0], flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	kubeconfig := fs.String("kubeconfig", defaultPath, "(optional) absolute path to the kubeconfig file")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return "", fmt.Errorf("parse command-line flags: %w", err)
	}
	return *kubeconfig, nil
}

func homeDir() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	return os.Getenv("USERPROFILE") // windows
}

// getMongoPrimary resolves the primary pod name from MongoDB by fetching the
// "hello" command response and parsing it. The fetch step is pluggable via
// helloFetcher (defaulting to fetchHello) so the parsing/orchestration can be
// tested without a live MongoDB.
func (l *Labeler) getMongoPrimary() (string, error) {
	fetch := l.helloFetcher
	if fetch == nil {
		fetch = l.fetchHello
	}

	ctx, cancel := context.WithTimeout(context.Background(), mongoCommandTimeout)
	defer cancel()

	hello, err := fetch(ctx)
	if err != nil {
		return "", err
	}
	return parsePrimaryPodName(hello)
}

// fetchHello lazily connects a single long-lived MongoDB client on first use and
// reuses it on every subsequent tick (the mongo-driver Client is concurrency-safe
// and pools connections, so reconnecting each tick is wasteful), then returns the
// decoded "hello" command response.
func (l *Labeler) fetchHello(ctx context.Context) (bson.M, error) {
	if l.mongoClient == nil {
		clientOptions := options.Client().
			ApplyURI("mongodb://" + l.Config.Address).
			SetDirect(true).
			SetMinPoolSize(1).
			SetMaxPoolSize(1)
		client, err := mongo.Connect(clientOptions)
		if err != nil {
			return nil, fmt.Errorf("connect to mongo at %q: %w", l.Config.Address, err)
		}
		l.mongoClient = client
	}

	if err := l.mongoClient.Ping(ctx, nil); err != nil {
		return nil, fmt.Errorf("ping mongo at %q: %w", l.Config.Address, err)
	}

	var hello bson.M
	if err := l.mongoClient.Database("admin").
		RunCommand(ctx, bson.D{{Key: "hello", Value: 1}}).
		Decode(&hello); err != nil {
		return nil, fmt.Errorf("run hello command on mongo at %q: %w", l.Config.Address, err)
	}
	return hello, nil
}

// closeMongo disconnects the long-lived MongoDB client if one was created. It is
// safe to call when no client exists and is intended for graceful shutdown.
func (l *Labeler) closeMongo(ctx context.Context) {
	if l.mongoClient == nil {
		return
	}
	if err := l.mongoClient.Disconnect(ctx); err != nil {
		phuslog.Debug().Msgf("unable to close mongo connection: %v", err)
	}
	l.mongoClient = nil
}

// parsePrimaryPodName extracts the primary pod name from a MongoDB "hello"
// command response. It prefers the "primary" field and falls back to "me" when
// the node reports itself as primary via "isWritablePrimary" or "ismaster". The
// host is expected to be a "host:port" value whose host is a Kubernetes DNS name
// (e.g. mongo-0.mongo.default.svc.cluster.local); the pod name is the first
// dot-separated label.
func parsePrimaryPodName(hello bson.M) (string, error) {
	primaryHost, _ := hello["primary"].(string)
	if primaryHost == "" {
		if isWritablePrimary, ok := hello["isWritablePrimary"].(bool); ok && isWritablePrimary {
			primaryHost, _ = hello["me"].(string)
		} else if isMaster, ok := hello["ismaster"].(bool); ok && isMaster {
			primaryHost, _ = hello["me"].(string)
		}
	}
	host, _, err := net.SplitHostPort(primaryHost)
	if err != nil {
		return "", fmt.Errorf("invalid primary host %q: %w", primaryHost, err)
	}
	primaryPodName, _, _ := strings.Cut(host, ".")
	if primaryPodName != "" {
		return primaryPodName, nil
	}
	return "", fmt.Errorf("unable to derive primary pod name from host %q", host)
}

func main() {
	phuslog.DefaultLogger = configureLogger(phuslog.InfoLevel)

	config, err := getConfigFromEnvironment()
	if err != nil {
		phuslog.Fatal().Err(err).Msg("failed to read configuration")
	}
	phuslog.DefaultLogger = configureLogger(config.LogLevel)
	phuslog.Info().
		Str("namespace", config.Namespace).
		Str("label_selector", config.LabelSelector).
		Str("mongo_address", config.Address).
		Bool("label_all", config.LabelAll).
		Str("log_level", config.LogLevel.String()).
		Dur("k8s_request_timeout", config.K8sRequestTimeout).
		Msg("starting with configuration")

	labeler, err := New(config)
	if err != nil {
		phuslog.Fatal().Err(err).Msg("failed to initialize labeler")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	reconcile := func() {
		if err := labeler.setPrimaryLabel(); err != nil {
			phuslog.Error().Err(err).Msg("failed to set primary label")
		}
	}

	// Reconcile once immediately so pod labels converge at startup instead of
	// only after the first tick fires.
	reconcile()

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			phuslog.Info().Msg("shutdown signal received, stopping")
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			labeler.closeMongo(shutdownCtx)
			cancel()
			return
		case <-ticker.C:
			reconcile()
		}
	}
}
