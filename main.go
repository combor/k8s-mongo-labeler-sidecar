package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
	LabelSelector string
	Namespace     string
	Address       string
	LabelAll      bool
	LogLevel      phuslog.Level
}

type Labeler struct {
	Config *Config
	K8scli *kubernetes.Clientset
}

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
	k8scli, err := getKubeClientSet()
	if err != nil {
		return nil, err
	}
	return &Labeler{
		Config: config,
		K8scli: k8scli,
	}, nil

}

func (l *Labeler) setPrimaryLabel() error {
	primaryPodName, err := l.getMongoPrimary()
	if err != nil {
		return err
	}
	podsClient := l.K8scli.CoreV1().Pods(l.Config.Namespace)
	pods, err := podsClient.List(context.Background(), metav1.ListOptions{LabelSelector: l.Config.LabelSelector})
	if err != nil {
		return err
	}
	foundPrimary := false
	phuslog.Debug().Msgf("Found %d pods", len(pods.Items))
	for _, pod := range pods.Items {
		currentPodIsPrimary := false
		currentPodName := pod.GetName()
		if currentPodName == primaryPodName {
			currentPodIsPrimary = true
			foundPrimary = true
			if pod.Labels["primary"] != "true" {
				phuslog.Info().Msgf("Setting primary to true for pod %s", primaryPodName)
			}
		}
		removePrimaryLabel := !currentPodIsPrimary && !l.Config.LabelAll
		patchBytes, err := json.Marshal(primaryLabelPatch(currentPodIsPrimary, removePrimaryLabel))
		if err != nil {
			return err
		}

		phuslog.Debug().Msgf("Patching pod %s with: %s", currentPodName, string(patchBytes))
		_, err = podsClient.Patch(
			context.Background(),
			currentPodName,
			types.StrategicMergePatchType,
			patchBytes,
			metav1.PatchOptions{},
		)
		if err != nil {
			return err
		}
	}
	if !foundPrimary {
		return fmt.Errorf("primary not found")
	}
	return nil
}

func primaryLabelPatch(value bool, remove bool) map[string]interface{} {
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
		LabelSelector: l,
		Namespace:     "default",
		Address:       "localhost:27017",
		LabelAll:      false,
		LogLevel:      phuslog.InfoLevel,
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

	var kubeconfig *string
	if home := homeDir(); home != "" {
		kubeconfig = flag.String("kubeconfig", filepath.Join(home, ".kube", "config"), "(optional) absolute path to the kubeconfig file")
	} else {
		kubeconfig = flag.String("kubeconfig", "", "absolute path to the kubeconfig file")
	}
	flag.Parse()

	// use the current context in kubeconfig
	config, err := clientcmd.BuildConfigFromFlags("", *kubeconfig)
	if err != nil {
		return nil, err
	}
	return kubernetes.NewForConfig(config)
}

func homeDir() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	return os.Getenv("USERPROFILE") // windows
}

func (l *Labeler) getMongoPrimary() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	clientOptions := options.Client().
		ApplyURI(fmt.Sprintf("mongodb://%s", l.Config.Address)).
		SetDirect(true)
	client, err := mongo.Connect(clientOptions)
	if err != nil {
		return "", err
	}
	defer func() {
		err := client.Disconnect(ctx)
		if err != nil {
			phuslog.Debug().Msgf("unable to close mongo connection: %v", err)
		}
	}()
	if err = client.Ping(ctx, nil); err != nil {
		return "", err
	}

	var hello bson.M
	err = client.Database("admin").
		RunCommand(ctx, bson.D{{Key: "hello", Value: 1}}).
		Decode(&hello)
	if err != nil {
		return "", err
	}

	hostsValue, ok := hello["hosts"]
	if !ok {
		return "", fmt.Errorf("no hosts found for replica")
	}

	hosts, ok := hostsValue.(bson.A)
	if !ok {
		return "", fmt.Errorf("invalid hosts type %T", hostsValue)
	}
	phuslog.Debug().Msgf("Hosts %v", hosts)

	primaryHost, _ := hello["primary"].(string)
	if primaryHost == "" {
		if isWritablePrimary, ok := hello["isWritablePrimary"].(bool); ok && isWritablePrimary {
			primaryHost, _ = hello["me"].(string)
		}
	}
	if primaryHost == "" {
		// Older MongoDB versions may expose "ismaster" instead of "isWritablePrimary".
		if isMaster, ok := hello["ismaster"].(bool); ok && isMaster {
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
	phuslog.Info().Msgf("Setting logging level to %s", config.LogLevel.String())

	labeler, err := New(config)
	if err != nil {
		phuslog.Fatal().Err(err).Msg("failed to initialize labeler")
	}

	ticker := time.NewTicker(5 * time.Second)
	tickCh := ticker.C
	done := make(chan bool)
	for {
		select {
		case <-tickCh:
			err := labeler.setPrimaryLabel()
			if err != nil {
				phuslog.Error().Err(err).Msg("failed to set primary label")
			}
		case <-done:
			phuslog.Info().Msg("Done")
			return
		}
	}
}
