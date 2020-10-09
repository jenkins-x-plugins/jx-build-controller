package controller

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/jenkins-x-plugins/jx-build-controller/pkg/common"
	jxv1 "github.com/jenkins-x/jx-api/v3/pkg/apis/jenkins.io/v1"
	"github.com/jenkins-x/jx-api/v3/pkg/client/clientset/versioned"
	"github.com/jenkins-x/jx-api/v3/pkg/config"
	"github.com/jenkins-x/jx-helpers/v3/pkg/cobras/helper"
	"github.com/jenkins-x/jx-helpers/v3/pkg/cobras/templates"
	"github.com/jenkins-x/jx-helpers/v3/pkg/kube"
	"github.com/jenkins-x/jx-helpers/v3/pkg/kube/jxclient"
	"github.com/jenkins-x/jx-helpers/v3/pkg/kube/naming"
	"github.com/jenkins-x/jx-helpers/v3/pkg/options"
	"github.com/jenkins-x/jx-helpers/v3/pkg/stringhelpers"
	"github.com/jenkins-x/jx-helpers/v3/pkg/termcolor"
	"github.com/jenkins-x/jx-kube-client/v3/pkg/kubeclient"
	"github.com/jenkins-x/jx-logging/v3/pkg/log"
	"github.com/jenkins-x/jx-pipeline/pkg/cloud/buckets"
	"github.com/jenkins-x/jx-pipeline/pkg/pipelines"
	"github.com/jenkins-x/jx-pipeline/pkg/tektonlog"
	"github.com/jenkins-x/jx-secret/pkg/masker"
	"github.com/jenkins-x/jx-secret/pkg/masker/watcher"
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

	stop := make(chan struct{})

	o.Masker.RunWithChannel(stop)

	env := &jxv1.Environment{}
	log.Logger().Infof("Watching for Environment resources in namespace %s", info(ns))
	listWatch := cache.NewListWatchFromClient(o.JXClient.JenkinsV1().RESTClient(), "environments", ns, fields.Everything())
	kube.SortListWatchByName(listWatch)
	_, envCtrl := cache.NewInformer(
		listWatch,
		env,
		time.Minute*10,
		cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				o.onEnvironment(obj, ns)
			},
			UpdateFunc: func(oldObj, newObj interface{}) {
				o.onEnvironment(newObj, ns)
			},
			DeleteFunc: func(obj interface{}) {
			},
		},
	)
	go envCtrl.Run(stop)

	pr := &v1beta1.PipelineRun{}
	log.Logger().Infof("Watching for PipelineRun resources in namespace %s", info(ns))
	listWatch = cache.NewListWatchFromClient(o.TektonClient.TektonV1beta1().RESTClient(), "pipelineruns", ns, fields.Everything())
	kube.SortListWatchByName(listWatch)
	_, pipelineRunCtrl := cache.NewInformer(
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
	go pipelineRunCtrl.Run(stop)

	// Wait forever
	select {}
	return nil
}

func (o *Options) onEnvironment(obj interface{}, ns string) {
	env, ok := obj.(*jxv1.Environment)
	if !ok {
		log.Logger().Infof("Object is not an Environment %#v", obj)
		return
	}
	if env != nil {
		o.EnvironmentCache[env.Name] = env
	}
}

func (o *Options) onPipelineRun(obj interface{}, ns string) {
	pr, ok := obj.(*v1beta1.PipelineRun)
	if !ok {
		log.Logger().Infof("Object is not a PipelineRun %#v", obj)
		return
	}
	if pr != nil {
		ctx := context.Background()
		pa, err := o.OnPipelineRunUpsert(ctx, pr, ns)
		if err != nil {
			log.Logger().Warnf("failed to process PipelineRun %s in namespace %s: %s", pr.Name, ns, err.Error())
		}

		if pa != nil {
			err = o.StoreResources(ctx, pr, pa, ns)
			if err != nil {
				log.Logger().Warnf("failed to store resources for PipelineActivity %s in namespace %s: %s", pa.Name, ns, err.Error())
			}
		}
	}
}

