package controller

import (
	"context"
	"fmt"
	"reflect"
	"time"

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
	"github.com/jenkins-x/jx-pipeline/pkg/pipelines"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"github.com/tektoncd/pipeline/pkg/apis/pipeline/v1beta1"
	tektonclient "github.com/tektoncd/pipeline/pkg/client/clientset/versioned"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
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

	Namespace    string
	KubeClient   kubernetes.Interface
	JXClient     versioned.Interface
	TektonClient tektonclient.Interface
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
	return nil
}

func (o *Options) Run() error {
	err := o.Validate()
	if err != nil {
		return errors.Wrapf(err, "failed to validate options")
	}

	log.Logger().Info("starting build controller")

	ns := o.Namespace
	pr := &v1beta1.PipelineRun{}
	log.Logger().Infof("Watching for PipelineRun resources in namespace %s", info(ns))
	listWatch := cache.NewListWatchFromClient(o.TektonClient.TektonV1beta1().RESTClient(), "pipelineruns", ns, fields.Everything())
	kube.SortListWatchByName(listWatch)
	_, controller := cache.NewInformer(
		listWatch,
		pr,
		time.Minute*10,
		cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				o.onPipelineRun(obj, ns)
			},
			UpdateFunc: func(oldObj, newObj interface{}) {
				o.onPipelineRun(newObj, ns)
			},
			DeleteFunc: func(obj interface{}) {
			},
		},
	)

	stop := make(chan struct{})
	go controller.Run(stop)

	// Wait forever
	select {}

	return nil
}

func (o *Options) onPipelineRun(obj interface{}, ns string) {
	pr, ok := obj.(*v1beta1.PipelineRun)
	if !ok {
		log.Logger().Infof("Object is not a PipelineRun %#v", obj)
		return
	}
	if pr != nil {
		pa, err := o.OnPipelineRunUpsert(pr, ns)
		if err != nil {
			log.Logger().Warnf("failed to process PipelineRun %s in namespace %s: %s", pr.Name, ns, err.Error())
		}

		if pa != nil {
			err = o.StoreResources(pr, pa, ns)
			if err != nil {
				log.Logger().Warnf("failed to store resources for PipelineActivity %s in namespace %s: %s", pa.Name, ns, err.Error())
			}
		}
	}
}

// OnPipelineRunUpsert lets upsert the associated PipelineActivity
func (o *Options) OnPipelineRunUpsert(pr *v1beta1.PipelineRun, ns string) (*jxv1.PipelineActivity, error) {
	ctx := context.Background()
	activityInterface := o.JXClient.JenkinsV1().PipelineActivities(ns)

	paResources, err := activityInterface.List(ctx, metav1.ListOptions{})
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, errors.Wrapf(err, "failed to list PipelineActivity resources in namespace %s", ns)
		}
	}

	paItems := paResources.Items
	name := pipelines.ToPipelineActivityName(pr, paItems)
	if name == "" {
		return nil, nil
	}

	found := false
	pa := &jxv1.PipelineActivity{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	}
	for i := range paItems {
		r := &paItems[i]
		if r.Name == name {
			found = true
			pa = r
			break
		}
	}
	original := pa.DeepCopy()

	if !found {
		pipelines.ToPipelineActivity(pr, pa)
	} else {
		// lets remove all the steps other than promotions/previews and recreate the others from the PipelineRun
		ps := &pa.Spec
		oldSteps := ps.Steps
		ps.Steps = nil
		pipelines.ToPipelineActivity(pr, pa)

		for _, s := range oldSteps {
			if s.Kind == jxv1.ActivityStepKindTypePreview || s.Kind == jxv1.ActivityStepKindTypePromote {
				ps.Steps = append(ps.Steps, s)
			}
		}
	}

	if found {
		// lets ignore if we don't change it...
		if reflect.DeepEqual(original, pa) {
			return nil, nil
		}
		pa, err = activityInterface.Update(ctx, pa, metav1.UpdateOptions{})
		if err != nil {
			return pa, errors.Wrapf(err, "failed to update PipelineActivity %s in namespace %s", name, ns)
		}
		log.Logger().Infof("updated PipelineActivity %s in namespace %s", name, ns)
		return pa, nil
	}

	pa, err = activityInterface.Create(ctx, pa, metav1.CreateOptions{})
	if err != nil {
		return pa, errors.Wrapf(err, "failed to create PipelineActivity %s in namespace %s", name, ns)
	}
	log.Logger().Infof("created PipelineActivity %s in namespace %s", name, ns)
	return pa, nil
}

// StoreResources stores resources
func (o *Options) StoreResources(pr *v1beta1.PipelineRun, pa *jxv1.PipelineActivity, ns string) error {
	return nil
}
