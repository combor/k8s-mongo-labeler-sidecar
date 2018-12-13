package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mongodb/mongo-go-driver/x/network/address"
	"github.com/mongodb/mongo-go-driver/x/network/command"
	"github.com/mongodb/mongo-go-driver/x/network/connection"
	"github.com/mongodb/mongo-go-driver/x/network/wiremessage"
	"github.com/sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

type Config struct {
	LabelSelector string
	Namespace     string
	LogLevel      logrus.Level
}

type Labeler struct {
	Config          *Config
	K8scli          *kubernetes.Clientset
	MongoConnection connection.Connection
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
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var addr address.Address
	addr = "localhost:27017"
	c, _, err := connection.New(ctx, addr)
	if err != nil {
		return nil, err
	}
	return &Labeler{
		Config:          config,
		K8scli:          k8scli,
		MongoConnection: c,
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
	pods, err := l.K8scli.CoreV1().Pods(l.Config.Namespace).List(listOptions)
	if err != nil {
		return err
	}

	for _, pod := range pods.Items {
		if pod.GetName() == primary {
			logrus.Infof("Seting primary to %s", primary)
			labels := pod.GetLabels()
			labels["primary"] = "true"
			logrus.Debugf("Setting labels %v", labels)
			pod.SetLabels(labels)
			l.K8scli.CoreV1().Pods(l.Config.Namespace).Update(&pod)
			return nil
		}
	}
	return fmt.Errorf("Primary not found")
}

func getConfigFromEnvironment() (*Config, error) {
	var l string
	var ok bool
	if l, ok = os.LookupEnv("LABEL_SELECTOR"); !ok {
		return nil, fmt.Errorf("Please export LABEL_SELECTOR")
	}

	config := &Config{
		LabelSelector: l,
		Namespace:     os.Getenv("NAMESPACE"),
		LogLevel:      logrus.InfoLevel,
	}
	if _, ok = os.LookupEnv("DEBUG"); !ok {
		config.LogLevel = logrus.DebugLevel
	}
	return config, nil
}

func getKubeClientSet() (*kubernetes.Clientset, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, err
	}
	return kubernetes.NewForConfig(config)
}

func (l *Labeler) getMongoPrimary() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	isMaster, err := (&command.IsMaster{}).Encode()
	err = l.MongoConnection.WriteWireMessage(ctx, isMaster)
	if err != nil {
		return "", err
	}
	wm, err := l.MongoConnection.ReadWireMessage(ctx)
	if err != nil {
		return "", err
	}
	reply := wm.(wiremessage.Reply)
	doc, err := reply.GetMainDocument()
	if primaryHost, ok := doc.Lookup("primary").StringValueOK(); ok {
		primary := strings.Split(primaryHost, ".")[0]
		if len(primary) != 0 {
			logrus.Debugf("Primary host %s", primary)
			return primary, nil
		}
	}
	return "", fmt.Errorf("Can't find primary server.")
}

func main() {
	labeler, err := New()
	if err != nil {
		logrus.Fatal(err)
	}
	logrus.SetLevel(labeler.Config.LogLevel)
	logrus.Infof("Setting logging level to %s", labeler.Config.LogLevel.String())

	ticker := time.NewTicker(5 * time.Second).C
	done := make(chan bool)
	for {
		select {
		case <-ticker:
			err := labeler.setPrimaryLabel()
			if err != nil {
				logrus.Debug(err)
			}
		case <-done:
			logrus.Info("Done")
			return
		}
	}

}
