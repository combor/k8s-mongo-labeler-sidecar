package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mongodb/mongo-go-driver/x/bsonx"
	"github.com/mongodb/mongo-go-driver/x/network/address"
	"github.com/mongodb/mongo-go-driver/x/network/command"
	"github.com/mongodb/mongo-go-driver/x/network/connection"
	"github.com/mongodb/mongo-go-driver/x/network/wiremessage"
	"github.com/sirupsen/logrus"
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
	LogLevel      logrus.Level
}

type Labeler struct {
	Config *Config
	K8scli *kubernetes.Clientset
}

func New() (*Labeler, error) {
	config, err := getConfigFromEnvironment()
	if err != nil {
		return nil, err
	}
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
	primary, err := l.getMongoPrimary()
	if err != nil {
		return err
	}
	listOptions := metav1.ListOptions{
		LabelSelector: l.Config.LabelSelector,
	}
	pods, err := l.K8scli.CoreV1().Pods(l.Config.Namespace).List(context.Background(), listOptions)
	if err != nil {
		return err
	}
	var found bool
	logrus.Debugf("Found %d pods", len(pods.Items))
	for _, pod := range pods.Items {
		name := pod.GetName()
		var patchData map[string]interface{}

		if name == primary {
			if pod.Labels["primary"] != "true" {
				logrus.Infof("Setting primary to true for pod %s", name)
			}
			patchData = map[string]interface{}{
				"metadata": map[string]interface{}{
					"labels": map[string]string{
						"primary": "true",
					},
				},
			}
			found = true
		} else {
			if l.Config.LabelAll {
				if pod.Labels["primary"] != "false" {
					logrus.Infof("Setting primary to false for pod %s", name)
				}
				patchData = map[string]interface{}{
					"metadata": map[string]interface{}{
						"labels": map[string]string{
							"primary": "false",
						},
					},
				}
			} else {
				// To remove a label, set it to null in strategic merge patch
				patchData = map[string]interface{}{
					"metadata": map[string]interface{}{
						"labels": map[string]interface{}{
							"primary": nil,
						},
					},
				}
			}
		}

		patchBytes, err := json.Marshal(patchData)
		if err != nil {
			return err
		}

		logrus.Debugf("Patching pod %s with: %s", name, string(patchBytes))
		_, err = l.K8scli.CoreV1().Pods(l.Config.Namespace).Patch(
			context.Background(),
			name,
			types.StrategicMergePatchType,
			patchBytes,
			metav1.PatchOptions{},
		)
		if err != nil {
			return err
		}
	}
	if !found {
		return fmt.Errorf("primary not found")
	}
	return nil
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
		LogLevel:      logrus.InfoLevel,
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
			config.LogLevel = logrus.DebugLevel
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
	addr := address.Address(l.Config.Address)
	c, _, err := connection.New(ctx, addr)
	if err != nil {
		return "", err
	}
	defer func(c connection.Connection) {
		err := c.Close()
		if err != nil {
			_ = fmt.Errorf("not posiblie to close connection")
		}
	}(c)

	isMaster, err := (&command.IsMaster{}).Encode()
	if err != nil {
		return "", err
	}
	err = c.WriteWireMessage(ctx, isMaster)
	if err != nil {
		return "", err
	}
	wm, err := c.ReadWireMessage(ctx)
	if err != nil {
		return "", err
	}
	reply := wm.(wiremessage.Reply)
	doc, err := reply.GetMainDocument()
	if err != nil {
		return "", err
	}
	var hosts bsonx.Arr
	var ok bool
	if hosts, ok = doc.Lookup("hosts").ArrayOK(); !ok {
		return "", fmt.Errorf("no hosts found for replica")
	}
	logrus.Debugf("Hosts %s", hosts)
	if primaryHost, ok := doc.Lookup("primary").StringValueOK(); ok {
		primary := strings.Split(primaryHost, ".")[0]
		if len(primary) != 0 {
			return primary, nil
		}
	}
	return "", fmt.Errorf("can't find primary server")
}

func main() {
	labeler, err := New()
	if err != nil {
		logrus.Fatal(err)
	}
	logrus.SetLevel(labeler.Config.LogLevel)
	logrus.Infof("Setting logging level to %s", labeler.Config.LogLevel.String())

	ticker := time.NewTicker(5 * time.Second)
	tickCh := ticker.C
	done := make(chan bool)
	for {
		select {
		case <-tickCh:
			err := labeler.setPrimaryLabel()
			if err != nil {
				logrus.Error(err)
			}
		case <-done:
			logrus.Info("Done")
			return
		}
	}
}
