package jx

import (
	"fmt"
	"time"

	jxv1 "github.com/jenkins-x/jx-api/v3/pkg/apis/jenkins.io/v1"
	v1 "github.com/jenkins-x/jx-api/v3/pkg/apis/jenkins.io/v1"
	jxVersioned "github.com/jenkins-x/jx-api/v3/pkg/client/clientset/versioned"
	informers "github.com/jenkins-x/jx-api/v3/pkg/client/informers/externalversions"
	"github.com/jenkins-x/jx-logging/v3/pkg/log"
	"github.com/jenkins-x/jx-secret/pkg/masker/watcher"
	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/tools/cache"
)

type Options struct {
	JXClient         jxVersioned.Interface
	Masker           watcher.Options
	EnvironmentCache map[string]*jxv1.Environment
	Namespace        string
}

func (o *Options) Start() error {
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
		return errors.Wrapf(err, "failed to start masker channel")
	}

	log.Logger().Infof("Watching for Environment resources in namespace %s", o.Namespace)

	jxEnvironmentInformer := informerFactory.Jenkins().V1().Environments().Informer()
	jxEnvironmentInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			e := obj.(*v1.Environment)
			o.onEnvironment(obj, o.Namespace)
			log.Logger().Infof("added %s", e.Name)
		},
		UpdateFunc: func(old, new interface{}) {
			e := new.(*v1.Environment)
			o.onEnvironment(new, o.Namespace)
			log.Logger().Infof("updated %s", e.Name)
		},
	})

	if !cache.WaitForCacheSync(stop, jxEnvironmentInformer.HasSynced) {
		msg := "timed out waiting for jx caches to sync"
		runtime.HandleError(fmt.Errorf(msg))
		return errors.New(msg)
	}

	// Starts all the shared informers that have been created by the factory so
	// far.
	informerFactory.Start(stop)
	// wait for the initial synchronization of the local cache.
	if !cache.WaitForCacheSync(stop, jxEnvironmentInformer.HasSynced) {
		msg := "timed out waiting for jx caches to sync"
		runtime.HandleError(fmt.Errorf(msg))
		return errors.New(msg)
	}

	//todo i.IsReady.Store(true)
	<-stop

	// Wait forever
	select {}
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
