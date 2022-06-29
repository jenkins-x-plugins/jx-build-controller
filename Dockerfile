FROM alpine:3.16.0
RUN apk update && apk add --no-cache git
RUN git config --global credential.helper store
COPY ./build/linux/jx-build-controller /usr/bin/jx-build-controller

ENTRYPOINT ["jx-build-controller"]
