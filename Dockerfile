FROM ghcr.io/jenkins-x/jx-cli-base-image:0.0.48

ENTRYPOINT ["jx-build-controller"]

RUN git config --global credential.helper store

COPY ./build/linux/jx-build-controller /usr/bin/jx-build-controller