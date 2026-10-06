#!/bin/bash
# cmux-tui-run.sh — 在 cmux 或 GoRex 专用 split 里跑 pi/agy 等交互 TUI 工具，轮询
# sentinel 检测完成，linger 后安全关闭 split，结果回传 stdout。两者都不在时若给了
# --fallback-cmd 则改走旧版非交互路径。
#
# GoRex 的 gorex 命令行（GoRex 会话里自动在 PATH 末尾）与本脚本用到的 cmux 子命令
# 同名同参：new-split / rename-tab / send / read-screen / close-surface / ping；
# 会话里的 GOREX_SURFACE_ID / GOREX_WORKSPACE_ID 对应 CMUX_SURFACE_ID /
# CMUX_WORKSPACE_ID。在 GoRex 内优先用 GoRex，否则用 cmux。
#
# 用法：
#   bash cmux-tui-run.sh --sentinel <file> [--timeout <sec>] [--linger <sec>] \
#       [--direction right|down|left|up] [--title <text>] [--fallback-cmd '<shell str>'] \
#       -- <command> [args...]
# 默认值：--timeout 0（0 = 不超时，无限等待），--linger 5，--direction right。
# --sentinel 和 -- 后的命令必填。返回码：0 成功；1 工具退出但无 sentinel；
# 97 不在 cmux / GoRex 内且无 fallback；98 split 创建/定位/注入失败；
# 124 超时（仅显式给了正 --timeout 时可能出现）。
set -u

usage() {
  cat >&2 <<'USAGE'
usage: cmux-tui-run.sh --sentinel <file> [--timeout <sec>] [--linger <sec>]
                       [--direction right|down|left|up] [--title <text>]
                       [--fallback-cmd '<shell string>']
                       -- <command> [args...]
USAGE
}

SENTINEL=""
TIMEOUT=0
LINGER=5
DIRECTION="right"
TITLE=""
FALLBACK=""
CMD=()

while [ $# -gt 0 ]; do
  case "$1" in
    --sentinel)     [ $# -ge 2 ] || { usage; exit 2; }; SENTINEL="$2"; shift 2 ;;
    --timeout)      [ $# -ge 2 ] || { usage; exit 2; }; TIMEOUT="$2"; shift 2 ;;
    --linger)       [ $# -ge 2 ] || { usage; exit 2; }; LINGER="$2"; shift 2 ;;
    --direction)    [ $# -ge 2 ] || { usage; exit 2; }; DIRECTION="$2"; shift 2 ;;
    --title)        [ $# -ge 2 ] || { usage; exit 2; }; TITLE="$2"; shift 2 ;;
    --fallback-cmd) [ $# -ge 2 ] || { usage; exit 2; }; FALLBACK="$2"; shift 2 ;;
    --)             shift; break ;;
    *)              usage; exit 2 ;;
  esac
done
CMD=("$@")

if [ -z "$SENTINEL" ] || [ "${#CMD[@]}" -eq 0 ]; then
  usage
  exit 2
fi

# 校验 --timeout / --linger 为非负整数（非整数会让轮询的比较每轮报错返回假，永不超时）。
case "$TIMEOUT" in *[!0-9]* | '') echo "--timeout must be a non-negative integer" >&2; usage; exit 2 ;; esac
case "$LINGER"  in *[!0-9]* | '') echo "--linger must be a non-negative integer" >&2;  usage; exit 2 ;; esac

# 终端检测：GOREX_SURFACE_ID 非空且 gorex ping 成功算在 GoRex 内；否则
# CMUX_WORKSPACE_ID 非空且 cmux ping 成功算在 cmux 内。MUX 是要调用的命令，
# SELF 是调用方自己的 surface（绝不能关掉它）。
MUX=""
SELF=""
if [ -n "${GOREX_SURFACE_ID:-}" ] && gorex ping >/dev/null 2>&1; then
  MUX=gorex
  SELF="$GOREX_SURFACE_ID"
elif [ -n "${CMUX_WORKSPACE_ID:-}" ] && cmux ping >/dev/null 2>&1; then
  MUX=cmux
  SELF="${CMUX_SURFACE_ID:-}"
fi

if [ -z "$MUX" ]; then
  if [ -n "$FALLBACK" ]; then
    exec bash -c "$FALLBACK"
  fi
  echo "[cmux-tui-run] not running inside cmux or GoRex and no --fallback-cmd given" >&2
  exit 97
fi

# 陈旧文件会造成假完成，先清掉。
rm -f "$SENTINEL" "$SENTINEL.exit"

