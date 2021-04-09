package jx

import (
	"fmt"
	"sync/atomic"
	"time"

	"github.com/jenkins-x-plugins/jx-secret/pkg/masker/watcher"
	jxv1 "github.com/jenkins-x/jx-api/v4/pkg/apis/jenkins.io/v1"
	v1 "github.com/jenkins-x/jx-api/v4/pkg/apis/jenkins.io/v1"
	jxVersioned "github.com/jenkins-x/jx-api/v4/pkg/client/clientset/versioned"
	informers "github.com/jenkins-x/jx-api/v4/pkg/client/informers/externalversions"
	"github.com/jenkins-x/jx-logging/v3/pkg/log"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/tools/cache"
)

type Options struct {
	JXClient         jxVersioned.Interface
	Masker           watcher.Options
	EnvironmentCache map[string]*jxv1.Environment
	ActivityCache    *ActivityCache
	Namespace        string
	IsReady          *atomic.Value
}

func (o *Options) Start() {
	informerFactory := informers.NewSharedInformerFactoryWithOptions(
		o.JXClient,
		time.Minute*10,
		informers.WithNamespace(o.Namespace),
	)

	stop := make(chan struct{})
	defer close(stop)
	defer runtime.HandleCrash()

	err := o.Masker.RunWithChannel(stop)
	if err != nil {
		log.Logger().Fatalf("failed to start masker channel: %v", err)
	}

	log.Logger().Infof("Watching for Environment resources in namespace %s", o.Namespace)

	jxEnvironmentInformer := informerFactory.Jenkins().V1().Environments().Informer()
	jxEnvironmentInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			e := obj.(*v1.Environment)
			o.onEnvironment(obj)
			log.Logger().Infof("added %s", e.Name)
		},
		UpdateFunc: func(old, new interface{}) {
			e := new.(*v1.Environment)
			o.onEnvironment(new)
			log.Logger().Infof("updated %s", e.Name)
		},
	})

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
	// Starts all the shared informers that have been created by the factory so
	// far.

	// wait for the initial synchronization of the local cache.
	if !cache.WaitForCacheSync(stop, jxEnvironmentInformer.HasSynced) {
		runtime.HandleError(fmt.Errorf("timed out waiting for jx caches to sync"))
	}

	o.IsReady.Store(true)

	<-stop

	// Wait forever
	select {}
}

func (o *Options) onEnvironment(obj interface{}) {
	env, ok := obj.(*jxv1.Environment)
	if !ok {
		log.Logger().Infof("Object is not an Environment %#v", obj)
		return
	}
	if env != nil {
		o.EnvironmentCache[env.Name] = env
	}
}
