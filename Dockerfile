FROM gcr.io/jenkinsxio/jx-cli-base:0.0.21

ENTRYPOINT ["jx-build-controller"]

COPY ./build/linux/jx-build-controller /usr/bin/jx-build-controller