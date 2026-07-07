# 构建阶段
FROM golang:1.25-alpine AS builder

WORKDIR /app

# 缓存依赖
COPY go.mod go.sum ./
RUN go mod download

# 编译
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o claw ./cmd/claw
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o bench ./cmd/bench

# 运行阶段
FROM alpine:3.19

RUN apk add --no-cache bash git ca-certificates

WORKDIR /app

# 复制编译产物
COPY --from=builder /app/claw /app/claw
COPY --from=builder /app/bench /app/bench

# 复制配置模板
COPY --from=builder /app/config.yaml.example /app/config.yaml.example
COPY --from=builder /app/.claw /app/.claw

# 默认端口（微信模式）
EXPOSE 48080

# 入口
ENTRYPOINT ["/app/claw"]
