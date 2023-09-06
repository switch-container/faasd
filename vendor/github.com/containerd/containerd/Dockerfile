# docker build . -t containerd-dev -f Dockerfile --network host \
#   --build-arg https_proxy=http://127.0.0.1:7890 --build-arg http_proxy=http://127.0.0.1:7890


FROM golang:1.20

RUN apt-get update && \
    apt-get install -y libbtrfs-dev libseccomp-dev
