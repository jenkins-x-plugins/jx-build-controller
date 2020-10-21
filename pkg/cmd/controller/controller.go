package controller

import (
	"fmt"
	"time"

	"github.com/jenkins-x-plugins/jx-build-controller/pkg/cmd/controller/tekton"

	"github.com/jenkins-x-plugins/jx-build-controller/pkg/cmd/controller/jx"

	"github.com/jenkins-x-plugins/jx-build-controller/pkg/common"
	jxv1 "github.com/jenkins-x/jx-api/v3/pkg/apis/jenkins.io/v1"
	"github.com/jenkins-x/jx-api/v3/pkg/client/clientset/versioned"
	"github.com/jenkins-x/jx-helpers/v3/pkg/cobras/helper"
	"github.com/jenkins-x/jx-helpers/v3/pkg/cobras/templates"
	"github.com/jenkins-x/jx-helpers/v3/pkg/kube"
	"github.com/jenkins-x/jx-helpers/v3/pkg/kube/jxclient"
	"github.com/jenkins-x/jx-helpers/v3/pkg/options"
	"github.com/jenkins-x/jx-helpers/v3/pkg/termcolor"
	"github.com/jenkins-x/jx-kube-client/v3/pkg/kubeclient"
	"github.com/jenkins-x/jx-logging/v3/pkg/log"
	"github.com/jenkins-x/jx-secret/pkg/masker/watcher"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	tektonclient "github.com/tektoncd/pipeline/pkg/client/clientset/versioned"
	"k8s.io/client-go/kubernetes"
)

var (
	info = termcolor.ColorInfo

	cmdLong = templates.LongDesc(`
		Promotes a version of an application to an Environment
`)

	cmdExample = templates.Examples(`
		# promotes your current app to the staging environment
		%s 
	`)
)

// Options the options for this command
type Options struct {
	options.BaseOptions

	Namespace               string
	OperatorNamespace       string
	WriteLogToBucketTimeout time.Duration
	KubeClient              kubernetes.Interface
	JXClient                versioned.Interface
	TektonClient            tektonclient.Interface
	EnvironmentCache        map[string]*jxv1.Environment
	Masker                  watcher.Options
}

// NewCmdController creates a command object for the command
func NewCmdController() (*cobra.Command, *Options) {
	options := &Options{}

	cmd := &cobra.Command{
		Use:     "run",
		Short:   "Promotes a version of an application to an Environment",
		Long:    cmdLong,
		Example: fmt.Sprintf(cmdExample, common.BinaryName),
		Run: func(cmd *cobra.Command, args []string) {
			err := options.Run()
			helper.CheckErr(err)
		},
	}

	cmd.Flags().StringVarP(&options.Namespace, "namespace", "n", "", "The kubernetes Namespace to watch for PipelineRun and PipelineActivity resources. Defaults to the current namespace")
	cmd.Flags().StringVarP(&options.OperatorNamespace, "operator-namespace", "", "jx-git-operator", "The git operator namespace")
	cmd.Flags().DurationVarP(&options.WriteLogToBucketTimeout, "write-log-timeout", "", time.Minute*30, "The timeout for writing pipeline logs to the bucket")

	options.BaseOptions.AddBaseFlags(cmd)
	return cmd, options
}

// Validate verifies things are setup correctly
func (o *Options) Validate() error {
	var err error
	o.KubeClient, o.Namespace, err = kube.LazyCreateKubeClientAndNamespace(o.KubeClient, o.Namespace)
	if err != nil {
		return errors.Wrapf(err, "failed to create kube client")
	}
	o.JXClient, err = jxclient.LazyCreateJXClient(o.JXClient)
	if err != nil {
		return errors.Wrapf(err, "failed to create the jx client")
	}
	if o.TektonClient == nil {
		f := kubeclient.NewFactory()
		cfg, err := f.CreateKubeConfig()
		if err != nil {
			return errors.Wrap(err, "failed to get kubernetes config")
		}
		o.TektonClient, err = tektonclient.NewForConfig(cfg)
		if err != nil {
			return errors.Wrap(err, "error building tekton client")
		}
	}
	if o.EnvironmentCache == nil {
		o.EnvironmentCache = map[string]*jxv1.Environment{}
	}
	o.Masker.KubeClient = o.KubeClient
	o.Masker.Namespaces = []string{o.Namespace, o.OperatorNamespace}
	err = o.Masker.Validate()
	if err != nil {
		return errors.Wrapf(err, "failed to validate secret masker")
	}
	return nil
}

func (o *Options) Run() error {
	err := o.Validate()
	if err != nil {
		return errors.Wrapf(err, "failed to validate options")
	}

	log.Logger().Info("starting build controller")
	ns := o.Namespace

	// todo add readiness check
	// todo add health check
	// todo add resyncInterval

	go func() {
		(&jx.Options{
			JXClient:         o.JXClient,
			Namespace:        ns,
			Masker:           o.Masker,
			EnvironmentCache: o.EnvironmentCache,
		}).Start()
	}()

	go func() {
		(&tekton.Options{
			TektonClient: o.TektonClient,
			KubeClient:   o.KubeClient,
			Namespace:    ns,
		}).Start()
	}()

	return nil
}