# 生成 runner 临时脚本（所有插值用 printf '%q' 转义，安全对抗空格/引号/特殊字符）。
RUNNER=$(mktemp -t cmux-tui-run.XXXX)
# EXIT trap 兜底清理 runner：所有退出路径（失败/超时/成功）都会触发；split 里的 bash
# 已持有该文件 fd，unlink 不影响其继续执行。
trap 'rm -f "$RUNNER"' EXIT
{
  echo '#!/bin/bash'
  printf 'cd %s || exit 1\n' "$(printf '%q' "$PWD")"
  printf '%q ' "${CMD[@]}"
  printf '\n'
  printf 'rc=$?\n'
  printf '%s\n' "echo \"\$rc\" > $(printf '%q' "$SENTINEL.exit")"
  printf '%s\n' "printf '\\n[cmux-tui-run] tool exited with code %s\\n' \"\$rc\""
} > "$RUNNER"
chmod +x "$RUNNER"

# 开 split。
OUT=$("$MUX" --id-format both new-split "$DIRECTION" --focus false 2>&1)
if [ $? -ne 0 ]; then
  if [ -n "$FALLBACK" ]; then
    rm -f "$RUNNER"
    exec bash -c "$FALLBACK"
  fi
  echo "[cmux-tui-run] new-split failed: $OUT" >&2
  exit 98
fi

SID=$(printf '%s\n' "$OUT" | grep -oE '[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}' | head -1)

if [ -z "$SID" ]; then
  # split 可能已创建但拿不到 ID，绝不自动关闭，留人工处理。
  echo "[cmux-tui-run] split may have been created but failed to extract surface ID; left open for manual handling" >&2
  if [ -n "$FALLBACK" ]; then
    rm -f "$RUNNER"
    exec bash -c "$FALLBACK"
  fi
  exit 98
fi

if [ "$SID" = "$SELF" ]; then
  # 绝不把调用方自己的 surface 当目标。
  echo "[cmux-tui-run] FATAL: extracted SID equals caller's own surface ID; aborting" >&2
  exit 98
fi

echo "[cmux-tui-run] surface $SID opened"

if [ -n "$TITLE" ]; then
  "$MUX" rename-tab --surface "$SID" "$TITLE" || true
fi

# split 里新 shell 初始化需要几秒，直接 send 会丢输入。
sleep 4

if ! "$MUX" send --surface "$SID" -- "bash $(printf '%q' "$RUNNER")\n"; then
  echo "[cmux-tui-run] send failed; split left open" >&2
  exit 98
fi

# 安全关闭：cmux 的 close-surface 缺省参数会回退到调用方 pane / 聚焦 surface，
# 误杀调用方（Claude Code 自身）；因此显式 UUID + 剥离环境变量 + 空值拒关，
# 三者缺一不可（gorex close-surface 本身不给 --surface 就拒绝执行，这里同样防护）。
# 这是全脚本唯一允许调用 close-surface 的地方。
safe_close() {
  if [ -n "$SID" ] && [ "$SID" != "$SELF" ]; then
    env -u CMUX_SURFACE_ID -u CMUX_PANEL_ID -u CMUX_TAB_ID -u GOREX_SURFACE_ID \
      "$MUX" close-surface --surface "$SID" \
      || echo "[cmux-tui-run] close failed; split left open" >&2
  fi
}

finish_success() {
  sleep "$LINGER"
  safe_close
  echo "=== RESULT ==="
  cat "$SENTINEL"
  if [ -f "$SENTINEL.exit" ]; then
    rc2=$(cat "$SENTINEL.exit" 2>/dev/null || true)
    echo "tool exit code: ${rc2:-unknown}"
  fi
}

# 轮询循环：每 2 秒；--timeout 为 0（默认）时不设超时，一直等 sentinel。
START=$SECONDS
while :; do
  if [ -f "$SENTINEL" ]; then
    finish_success
    exit 0
  fi

  if [ -f "$SENTINEL.exit" ]; then
    # .exit 出现而 sentinel 不在：再宽限 5 秒看 sentinel 是否补上。
    sleep 5
    if [ -f "$SENTINEL" ]; then
      finish_success
      exit 0
    fi
    rc3=$(cat "$SENTINEL.exit" 2>/dev/null || true)
    echo "[cmux-tui-run] tool exited (code ${rc3:-?}) without writing sentinel — split left open" >&2
    "$MUX" read-screen --surface "$SID" --scrollback --lines 60
    exit 1
  fi

  if [ "$TIMEOUT" -gt 0 ]; then
    elapsed=$((SECONDS - START))
    if [ "$elapsed" -ge "$TIMEOUT" ]; then
      echo "[cmux-tui-run] timeout after ${TIMEOUT}s — split left open" >&2
      "$MUX" read-screen --surface "$SID" --scrollback --lines 60
      exit 124
    fi
  fi

  sleep 2
done