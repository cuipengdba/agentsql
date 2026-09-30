#!/usr/bin/env bash
# AgentSQL Live Demo 每日重置：清卷 -> 重建两库 -> 重新 seed -> 起网关。
# 由 root crontab 每天凌晨 04:17 调用。
set -euo pipefail
cd /opt/agentsql-demo

LOG=/var/log/agentsql-demo-reset.log
exec >> "$LOG" 2>&1
echo "===== $(date '+%Y-%m-%d %H:%M:%S') reset start ====="

docker compose down -v
docker compose up -d postgres mysql

# 等待两库 healthy（最多 120 秒）；超时不退出，记录结构化日志后继续尝试 seed。
db_ready=0
for i in $(seq 1 24); do
  pg=$(docker inspect -f '{{.State.Health.Status}}' agentsql-demo-postgres 2>/dev/null || echo none)
  my=$(docker inspect -f '{{.State.Health.Status}}' agentsql-demo-mysql 2>/dev/null || echo none)
  echo "reset:wait postgres=$pg mysql=$my attempt=$i"
  if [ "$pg" = "healthy" ] && [ "$my" = "healthy" ]; then db_ready=1; break; fi
  sleep 5
done
if [ "$db_ready" != "1" ]; then
  echo "reset:db_wait_timeout postgres=$pg mysql=$my (proceeding anyway)"
fi

# MySQL 容器 healthcheck（mysqladmin ping）可能在应用层（用户库/权限）完全就绪前
# 就报告 healthy，此时 seed 会因连通性探测失败而退出。给 seed 加重试（最多 5 次，间隔 10 秒）。
seed_ok=0
for i in $(seq 1 5); do
  echo "reset:seed_attempt $i/5"
  if docker compose run --rm seed; then
    seed_ok=1
    break
  fi
  echo "reset:seed_attempt $i failed, retry in 10s"
  sleep 10
done
if [ "$seed_ok" != "1" ]; then
  echo "reset:seed_failed_after_5_attempts (starting gateway with --no-deps anyway)"
fi

# 无论 seed 是否完全成功都启动网关，避免对外 502。
# --no-deps：显式跳过 compose 中 gateway 对 seed 的
# depends_on: service_completed_successfully 依赖，否则 seed 最终失败时 gateway 不会启动。
docker compose up -d --no-deps gateway

sleep 5
if curl -sS http://127.0.0.1:17880/healthz; then
  echo "reset:healthz ok"
else
  echo "reset:healthz FAIL (gateway up but not healthy)"
fi
echo "===== $(date '+%Y-%m-%d %H:%M:%S') reset done ====="
