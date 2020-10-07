package controller_test

import (
	"context"
	"fmt"
	"io/ioutil"
	"path/filepath"
	"testing"

	controller2 "github.com/jenkins-x-plugins/jx-build-controller/pkg/cmd/controller"
	jxv1 "github.com/jenkins-x/jx-api/v3/pkg/apis/jenkins.io/v1"
	"github.com/jenkins-x/jx-api/v3/pkg/client/clientset/versioned"
	fakejx "github.com/jenkins-x/jx-api/v3/pkg/client/clientset/versioned/fake"
	"github.com/jenkins-x/jx-helpers/v3/pkg/testhelpers"
	"github.com/jenkins-x/jx-helpers/v3/pkg/yamls"
	"github.com/stretchr/testify/require"
	"github.com/tektoncd/pipeline/pkg/apis/pipeline/v1beta1"
	faketekton "github.com/tektoncd/pipeline/pkg/client/clientset/versioned/fake"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestBuildController(t *testing.T) {
	// enable this flag if you want to re-generate the test input data
	regenTestDataMode := false
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

		pa, err := o.OnPipelineRunUpsert(pr, ns)
		require.NoError(t, err, "failed to process PipelineRun %i", i)

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

func findOnlyPipelineActivity(t *testing.T, jxClient versioned.Interface, ns string) *jxv1.PipelineActivity {
	ctx := context.Background()
	resources, err := jxClient.JenkinsV1().PipelineActivities(ns).List(ctx, metav1.ListOptions{})
	require.NoError(t, err, "finding PipeineActivity resources in namespace %s", ns)
	require.NotNil(t, resources, "should have found a PipelineActivityList in namespace %s", ns)
	require.Len(t, resources.Items, 1, "should have found a PipelineActivity in namespace %s", ns)
	return &resources.Items[0]
}
