# Stage 1: build a fully static binary.
# CGO_ENABLED=0 produces a static Go binary with no libc dependency.
# GOOS/GOARCH target the linux/amd64 architecture UKC expects.
FROM golang:1.22 AS build
WORKDIR /src
COPY go.mod main.go ./
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags='-s -w' -o /server ./main.go

# Stage 2: a minimal rootfs containing only the binary.
# FROM scratch ensures there are no dynamic libraries for the elfloader to reject.
# kraft turns this rootfs + the chosen runtime into a Unikraft unikernel package.
FROM scratch
COPY --from=build /server /server
ENTRYPOINT ["/server"]
