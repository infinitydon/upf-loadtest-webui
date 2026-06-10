FROM node:24-alpine AS frontend
WORKDIR /src/frontend
COPY frontend/package*.json ./
RUN npm install
COPY frontend/ ./
RUN npm run build

FROM golang:1.25-alpine AS backend
WORKDIR /src
COPY go.mod main.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/server .

FROM alpine:3.22
ARG HELM_VERSION=v4.2.0
ARG KUBECTL_VERSION=v1.35.0
LABEL org.opencontainers.image.source="https://github.com/infinitydon/upf-loadtest-webui"
LABEL org.opencontainers.image.description="Web control plane for Travelping UPF PFCP and TRex load tests"
RUN apk add --no-cache ca-certificates curl tar \
 && curl -fsSL "https://get.helm.sh/helm-${HELM_VERSION}-linux-amd64.tar.gz" | tar -xz -C /tmp \
 && install /tmp/linux-amd64/helm /usr/local/bin/helm \
 && curl -fsSL "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/amd64/kubectl" -o /usr/local/bin/kubectl \
 && chmod 0755 /usr/local/bin/kubectl \
 && rm -rf /tmp/linux-amd64
WORKDIR /app
COPY --from=backend /out/server /app/server
COPY --from=frontend /src/frontend/dist /app/static
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/app/server"]