// OnPipelineRunUpsert lets upsert the associated PipelineActivity
func (o *Options) OnPipelineRunUpsert(ctx context.Context, pr *v1beta1.PipelineRun, ns string) (*jxv1.PipelineActivity, error) {
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

	pipelines.ToPipelineActivity(pr, pa, true)

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
func (o *Options) StoreResources(ctx context.Context, pr *v1beta1.PipelineRun, activity *jxv1.PipelineActivity, ns string) error {
	if !activity.Spec.Status.IsTerminated() {
		return nil
	}

	// lets check if we've stored the pipeline log
	if activity.Spec.BuildLogsURL != "" {
		return nil
	}

	// lets get the build log and store it
	log.Logger().Debugf("Storing build logs for %s", activity.Name)
	envName := kube.LabelValueDevEnvironment
	devEnv := o.EnvironmentCache[envName]
	if devEnv == nil {
		log.Logger().Warnf("No Environment %s found", envName)
		return nil
	}
	settings := &devEnv.Spec.TeamSettings
	requirements, err := config.GetRequirementsConfigFromTeamSettings(settings)
	if err != nil {
		return errors.Wrapf(err, "failed to get requirements from Environment %s", devEnv.Name)
	}
	if requirements == nil {
		return errors.Errorf("no requirements for Environment %s", devEnv.Name)
	}
	bucketURL := requirements.Storage.Logs.URL
	if !requirements.Storage.Logs.Enabled || bucketURL == "" {
		return nil
	}

	owner := activity.RepositoryOwner()
	repository := activity.RepositoryName()
	branch := activity.BranchName()
	buildNumber := activity.Spec.Build
	if buildNumber == "" {
		buildNumber = "1"
	}

	pathDir := filepath.Join("jenkins-x", "logs", owner, repository, branch)
	fileName := filepath.Join(pathDir, buildNumber+".log")

	buildName := fmt.Sprintf("%s/%s/%s #%s",
		naming.ToValidName(owner),
		naming.ToValidName(repository),
		naming.ToValidName(branch),
		strings.ToLower(buildNumber))

	tektonLogger := tektonlog.TektonLogger{
		JXClient:     o.JXClient,
		KubeClient:   o.KubeClient,
		TektonClient: o.TektonClient,
		Namespace:    o.Namespace,
	}

	log.Logger().Debugf("Capturing running build logs for %s", activity.Name)

	masker := o.Masker.GetClient()
	reader := streamMaskedRunningBuildLogs(&tektonLogger, activity, pr, buildName, masker)
	defer reader.Close()

	err = buckets.WriteBucket(ctx, bucketURL, fileName, reader, o.WriteLogToBucketTimeout)
	if err != nil {
		return errors.Wrapf(err, "failed to write to bucket %s file %s", bucketURL, fileName)
	}

	log.Logger().Infof("wrote file %s to bucket %s", fileName, bucketURL)

	activity.Spec.BuildLogsURL = stringhelpers.UrlJoin(bucketURL, fileName)
	_, err = o.JXClient.JenkinsV1().PipelineActivities(ns).Update(ctx, activity, metav1.UpdateOptions{})
	if err != nil {
		return errors.Wrapf(err, "failed to update PipelineActivity %s", activity.Name)
	}
	log.Logger().Infof("updated PipelineActivity %s with new build logs URL %s", activity.Name, activity.Spec.BuildLogsURL)
	return nil
}

func streamMaskedRunningBuildLogs(tl *tektonlog.TektonLogger, activity *jxv1.PipelineActivity, pr *v1beta1.PipelineRun, buildName string, logMasker *masker.Client) io.ReadCloser {
	prList := []*v1beta1.PipelineRun{pr}
	reader, writer := io.Pipe()
	go func() {
		var err error
		for l := range tl.GetRunningBuildLogs(activity, prList, buildName) {
			if err == nil {
				line := l.Line
				if logMasker != nil && l.ShouldMask {
					line = logMasker.Mask(line)
				}
				_, err = writer.Write([]byte(line + "\n"))
			}
		}
		if err == nil {
			err = errors.Wrapf(tl.Err(), "getting logs for build %s", buildName)
		}
		writer.CloseWithError(err) //nolint
	}()
	return reader
}
