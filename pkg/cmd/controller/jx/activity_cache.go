package jx

import (
	"context"
	"sync"

	v1 "github.com/jenkins-x/jx-api/v4/pkg/apis/jenkins.io/v1"
	jxVersioned "github.com/jenkins-x/jx-api/v4/pkg/client/clientset/versioned"
	"github.com/pkg/errors"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ActivityCache a cache of activities
type ActivityCache struct {
	lock  sync.Mutex
	cache map[string]*v1.PipelineActivity
}

// NewActivityCache creates a new activity cache
func NewActivityCache(jxClient jxVersioned.Interface, ns string) (*ActivityCache, error) {
	c := map[string]*v1.PipelineActivity{}

	ctx := context.TODO()
	resources, err := jxClient.JenkinsV1().PipelineActivities(ns).List(ctx, metav1.ListOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return nil, errors.Wrapf(err, "failed to list PipelineActivity resources in namespace %s", ns)
	}
	if resources != nil {
		for i := range resources.Items {
			r := &resources.Items[i]
			c[r.Name] = r
		}
	}
	return &ActivityCache{
		cache: c,
	}, nil
}

// Upsert upserts an activity
func (c *ActivityCache) Upsert(r *v1.PipelineActivity) {
	c.lock.Lock()
	defer c.lock.Unlock()

	c.cache[r.Name] = r
}

// Delete deletes an activity
func (c *ActivityCache) Delete(r *v1.PipelineActivity) {
	c.lock.Lock()
	defer c.lock.Unlock()

	delete(c.cache, r.Name)
}

// Get returns the resource by name
func (c *ActivityCache) Get(name string) *v1.PipelineActivity {
	c.lock.Lock()
	defer c.lock.Unlock()

	return c.cache[name]
}

// List lists the resources
func (c *ActivityCache) List() []v1.PipelineActivity {
	c.lock.Lock()
	defer c.lock.Unlock()

	var answer []v1.PipelineActivity
	for _, r := range c.cache {
		answer = append(answer, *r)
	}
	return answer
}
