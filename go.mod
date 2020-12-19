module github.com/jenkins-x-plugins/jx-build-controller

require (
	github.com/cpuguy83/go-md2man v1.0.10
	github.com/gorilla/mux v1.7.4
	github.com/jenkins-x/jx-api/v4 v4.0.14
	github.com/jenkins-x/jx-helpers/v3 v3.0.41
	github.com/jenkins-x/jx-kube-client/v3 v3.0.1
	github.com/jenkins-x/jx-logging/v3 v3.0.2
	github.com/jenkins-x/jx-pipeline v0.0.74
	github.com/jenkins-x/jx-secret v0.0.196
	github.com/pkg/errors v0.9.1
	github.com/spf13/cobra v1.1.1
	github.com/spf13/pflag v1.0.5
	github.com/stretchr/testify v1.6.1
	github.com/tektoncd/pipeline v0.16.3
	k8s.io/apimachinery v0.19.4
	k8s.io/client-go v11.0.1-0.20190805182717-6502b5e7b1b5+incompatible
)

replace (
	github.com/tektoncd/pipeline => github.com/jenkins-x/pipeline v0.0.0-20201002150609-ca0741e5d19a
	k8s.io/client-go => k8s.io/client-go v0.19.2
)

go 1.15
