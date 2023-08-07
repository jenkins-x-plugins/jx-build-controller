package controller

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jenkins-x-plugins/jx-build-controller/pkg/cmd/controller/handler"
	"github.com/jenkins-x-plugins/jx-build-controller/pkg/cmd/controller/jx"
	"github.com/jenkins-x-plugins/jx-build-controller/pkg/cmd/controller/tekton"
	"github.com/jenkins-x-plugins/jx-build-controller/pkg/common"
	"github.com/jenkins-x-plugins/jx-secret/pkg/masker/watcher"
	jxv1 "github.com/jenkins-x/jx-api/v4/pkg/apis/jenkins.io/v1"
	"github.com/jenkins-x/jx-api/v4/pkg/client/clientset/versioned"
	"github.com/jenkins-x/jx-helpers/v3/pkg/cobras/helper"
	"github.com/jenkins-x/jx-helpers/v3/pkg/cobras/templates"
	"github.com/jenkins-x/jx-helpers/v3/pkg/kube"
	"github.com/jenkins-x/jx-helpers/v3/pkg/kube/jxclient"
	"github.com/jenkins-x/jx-helpers/v3/pkg/options"
	"github.com/jenkins-x/jx-kube-client/v3/pkg/kubeclient"
	"github.com/jenkins-x/jx-logging/v3/pkg/log"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	tektonclient "github.com/tektoncd/pipeline/pkg/client/clientset/versioned"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/jaeger"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdkresource "go.opentelemetry.io/otel/sdk/resource"
	exporttrace "go.opentelemetry.io/otel/sdk/trace"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/semconv/v1.20.0"
	"k8s.io/client-go/kubernetes"
)

var (
	cmdLong = templates.LongDesc(`
		Promotes a version of an application to an Environment
`)

	cmdExample = templates.Examples(`
		# promotes your current app to the staging environment
		%s 
	`)
)

// ControllerOptions the options for this command
type ControllerOptions struct {
	options.BaseOptions

	Namespace               string
	OperatorNamespace       string
	port                    string
	WriteLogToBucketTimeout time.Duration
	KubeClient              kubernetes.Interface
	JXClient                versioned.Interface
	TektonClient            tektonclient.Interface
	EnvironmentCache        map[string]*jxv1.Environment
	Masker                  watcher.Options
	tracesExporterType      string
	tracesExporterEndpoint  string
}

// NewCmdController creates a command object for the command
func NewCmdController() (*cobra.Command, *ControllerOptions) {
	o := &ControllerOptions{}

	cmd := &cobra.Command{
		Use:     "run",
		Short:   "Promotes a version of an application to an Environment",
		Long:    cmdLong,
		Example: fmt.Sprintf(cmdExample, common.BinaryName),
		Run: func(cmd *cobra.Command, args []string) {
			err := o.Run()
			helper.CheckErr(err)
		},
	}

	cmd.Flags().StringVarP(&o.Namespace, "namespace", "n", "", "The kubernetes Namespace to watch for PipelineRun and PipelineActivity resources. Defaults to the current namespace")
	cmd.Flags().StringVarP(&o.OperatorNamespace, "operator-namespace", "", "jx-git-operator", "The git operator namespace")
	cmd.Flags().DurationVarP(&o.WriteLogToBucketTimeout, "write-log-timeout", "", time.Minute*30, "The timeout for writing pipeline logs to the bucket")
	cmd.Flags().StringVarP(&o.port, "port", "", "8080", "The port for health and readiness checks to listen on")
	cmd.Flags().StringVarP(&o.tracesExporterType, "traces-exporter-type", "", os.Getenv("TRACES_EXPORTER_TYPE"), "The OpenTelemetry traces exporter type: otlp:grpc:insecure, otlp:http:insecure or jaeger:http:thrift")
	cmd.Flags().StringVarP(&o.tracesExporterEndpoint, "traces-exporter-endpoint", "", os.Getenv("TRACES_EXPORTER_ENDPOINT"), "The OpenTelemetry traces exporter endpoint (host:port)")
	o.BaseOptions.AddBaseFlags(cmd)
	return cmd, o
}

