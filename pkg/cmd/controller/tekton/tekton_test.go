package tekton_test

import (
	"context"
	"fmt"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"os"
	"path/filepath"
	"testing"

	"github.com/jenkins-x-plugins/jx-build-controller/pkg/cmd/controller/jx"

	"github.com/jenkins-x-plugins/jx-build-controller/pkg/cmd/controller/tekton"

	"github.com/jenkins-x-plugins/jx-pipeline/pkg/testpipelines"
	fakejx "github.com/jenkins-x/jx-api/v4/pkg/client/clientset/versioned/fake"
	"github.com/jenkins-x/jx-helpers/v3/pkg/testhelpers"
	"github.com/jenkins-x/jx-helpers/v3/pkg/yamls"
	"github.com/stretchr/testify/require"
	"github.com/tektoncd/pipeline/pkg/apis/pipeline/v1"
	faketekton "github.com/tektoncd/pipeline/pkg/client/clientset/versioned/fake"
	"k8s.io/client-go/kubernetes/fake"
)

const (
	// enable this flag if you want to re-generate the test input data
	regenTestDataMode = false
)

func TestBuildControllerTekton(t *testing.T) {
	sourceDir := filepath.Join("testdata", "tekton")
	ns := "jx"

	tmpDir, err := os.MkdirTemp("", "")
	require.NoError(t, err, "failed to create temp dir")
	t.Logf("generating PipelineActivity resources to %s\n", tmpDir)

	o := tekton.Options{}
	o.KubeClient = fake.NewSimpleClientset()
	o.JXClient = fakejx.NewSimpleClientset()
	o.TektonClient = faketekton.NewSimpleClientset()
	o.Namespace = ns

	for i := 1; i <= 5; i++ {
		fileName := fmt.Sprintf("%d.yaml", i)
		prFile := filepath.Join(sourceDir, "pr", fileName)
		trFile := filepath.Join(sourceDir, "tr", fileName)
		expectedPAFile := filepath.Join(sourceDir, "pa", fileName)

		pr := &v1.PipelineRun{}
		err := yamls.LoadFile(prFile, pr)
		require.NoError(t, err, "failed to load %s", prFile)

		tr := &v1.TaskRun{}
		err = yamls.LoadFile(trFile, tr)
		require.NoError(t, err, "failed to load %s", trFile)

		_, err = o.TektonClient.TektonV1().TaskRuns(ns).Create(context.TODO(), tr, metav1.CreateOptions{})
		if err != nil {
			require.ErrorContains(t, err, "already exists", "failed to store taskrun %s", trFile)
			_, err = o.TektonClient.TektonV1().TaskRuns(ns).Update(context.TODO(), tr, metav1.UpdateOptions{})
		}
		require.NoError(t, err, "failed to store taskrun %s", trFile)

		// lets recreate the cache each loop so we load all the latest activities
		o.ActivityCache, err = jx.NewActivityCache(o.JXClient, ns)
		require.NoError(t, err, "failed to create activity cache")

		pa, err := o.OnPipelineRunUpsert(context.TODO(), pr, ns)
		require.NoError(t, err, "failed to process PipelineRun %i", i)
		require.NotNil(t, pa, "PipelineActivity %s is nil", i)

		testpipelines.ClearTimestamps(pa)
		if regenTestDataMode {
			actualPaFile := filepath.Join(sourceDir, "pa", fileName)
			err = yamls.SaveFile(pa, actualPaFile)
			require.NoError(t, err, "failed to save %s", actualPaFile)
		} else {
			actualPaFile := filepath.Join(tmpDir, fileName)
			err = yamls.SaveFile(pa, actualPaFile)
			require.NoError(t, err, "failed to save %s", actualPaFile)

			testhelpers.AssertTextFilesEqual(t, expectedPAFile, actualPaFile, "generated PipelineActivity for "+fileName)
		}
	}
}

func TestBuildControllerMetaPipeline(t *testing.T) {
	sourceDir := filepath.Join("testdata", "jx")
	ns := "jx"

	tmpDir, err := os.MkdirTemp("", "")
	require.NoError(t, err, "failed to create temp dir")
	t.Logf("generating PipelineActivity resources to %s\n", tmpDir)

	o := tekton.Options{}
	o.KubeClient = fake.NewSimpleClientset()
	o.JXClient = fakejx.NewSimpleClientset()
	o.TektonClient = faketekton.NewSimpleClientset()
	o.Namespace = ns

	for i := 1; i <= 6; i++ {
		o.TektonClient = faketekton.NewSimpleClientset()
		fileName := fmt.Sprintf("%d.yaml", i)
		prFile := filepath.Join(sourceDir, "pr", fileName)
		trFile := filepath.Join(sourceDir, "tr", fileName)
		expectedPAFile := filepath.Join(sourceDir, "pa", fileName)

		pr := &v1.PipelineRun{}
		err := yamls.LoadFile(prFile, pr)
		require.NoError(t, err, "failed to load %s", prFile)

		tr := &v1.TaskRun{}
		err = yamls.LoadFile(trFile, tr)
		require.NoError(t, err, "failed to load %s", trFile)

		_, err = o.TektonClient.TektonV1().TaskRuns(ns).Create(context.TODO(), tr, metav1.CreateOptions{})
		if err != nil {
			require.ErrorContains(t, err, "already exists", "failed to store taskrun %s", trFile)
			_, err = o.TektonClient.TektonV1().TaskRuns(ns).Update(context.TODO(), tr, metav1.UpdateOptions{})
		}
		require.NoError(t, err, "failed to store taskrun %s", trFile)

		// lets recreate the cache each loop so we load all the latest activities
		o.ActivityCache, err = jx.NewActivityCache(o.JXClient, ns)
		require.NoError(t, err, "failed to create activity cache")

		pa, err := o.OnPipelineRunUpsert(context.TODO(), pr, ns)
		require.NoError(t, err, "failed to process PipelineRun %i", i)
		require.NotNil(t, pa, "PipelineActivity %s is nil", i)
		testpipelines.ClearTimestamps(pa)

		if regenTestDataMode {
			actualPaFile := filepath.Join(sourceDir, "pa", fileName)
			err = yamls.SaveFile(pa, actualPaFile)
			require.NoError(t, err, "failed to save %s", actualPaFile)
		} else {
			actualPaFile := filepath.Join(tmpDir, fileName)
			err = yamls.SaveFile(pa, actualPaFile)
			require.NoError(t, err, "failed to save %s", actualPaFile)

			testhelpers.AssertTextFilesEqual(t, expectedPAFile, actualPaFile, "generated PipelineActivity for "+fileName)
		}
	}
}
