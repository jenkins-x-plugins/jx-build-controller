CHART_REPO := gs://jenkinsxio/charts
NAME := jx-build-controller

build: clean
	rm -rf Chart.lock
	helm dependency build
	helm lint

install: clean build
	helm install . --name ${NAME}

upgrade: clean build
	helm upgrade ${NAME} .

delete:
	helm delete --purge ${NAME}

clean:
	rm -rf charts
	rm -rf ${NAME}*.tgz