// Validate verifies things are setup correctly
func (o *ControllerOptions) Validate() error {
	var (
		ctx = context.Background()
		err error
	)

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

	if len(o.tracesExporterType) > 0 && len(o.tracesExporterEndpoint) > 0 {
		log.Logger().WithField("type", o.tracesExporterType).WithField("endpoint", o.tracesExporterEndpoint).Info("Initializing OpenTelemetry Traces Exporter")
		var exporter exporttrace.SpanExporter
		switch o.tracesExporterType {
		case "otlp:grpc:insecure":
			exporter, err = otlptrace.New(ctx, otlptracegrpc.NewClient(
				otlptracegrpc.WithEndpoint(o.tracesExporterEndpoint),
				otlptracegrpc.WithInsecure(),
			))
		case "otlp:http:insecure":
			exporter, err = otlptrace.New(ctx, otlptracehttp.NewClient(
				otlptracehttp.WithEndpoint(o.tracesExporterEndpoint),
				otlptracehttp.WithInsecure(),
			))
		case "jaeger:http:thrift":
			endpoint := fmt.Sprintf("http://%s/api/traces", o.tracesExporterEndpoint)
			_, err = http.Post(endpoint, "application/x-thrift", nil)
			if err != nil && strings.Contains(err.Error(), "no such host") {
				log.Logger().WithError(err).Warning("Traces Exporter Endpoint configuration error. Maybe you need to install/configure the Observability stack? https://jenkins-x.io/v3/admin/guides/observability/ The OpenTelemetry Tracing feature won't be enabled until this is fixed.")
				err = nil // ensure we won't fail. we just need to NOT set the exporter
			} else {
				exporter, err = jaeger.New(
					jaeger.WithCollectorEndpoint(jaeger.WithEndpoint(endpoint)),
				)
			}
		}
		if err != nil {
			return errors.Wrapf(err, "failed to create an OpenTelemetry Exporter for %s on %s", o.tracesExporterType, o.tracesExporterEndpoint)
		}
		if exporter != nil {
			otel.SetTracerProvider(sdktrace.NewTracerProvider(
				sdktrace.WithBatcher(exporter,
					sdktrace.WithMaxQueueSize(20),
					sdktrace.WithBatchTimeout(1000),
					sdktrace.WithMaxExportBatchSize(512),
				),
				sdktrace.WithSampler(sdktrace.AlwaysSample()),
				sdktrace.WithResource(sdkresource.NewWithAttributes(
					semconv.SchemaURL,
					semconv.ServiceNameKey.String("pipeline"),
					semconv.ServiceNamespaceKey.String(o.Namespace),
				)),
			))
		}
	}

	return nil
}

func (o *ControllerOptions) Run() error {
	err := o.Validate()
	if err != nil {
		return errors.Wrapf(err, "failed to validate options")
	}

	ns := o.Namespace

	// todo add resyncInterval

	isTektonClientReady := &atomic.Value{}
	isTektonClientReady.Store(false)

	isJenkinXClientReady := &atomic.Value{}
	isJenkinXClientReady.Store(false)

	activityCache, err := jx.NewActivityCache(o.JXClient, ns)
	if err != nil {
		return errors.Wrapf(err, "failed to create the pipeline activity cache")
	}

	to := &tekton.Options{
		TektonClient:  o.TektonClient,
		KubeClient:    o.KubeClient,
		JXClient:      o.JXClient,
		Namespace:     ns,
		IsReady:       isTektonClientReady,
		ActivityCache: activityCache,
	}

	// lets ensure the git client is setup to use git credentials
	g := to.GitClient()
	_, err = g.Command(".", "config", "--global", "credential.helper", "store")
	if err != nil {
		return errors.Wrapf(err, "failed to setup git")
	}

	log.Logger().Info("starting build controller")

	go func() {
		(&jx.Options{
			JXClient:      o.JXClient,
			Namespace:     ns,
			Masker:        o.Masker,
			IsReady:       isJenkinXClientReady,
			ActivityCache: activityCache,
		}).Start()
	}()

	go func() {
		to.Start()
	}()

	o.startHealthEndpoint(isTektonClientReady, isJenkinXClientReady)
	return nil
}

func (o ControllerOptions) startHealthEndpoint(isTektonClientReady, isJenkinXClientReady *atomic.Value) error {
	r := handler.Router(isTektonClientReady, isJenkinXClientReady)

	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt, syscall.SIGTERM)

	srv := &http.Server{
		Addr:    fmt.Sprintf(":%s", o.port),
		Handler: r,
	}
	go func() {
		log.Logger().Fatal(srv.ListenAndServe())
	}()
	log.Logger().Infof("The service is ready to listen and serve.")

	killSignal := <-interrupt
	switch killSignal {
	case os.Interrupt:
		log.Logger().Infof("Got SIGINT...")
	case syscall.SIGTERM:
		log.Logger().Infof("Got SIGTERM...")
	}

	log.Logger().Infof("The service is shutting down...")
	err := srv.Shutdown(context.Background())
	if err != nil {
		return errors.Wrapf(err, "failed to shutdown cleanly")
	}
	log.Logger().Infof("Done")
	return nil
}
