package controller_test

import (
	"context"
	"fmt"
	"io/ioutil"
	"path/filepath"
	"testing"

	controller2 "github.com/jenkins-x-plugins/jx-build-controller/pkg/cmd/controller"
	fakejx "github.com/jenkins-x/jx-api/v3/pkg/client/clientset/versioned/fake"
	"github.com/jenkins-x/jx-helpers/v3/pkg/testhelpers"
	"github.com/jenkins-x/jx-helpers/v3/pkg/yamls"
	"github.com/jenkins-x/jx-pipeline/pkg/testpipelines"
	"github.com/stretchr/testify/require"
	"github.com/tektoncd/pipeline/pkg/apis/pipeline/v1beta1"
	faketekton "github.com/tektoncd/pipeline/pkg/client/clientset/versioned/fake"
	"k8s.io/client-go/kubernetes/fake"
)

const (
	// enable this flag if you want to re-generate the test input data
	regenTestDataMode = false
)

func TestBuildControllerTekton(t *testing.T) {
	sourceDir := filepath.Join("test_data", "tekton")
	ns := "jx"

	tmpDir, err := ioutil.TempDir("", "")
	require.NoError(t, err, "failed to create temp dir")
	t.Logf("generating PipelineActivity resources to %s\n", tmpDir)

	_, o := controller2.NewCmdController()
	o.KubeClient = fake.NewSimpleClientset()
	o.JXClient = fakejx.NewSimpleClientset()
	o.TektonClient = faketekton.NewSimpleClientset()
	o.Namespace = ns

	err = o.Validate()
	require.NoError(t, err, "failed to validate controller")

	for i := 1; i <= 5; i++ {
		fileName := fmt.Sprintf("%d.yaml", i)
		prFile := filepath.Join(sourceDir, "pr", fileName)
		expectedPAFile := filepath.Join(sourceDir, "pa", fileName)

		pr := &v1beta1.PipelineRun{}
		err := yamls.LoadFile(prFile, pr)
		require.NoError(t, err, "failed to load %s", prFile)

		pa, err := o.OnPipelineRunUpsert(context.TODO(), pr, ns)
		require.NoError(t, err, "failed to process PipelineRun %i", i)

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
	sourceDir := filepath.Join("test_data", "jx")
	ns := "jx"

	tmpDir, err := ioutil.TempDir("", "")
	require.NoError(t, err, "failed to create temp dir")
	t.Logf("generating PipelineActivity resources to %s\n", tmpDir)

	_, o := controller2.NewCmdController()
	o.KubeClient = fake.NewSimpleClientset()
	o.JXClient = fakejx.NewSimpleClientset()
	o.TektonClient = faketekton.NewSimpleClientset()
	o.Namespace = ns

	err = o.Validate()
	require.NoError(t, err, "failed to validate controller")

	for i := 1; i <= 6; i++ {
		fileName := fmt.Sprintf("%d.yaml", i)
		prFile := filepath.Join(sourceDir, "pr", fileName)
		expectedPAFile := filepath.Join(sourceDir, "pa", fileName)

		pr := &v1beta1.PipelineRun{}
		err := yamls.LoadFile(prFile, pr)
		require.NoError(t, err, "failed to load %s", prFile)

		pa, err := o.OnPipelineRunUpsert(context.TODO(), pr, ns)
		require.NoError(t, err, "failed to process PipelineRun %i", i)
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
