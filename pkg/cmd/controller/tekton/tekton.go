package tekton

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jenkins-x/jx-secret/pkg/masker/watcher"

	"k8s.io/client-go/kubernetes"

	jxv1 "github.com/jenkins-x/jx-api/v3/pkg/apis/jenkins.io/v1"
	"github.com/jenkins-x/jx-api/v3/pkg/config"
	"github.com/jenkins-x/jx-helpers/v3/pkg/kube"
	"github.com/jenkins-x/jx-helpers/v3/pkg/kube/naming"
	"github.com/jenkins-x/jx-helpers/v3/pkg/stringhelpers"
	"github.com/jenkins-x/jx-pipeline/pkg/cloud/buckets"
	"github.com/jenkins-x/jx-pipeline/pkg/pipelines"
	"github.com/jenkins-x/jx-pipeline/pkg/tektonlog"
	"github.com/jenkins-x/jx-secret/pkg/masker"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	jxVersioned "github.com/jenkins-x/jx-api/v3/pkg/client/clientset/versioned"
	"github.com/jenkins-x/jx-logging/v3/pkg/log"
	"github.com/pkg/errors"
	"github.com/tektoncd/pipeline/pkg/apis/pipeline/v1beta1"
	tkversioned "github.com/tektoncd/pipeline/pkg/client/clientset/versioned"
	informersTekton "github.com/tektoncd/pipeline/pkg/client/informers/externalversions"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/tools/cache"
)

type Options struct {
	KubeClient              kubernetes.Interface
	TektonClient            tkversioned.Interface
	JXClient                jxVersioned.Interface
	EnvironmentCache        map[string]*jxv1.Environment
	Namespace               string
	Masker                  watcher.Options
	WriteLogToBucketTimeout time.Duration
	IsReady                 *atomic.Value
}

func (o *Options) Start() {
	stop := make(chan struct{})
	defer close(stop)
	defer runtime.HandleCrash()

	informerFactoryTekton := informersTekton.NewSharedInformerFactoryWithOptions(
		o.TektonClient,
		time.Minute*10,
		informersTekton.WithNamespace(o.Namespace),
	)

	pipelineRunInformer := informerFactoryTekton.Tekton().V1beta1().PipelineRuns().Informer()
	pipelineRunInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			e := obj.(*v1beta1.PipelineRun)
			o.onPipelineRun(obj, o.Namespace)
			log.Logger().Infof("added %s", e.Name)
		},
		UpdateFunc: func(old, new interface{}) {
			e := new.(*v1beta1.PipelineRun)
			o.onPipelineRun(new, o.Namespace)
			log.Logger().Infof("updated %s", e.Name)
		},
	})

	// Starts all the shared informers that have been created by the factory so
	// far.

	informerFactoryTekton.Start(stop)
	// wait for the initial synchronization of the local cache
	if !cache.WaitForCacheSync(stop, pipelineRunInformer.HasSynced) {
		runtime.HandleError(fmt.Errorf("timed out waiting for tekton caches to sync"))
	}
	o.IsReady.Store(true)

	<-stop

	// Wait forever
	select {}
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

	myMasker := o.Masker.GetClient()
	reader := streamMaskedRunningBuildLogs(&tektonLogger, activity, pr, buildName, myMasker)
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
