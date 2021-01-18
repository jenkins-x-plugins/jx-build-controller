module github.com/jenkins-x-plugins/jx-build-controller

require (
	github.com/cpuguy83/go-md2man v1.0.10
	github.com/gorilla/mux v1.7.4
	github.com/jenkins-x/go-scm v1.5.211 // indirect
	github.com/jenkins-x/jx-api/v4 v4.0.23
	github.com/jenkins-x/jx-gitops v0.0.528 // indirect
	github.com/jenkins-x/jx-helpers/v3 v3.0.63
	github.com/jenkins-x/jx-kube-client/v3 v3.0.1
	github.com/jenkins-x/jx-logging/v3 v3.0.3
	github.com/jenkins-x/jx-pipeline v0.0.81
	github.com/jenkins-x/jx-secret v0.0.209
	github.com/jenkins-x/lighthouse v0.0.908 // indirect
	github.com/pkg/errors v0.9.1
	github.com/spf13/cobra v1.1.1
	github.com/spf13/pflag v1.0.5
	github.com/stretchr/testify v1.6.1
	github.com/tektoncd/pipeline v0.16.3
	k8s.io/apimachinery v0.20.2
	k8s.io/client-go v11.0.1-0.20190805182717-6502b5e7b1b5+incompatible
)

replace (
	github.com/tektoncd/pipeline => github.com/jenkins-x/pipeline v0.0.0-20201002150609-ca0741e5d19a
	k8s.io/client-go => k8s.io/client-go v0.19.2
)

go 1.15
