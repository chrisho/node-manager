//go:generate go run pkg/codegen/cleanup/main.go
//go:generate go run pkg/codegen/main.go
//go:generate /bin/bash scripts/generate-manifest

package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/ehazlett/simplelog"
	ctlnode "github.com/rancher/wrangler/pkg/generated/controllers/core"
	"github.com/rancher/wrangler/pkg/signals"
	"github.com/rancher/wrangler/pkg/start"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli/v2"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog"

	"github.com/harvester/node-manager/pkg/controller/ksmtuned"
	ctlksmtuned "github.com/harvester/node-manager/pkg/generated/controllers/node.harvesterhci.io"
	"github.com/harvester/node-manager/pkg/metrics"
	"github.com/harvester/node-manager/pkg/option"
	"github.com/harvester/node-manager/pkg/version"
)

var (
	VERSION = "v0.0.0-dev"
)

func main() {
	var opt option.Option

	app := cli.NewApp()
	app.Name = "harvester-node-manager"
	app.Version = VERSION
	app.Usage = "Harvester Node Manager, to help with cluster node configuration. Options kubeconfig or masterurl are required."
	app.Flags = []cli.Flag{
		&cli.StringFlag{
			Name:        "kubeconfig, k",
			EnvVars:     []string{"KUBECONFIG"},
			Value:       "",
			Usage:       "Kubernetes config files, e.g. $HOME/.kube/config",
			Destination: &opt.KubeConfig,
		},
		&cli.StringFlag{
			Name:        "node, n",
			EnvVars:     []string{"NODENAME"},
			Value:       "",
			Usage:       "Specify the node name",
			Destination: &opt.NodeName,
		},
		&cli.StringFlag{
			Name:        "profile-listen-address",
			Value:       "0.0.0.0:6060",
			DefaultText: "0.0.0.0:6060",
			Usage:       "Address to listen on for profiling",
			Destination: &opt.ProfilerAddress,
		},
		&cli.StringFlag{
			Name:        "log-format",
			EnvVars:     []string{"NDM_LOG_FORMAT"},
			Usage:       "Log format",
			Value:       "text",
			DefaultText: "text",
			Destination: &opt.LogFormat,
		},
		&cli.BoolFlag{
			Name:        "trace",
			EnvVars:     []string{"TRACE"},
			Usage:       "Run trace logs",
			Destination: &opt.Trace,
		},
		&cli.BoolFlag{
			Name:        "debug",
			EnvVars:     []string{"DEBUG"},
			Usage:       "enable debug logs",
			Destination: &opt.Debug,
		},
		&cli.IntFlag{
			Name:        "threadiness",
			Value:       2,
			DefaultText: "2",
			Destination: &opt.Threadiness,
		},
	}

	app.Action = func(c *cli.Context) error {
		initProfiling(&opt)
		initLogs(&opt)
		return run(c, &opt)
	}

	if err := app.Run(os.Args); err != nil {
		klog.Fatal(err)
	}
}

func initProfiling(opt *option.Option) {
	// enable profiler
	if opt.ProfilerAddress != "" {
		go func() {
			log.Println(http.ListenAndServe(opt.ProfilerAddress, nil))
		}()
	}
}

func initLogs(opt *option.Option) {
	switch opt.LogFormat {
	case "simple":
		logrus.SetFormatter(&simplelog.StandardFormatter{})
	case "json":
		logrus.SetFormatter(&logrus.JSONFormatter{})
	default:
		logrus.SetFormatter(&logrus.TextFormatter{})
	}
	logrus.SetOutput(os.Stdout)
	logrus.Infof("Ksmtuned controller %s is starting", version.FriendlyVersion())
	if opt.Debug {
		logrus.SetLevel(logrus.DebugLevel)
		logrus.Debugf("Loglevel set to [%v]", logrus.DebugLevel)
	}
	if opt.Trace {
		logrus.SetLevel(logrus.TraceLevel)
		logrus.Tracef("Loglevel set to [%v]", logrus.TraceLevel)
	}
}

func run(c *cli.Context, opt *option.Option) error {
	ctx := signals.SetupSignalContext()

	cfg, err := clientcmd.BuildConfigFromFlags(opt.MasterURL, opt.KubeConfig)
	if err != nil {
		klog.Fatalf("Error building config from flags: %s", err.Error())
	}

	nodes, err := ctlnode.NewFactoryFromConfig(cfg)
	if err != nil {
		return fmt.Errorf("error building node controllers: %s", err.Error())
	}

	ksmtuneds, err := ctlksmtuned.NewFactoryFromConfig(cfg)
	if err != nil {
		return fmt.Errorf("error building harvester-node-manager controllers: %s", err.Error())
	}

	var ksmtunedController *ksmtuned.Controller
	run := func(ctx context.Context) {
		kts := ksmtuneds.Node().V1beta1().Ksmtuned()
		nds := nodes.Core().V1().Node()
		if ksmtunedController, err = ksmtuned.Register(
			ctx,
			opt.NodeName,
			kts,
			nds,
		); err != nil {
			logrus.Fatalf("failed to register ksmtuned controller: %s", err)
		}

		if err := start.All(ctx, opt.Threadiness, ksmtuneds, nodes); err != nil {
			logrus.Fatalf("error starting, %s", err.Error())
		}
	}

	go metrics.Run()

	run(ctx)

	<-ctx.Done()

	return ksmtunedController.Ksmtuned.Stop()
}
