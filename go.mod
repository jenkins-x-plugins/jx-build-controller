module github.com/jenkins-x-plugins/jx-build-controller

require (
	cloud.google.com/go v0.76.0 // indirect
	github.com/cpuguy83/go-md2man v1.0.10
	github.com/gorilla/mux v1.7.4
	github.com/jenkins-x/jx-api/v4 v4.0.24
	github.com/jenkins-x/jx-helpers/v3 v3.0.74
	github.com/jenkins-x/jx-kube-client/v3 v3.0.2
	github.com/jenkins-x/jx-logging/v3 v3.0.3
	github.com/jenkins-x/jx-pipeline v0.0.102
	github.com/jenkins-x/jx-secret v0.0.227
	github.com/pkg/errors v0.9.1
	github.com/spf13/cobra v1.1.1
	github.com/spf13/pflag v1.0.5
	github.com/stretchr/testify v1.7.0
	github.com/tektoncd/pipeline v0.20.0
	golang.org/x/crypto v0.0.0-20201221181555-eec23a3978ad // indirect
	golang.org/x/oauth2 v0.0.0-20210201163806-010130855d6c // indirect
	golang.org/x/term v0.0.0-20201210144234-2321bbc49cbf // indirect
	golang.org/x/time v0.0.0-20201208040808-7e3f01d25324 // indirect
	gopkg.in/yaml.v2 v2.4.0
	gopkg.in/yaml.v3 v3.0.0-20210107192922-496545a6307b // indirect
	k8s.io/apimachinery v0.20.2
	k8s.io/client-go v11.0.1-0.20190805182717-6502b5e7b1b5+incompatible
	k8s.io/klog/v2 v2.5.0 // indirect
	k8s.io/utils v0.0.0-20210111153108-fddb29f9d009 // indirect
)

replace (
	github.com/tektoncd/pipeline => github.com/jenkins-x/pipeline v0.3.2-0.20210118090417-1e821d85abf6
	k8s.io/client-go => k8s.io/client-go v0.20.2
	knative.dev/pkg => github.com/jstrachan/pkg v0.0.0-20210118084935-c7bdd6c14bd0
)

go 1.15
