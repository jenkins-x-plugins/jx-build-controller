package jx

import (
	"fmt"
	"sync/atomic"
	"time"

	"github.com/jenkins-x-plugins/jx-secret/pkg/masker/watcher"
	v1 "github.com/jenkins-x/jx-api/v4/pkg/apis/jenkins.io/v1"
	jxVersioned "github.com/jenkins-x/jx-api/v4/pkg/client/clientset/versioned"
	informers "github.com/jenkins-x/jx-api/v4/pkg/client/informers/externalversions"
	"github.com/jenkins-x/jx-logging/v3/pkg/log"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/tools/cache"
)

type Options struct {
	JXClient      jxVersioned.Interface
	Masker        watcher.Options
	ActivityCache *ActivityCache
	Namespace     string
	AllNamespaces bool
	IsReady       *atomic.Value
}

func (o *Options) Start() {
	var externalVersions []informers.SharedInformerOption
	if !o.AllNamespaces {
		externalVersions = []informers.SharedInformerOption{
			informers.WithNamespace(o.Namespace),
		}
	}

	informerFactory := informers.NewSharedInformerFactoryWithOptions(
		o.JXClient,
		time.Minute*10,
		externalVersions...,
	)

	stop := make(chan struct{})
	defer close(stop)
	defer runtime.HandleCrash()

	err := o.Masker.RunWithChannel(stop)
	if err != nil {
		log.Logger().Fatalf("failed to start masker channel: %v", err)
	}

	log.Logger().Infof("Watching for PipelineActivity resources in namespace %s", o.Namespace)

	jxActivityInformer := informerFactory.Jenkins().V1().PipelineActivities().Informer()
	jxActivityInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			r := obj.(*v1.PipelineActivity)
			if r != nil {
				o.ActivityCache.Upsert(r)
			}
		},
		UpdateFunc: func(old, new interface{}) {
			r := new.(*v1.PipelineActivity)
			if r != nil {
				o.ActivityCache.Upsert(r)
			}
		},
		DeleteFunc: func(obj interface{}) {
			r := obj.(*v1.PipelineActivity)
			if r != nil {
				o.ActivityCache.Delete(r)
			}
		},
	})

	informerFactory.Start(stop)
	if !cache.WaitForCacheSync(stop, jxActivityInformer.HasSynced) {
		msg := "timed out waiting for jx caches to sync"
		runtime.HandleError(fmt.Errorf(msg))
	}

	o.IsReady.Store(true)

	<-stop

	// Wait forever
	select {}
}
