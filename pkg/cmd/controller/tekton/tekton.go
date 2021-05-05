package tekton

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jenkins-x-plugins/jx-build-controller/pkg/cmd/controller/jx"
	"github.com/jenkins-x-plugins/jx-pipeline/pkg/cloud/buckets"
	"github.com/jenkins-x-plugins/jx-pipeline/pkg/pipelines"
	"github.com/jenkins-x-plugins/jx-pipeline/pkg/tektonlog"
	"github.com/jenkins-x-plugins/jx-secret/pkg/masker"
	"github.com/jenkins-x-plugins/jx-secret/pkg/masker/watcher"
	jxv1 "github.com/jenkins-x/jx-api/v4/pkg/apis/jenkins.io/v1"
	jxVersioned "github.com/jenkins-x/jx-api/v4/pkg/client/clientset/versioned"
	v1 "github.com/jenkins-x/jx-api/v4/pkg/client/clientset/versioned/typed/jenkins.io/v1"
	"github.com/jenkins-x/jx-helpers/v3/pkg/cmdrunner"
	"github.com/jenkins-x/jx-helpers/v3/pkg/gitclient"
	"github.com/jenkins-x/jx-helpers/v3/pkg/gitclient/cli"
	"github.com/jenkins-x/jx-helpers/v3/pkg/kube/naming"
	"github.com/jenkins-x/jx-helpers/v3/pkg/requirements"
	"github.com/jenkins-x/jx-helpers/v3/pkg/stringhelpers"
	"github.com/jenkins-x/jx-logging/v3/pkg/log"
	"github.com/pkg/errors"
	"github.com/tektoncd/pipeline/pkg/apis/pipeline/v1beta1"
	tkversioned "github.com/tektoncd/pipeline/pkg/client/clientset/versioned"
	informersTekton "github.com/tektoncd/pipeline/pkg/client/informers/externalversions"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"gopkg.in/yaml.v2"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
)

// TODO move it to the jx-pipeline project?
const traceIDAnnotationKey = "pipeline.jenkins-x.io/traceID"

type Options struct {
	KubeClient              kubernetes.Interface
	TektonClient            tkversioned.Interface
	JXClient                jxVersioned.Interface
	gitClient               gitclient.Interface
	ActivityCache           *jx.ActivityCache
	Namespace               string
	Masker                  watcher.Options
	WriteLogToBucketTimeout time.Duration
	IsReady                 *atomic.Value
	CommandRunner           cmdrunner.CommandRunner
	bucketURL               string
}

