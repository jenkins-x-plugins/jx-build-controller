### Linux

```shell
curl -L https://github.com/jenkins-x-plugins/jx-build-controller/releases/download/v{{.Version}}/jx-build-controller-linux-amd64.tar.gz | tar xzv 
sudo mv jx-build-controller /usr/local/bin
```

### macOS

```shell
curl -L  https://github.com/jenkins-x-plugins/jx-build-controller/releases/download/v{{.Version}}/jx-build-controller-darwin-amd64.tar.gz | tar xzv
sudo mv jx-build-controller /usr/local/bin
```

