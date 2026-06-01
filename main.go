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
		if err := l.patchPrimaryLabel(podName, false, removePrimaryLabel); err != nil {
			return err
		}
	}

	// Promote the primary last, and only if it is not already labelled.
	if !primaryAlreadyTrue {
		if err := l.patchPrimaryLabel(primaryPodName, true, false); err != nil {
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

// patchPrimaryLabel applies a strategic-merge patch to a single pod's "primary"
// label using a fresh per-call timeout, so each request has an independent
// deadline rather than sharing one budget across the whole reconcile.
func (l *Labeler) patchPrimaryLabel(podName string, isPrimary, remove bool) error {
	patchBytes, err := json.Marshal(primaryLabelPatch(isPrimary, remove))
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

func primaryLabelPatch(value bool, remove bool) map[string]any {
	labelValue := any(strconv.FormatBool(value))
	if remove {
		// To remove a label, set it to null in strategic merge patch.
		labelValue = nil
	}
	return map[string]any{
		"metadata": map[string]any{
			"labels": map[string]any{
				"primary": labelValue,
			},
		},
	}
}

func getConfigFromEnvironment() (*Config, error) {
	var l string
	var ok bool
	if l, ok = os.LookupEnv("LABEL_SELECTOR"); !ok {
		return nil, fmt.Errorf("please export LABEL_SELECTOR")
	}

	config := &Config{
		LabelSelector:     l,
		Namespace:         "default",
		Address:           "localhost:27017",
		LabelAll:          false,
		LogLevel:          phuslog.InfoLevel,
		K8sRequestTimeout: defaultK8sRequestTimeout,
	}

	if l, ok = os.LookupEnv("NAMESPACE"); ok {
		config.Namespace = l
	}
	if l, ok = os.LookupEnv("MONGO_ADDRESS"); ok {
		config.Address = l
	}
	if l, ok = os.LookupEnv("LABEL_ALL"); ok {
		parsed, err := strconv.ParseBool(l)
		if err != nil {
			return nil, fmt.Errorf("invalid LABEL_ALL value %q: %w", l, err)
		}
		config.LabelAll = parsed
	}
	if l, ok = os.LookupEnv("DEBUG"); ok {
		parsed, err := strconv.ParseBool(l)
		if err != nil {
			return nil, fmt.Errorf("invalid DEBUG value %q: %w", l, err)
		}
		if parsed {
			config.LogLevel = phuslog.DebugLevel
		}
	}
	if l, ok = os.LookupEnv("K8S_REQUEST_TIMEOUT"); ok {
		parsed, err := time.ParseDuration(l)
		if err != nil {
			return nil, fmt.Errorf("invalid K8S_REQUEST_TIMEOUT value %q: %w", l, err)
		}
		config.K8sRequestTimeout = parsed
	}
	return config, nil
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

// getMongoPrimary resolves the primary pod name from MongoDB. It lazily connects
// a single long-lived client on first use and reuses it on every subsequent tick.
// The mongo-driver Client is concurrency-safe and maintains its own connection
// pool, so connecting/disconnecting on every tick is wasteful.
func (l *Labeler) getMongoPrimary() (string, error) {
	if l.mongoClient == nil {
		clientOptions := options.Client().
			ApplyURI("mongodb://" + l.Config.Address).
			SetDirect(true).
			SetMinPoolSize(1).
			SetMaxPoolSize(1)
		client, err := mongo.Connect(clientOptions)
		if err != nil {
			return "", fmt.Errorf("connect to mongo at %q: %w", l.Config.Address, err)
		}
		l.mongoClient = client
	}

	ctx, cancel := context.WithTimeout(context.Background(), mongoCommandTimeout)
	defer cancel()

	if err := l.mongoClient.Ping(ctx, nil); err != nil {
		return "", fmt.Errorf("ping mongo at %q: %w", l.Config.Address, err)
	}

	var hello bson.M
	if err := l.mongoClient.Database("admin").
		RunCommand(ctx, bson.D{{Key: "hello", Value: 1}}).
		Decode(&hello); err != nil {
		return "", fmt.Errorf("run hello command on mongo at %q: %w", l.Config.Address, err)
	}

	return parsePrimaryPodName(hello)
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
	primaryPodName := strings.Split(host, ".")[0]
	if len(primaryPodName) != 0 {
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