func (o *Options) Start() {
	stop := make(chan struct{})
	defer close(stop)
	defer runtime.HandleCrash()

	req, err := requirements.GetClusterRequirementsConfig(o.GitClient(), o.JXClient)
	if err != nil {
		log.Logger().Fatalf("failed to get cluster requirements: %v", err)
	}

	// sets an empty string if no logs URL exists
	o.bucketURL = req.GetStorageURL("logs")
	if o.bucketURL != "" {
		log.Logger().Infof("long term storage for logs is being used, bucket %s", o.bucketURL)
	} else {
		log.Logger().Info("long term storage for logs is not configured in cluster requirements")
	}

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
			pipelines.DefaultValues(pa)

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

	f := func() (*jxv1.PipelineActivity, error) {
		paItems := o.ActivityCache.List()
		name := pipelines.ToPipelineActivityName(pr, paItems)
		if name == "" {
			return nil, nil
		}

		var err error
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

	return o.retryUpdate(ctx, f, activityInterface, 5)
}

func (o *Options) retryUpdate(ctx context.Context, f func() (*jxv1.PipelineActivity, error), activityInterface v1.PipelineActivityInterface, retryCount int) (*jxv1.PipelineActivity, error) {
	i := 0
	for {
		i++
		pa, err := f()
		name := "nil"
		if pa != nil {
			name = pa.Name
		}
		if err == nil {
			if i > 1 {
				log.Logger().Infof("took %d attempts to update PipelineActivity %s", i, name)
			}
			return pa, nil
		}
		if i >= retryCount {
			return nil, errors.Wrapf(err, "failed to update PipelineActivity %s after %d attempts", name, retryCount)
		}

		if pa != nil {
			pa, err = activityInterface.Get(ctx, name, metav1.GetOptions{})
			if err != nil && apierrors.IsNotFound(err) {
				err = nil
			}
			if err == nil || pa == nil {
				continue
			}
			if err != nil {
				log.Logger().Warningf("Unable to get PipelineActivity %s due to: %s", name, err)
			} else {
				o.ActivityCache.Upsert(pa)
			}
		}
	}
}

// StoreResources stores resources
func (o *Options) StoreResources(ctx context.Context, pr *v1beta1.PipelineRun, activity *jxv1.PipelineActivity, ns string) error {
	// we'll store the logs only of the pipeline is finished
	if !activity.Spec.Status.IsTerminated() {
		return nil
	}
	// but we'll store it only once
	if activity.Spec.BuildLogsURL != "" {
		return nil
	}

	var (
		traceID            = activity.Annotations[traceIDAnnotationKey]
		needToStoreTraceID bool
	)
	if traceID == "" {
		traceID = generatePipelineTrace(ctx, activity)
		if traceID != "" {
			log.Logger().WithField("traceID", traceID).Infof("Published trace/spans for PipelineActivity %s in namespace %s", activity.Name, activity.Namespace)
			activity.Annotations[traceIDAnnotationKey] = traceID
			needToStoreTraceID = true
		}
	}

	// if long term storage is disabled, we have nothing more to do
	if o.bucketURL == "" {
		// except maybe patch the pipelineactivity with our newly generated traceID
		if needToStoreTraceID {
			patch := fmt.Sprintf(`{"metadata": {"annotations": {"%s": "%s"}}}`, traceIDAnnotationKey, traceID)
			_, err := o.JXClient.JenkinsV1().PipelineActivities(ns).Patch(ctx, activity.Name, types.MergePatchType, []byte(patch), metav1.PatchOptions{})
			if err != nil {
				return errors.Wrapf(err, "failed to patch PipelineActivity %s", activity.Name)
			}
		}
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
	logsFileName := filepath.Join(pathDir, buildNumber+".log")
	activityFileName := filepath.Join(pathDir, buildNumber+".yaml")
	pipelineRunFileName := filepath.Join("jenkins-x", "pipelineruns", pr.Namespace, pr.Name+".yaml")

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

	err := buckets.WriteBucket(ctx, o.bucketURL, logsFileName, reader, o.WriteLogToBucketTimeout)
	if err != nil {
		return errors.Wrapf(err, "failed to write to bucket %s file %s", o.bucketURL, logsFileName)
	}
	log.Logger().Infof("wrote file %s to bucket %s", logsFileName, o.bucketURL)
	activity.Spec.BuildLogsURL = stringhelpers.UrlJoin(o.bucketURL, logsFileName)

	activityYAML, err := yaml.Marshal(activity)
	if err != nil {
		return errors.Wrap(err, "failed to marshal activity to YAML")
	}

	err = buckets.WriteBucket(ctx, o.bucketURL, activityFileName, bytes.NewReader(activityYAML), o.WriteLogToBucketTimeout)
	if err != nil {
		return errors.Wrapf(err, "failed to write to bucket %s file %s", o.bucketURL, activityFileName)
	}
	log.Logger().Infof("wrote file %s to bucket %s", activityFileName, o.bucketURL)

	prYAML, err := yaml.Marshal(pr)
	if err != nil {
		return errors.Wrap(err, "failed to marshal pipelineRun to YAML")
	}

	err = buckets.WriteBucket(ctx, o.bucketURL, pipelineRunFileName, bytes.NewReader(prYAML), o.WriteLogToBucketTimeout)
	if err != nil {
		return errors.Wrapf(err, "failed to write to bucket %s file %s", o.bucketURL, pipelineRunFileName)
	}
	log.Logger().Infof("wrote file %s to bucket %s", pipelineRunFileName, o.bucketURL)

	// lets reload the activity to ensure we are on the latest version
	previousStatus := activity.Spec.Status
	activity, err = o.JXClient.JenkinsV1().PipelineActivities(ns).Get(ctx, activity.Name, metav1.GetOptions{})
	if err != nil {
		return errors.Wrapf(err, "failed to load PipelineActivity %s", activity.Name)
	}
	// lets update the status as we may have detected the pod has gone
	activity.Spec.Status = previousStatus
	activity.Spec.BuildLogsURL = stringhelpers.UrlJoin(o.bucketURL, logsFileName)
	activity.Annotations[traceIDAnnotationKey] = traceID
	_, err = o.JXClient.JenkinsV1().PipelineActivities(ns).Update(ctx, activity, metav1.UpdateOptions{})
	if err != nil {
		return errors.Wrapf(err, "failed to update PipelineActivity %s", activity.Name)
	}
	log.Logger().Infof("updated PipelineActivity %s with new build logs URL %s", activity.Name, activity.Spec.BuildLogsURL)
	return nil
}

func (o *Options) GitClient() gitclient.Interface {
	if o.gitClient == nil {
		o.gitClient = cli.NewCLIClient("", o.CommandRunner)
	}
	return o.gitClient
}

func streamMaskedRunningBuildLogs(tl *tektonlog.TektonLogger, activity *jxv1.PipelineActivity, pr *v1beta1.PipelineRun, buildName string, logMasker *masker.Client) io.ReadCloser {
	prList := []*v1beta1.PipelineRun{pr}
	reader, writer := io.Pipe()
	go func() {
		var err error
		for l := range tl.GetRunningBuildLogs(context.TODO(), activity, prList, buildName) {
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

// generatePipelineTrace generates a logical trace (stages/steps) for the given pipeline activity
// and returns the traceID - or an empty string if tracing is disabled
func generatePipelineTrace(ctx context.Context, activity *jxv1.PipelineActivity) string {
	var (
		tracer             = otel.Tracer("")
		pipelineAttributes = []attribute.KeyValue{
			attribute.Key("pipeline.owner").String(activity.RepositoryOwner()),
			attribute.Key("pipeline.repository").String(activity.RepositoryName()),
			attribute.Key("pipeline.branch").String(activity.BranchName()),
			attribute.Key("pipeline.build").String(activity.Spec.Build),
			attribute.Key("pipeline.context").String(activity.Spec.Context),
			attribute.Key("pipeline.author").String(activity.Spec.Author),
			attribute.Key("pipeline.pulltitle").String(activity.Spec.PullTitle),
		}
	)

	ctx, rootSpan := tracer.Start(ctx, activity.Name,
		trace.WithNewRoot(),
		trace.WithTimestamp(activity.Spec.StartedTimestamp.Time),
		trace.WithAttributes(pipelineAttributes...),
	)

	if !rootSpan.SpanContext().TraceID().IsValid() {
		// if the traceID is invalid, we're using the no-op tracer, because tracing is not enabled
		// let's avoid doing more work, and return now
		return ""
	}

	for i := range activity.Spec.Steps {
		stageStep := activity.Spec.Steps[i]
		if stageStep.Stage == nil {
			continue
		}
		stage := stageStep.Stage

		// ignore pending/running stages, which don't have started/completed timestamps
		if !stage.Status.IsTerminated() {
			continue
		}
		if stage.StartedTimestamp == nil {
			continue
		}

		stageCtx, stageSpan := tracer.Start(ctx, stage.Name,
			trace.WithTimestamp(stage.StartedTimestamp.Time),
			trace.WithAttributes(pipelineAttributes...),
		)

		for j := range stage.Steps {
			step := stage.Steps[j]

			// ignore pending/running steps, which don't have started/completed timestamps
			if !step.Status.IsTerminated() {
				continue
			}
			if step.StartedTimestamp == nil {
				continue
			}

			_, stepSpan := tracer.Start(stageCtx, step.Name,
				trace.WithTimestamp(step.StartedTimestamp.Time),
				trace.WithAttributes(pipelineAttributes...),
			)
			switch step.Status {
			case jxv1.ActivityStatusTypeFailed:
				stepSpan.SetStatus(codes.Error, stage.Description)
				stepSpan.RecordError(errors.New(stage.Description))
			default:
				stepSpan.SetStatus(codes.Ok, step.Description)
			}
			if step.CompletedTimestamp == nil {
				step.CompletedTimestamp = stage.CompletedTimestamp
			}
			if step.CompletedTimestamp == nil {
				step.CompletedTimestamp = activity.Spec.CompletedTimestamp
			}
			stepSpan.End(
				trace.WithTimestamp(step.CompletedTimestamp.Time),
			)
		}

		switch stage.Status {
		case jxv1.ActivityStatusTypeFailed:
			stageSpan.SetStatus(codes.Error, stage.Description)
			stageSpan.RecordError(errors.New(stage.Description))
		default:
			stageSpan.SetStatus(codes.Ok, stage.Description)
		}
		if stage.CompletedTimestamp == nil {
			stage.CompletedTimestamp = activity.Spec.CompletedTimestamp
		}
		stageSpan.End(
			trace.WithTimestamp(stage.CompletedTimestamp.Time),
		)
	}

	switch activity.Spec.Status {
	case jxv1.ActivityStatusTypeFailed:
		rootSpan.SetStatus(codes.Error, "pipeline failed")
	default:
		rootSpan.SetStatus(codes.Ok, "")
	}

	rootSpan.End(
		trace.WithTimestamp(activity.Spec.CompletedTimestamp.Time),
	)

	return rootSpan.SpanContext().TraceID().String()
}
