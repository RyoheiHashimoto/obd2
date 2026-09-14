#!/bin/bash
# cross-review: 差分を Antigravity CLI（agy）にレビューさせ、結果を検査して出力する。
# 使い方: run.sh [git diff の引数...]   省略時は HEAD（コミットしていない変更すべて。未追跡のファイルも含む）
# 出力の1行目: CROSS_REVIEW_OK / CROSS_REVIEW_EMPTY / CROSS_REVIEW_FAILED
# HEAD（作業ツリー）のレビューに成功したときは、その状態を .git/cross-review-ok に記録する。
# コミット前の hook（gate.py）は、この記録と今の状態を比べる。
#
# agy 1.2.2 の headless で確かめた挙動（2026-09-12〜13）:
# - 許可のない操作は拒否され、その場で打ち切られる。それでも status=SUCCESS・exit 0 で応答は空になり、
#   denied_actions にだけ残る。カレントディレクトリの読み取りも --add-dir を付けないと拒否される。
# - --print-timeout を超えると途中までの応答で status=SUCCESS・exit 0 になる（stderr に "print timeout"）。
# - ツールを使う回で、拒否もタイムアウトもないのに応答が空のまま SUCCESS で終わることがある
#   （obd2 の 1ac4459 で3回中1回。会話の記録にも最終回答が残っていない）。
# なので終了コードと status では判定せず、denied_actions・stderr・空の応答を見る。
# 打ち切りと空の応答のときは、ツールを使わない指示で1回だけ再実行する。
set -u

here=$(cd "$(dirname "$0")" && pwd)
MODEL="${CROSS_REVIEW_MODEL:-gemini-3.1-pro-high}"
MAX_BYTES=400000
BUDGET=540   # 秒。Claude Code の Bash ツールの上限（10分）に収める

fail() { echo "CROSS_REVIEW_FAILED: $*"; exit 1; }
# 作業ツリーの状態ハッシュ。state.py がないとき（リポジトリに写した run.sh など）や、計算できないときは空
state_of() {
  [ -f "$here/state.py" ] || return 0
  local out
  out=$(python3 "$here/state.py" "$root" 2>/dev/null) || return 0
  printf '%s' "${out%% *}"
}

command -v agy >/dev/null || fail "agy が見つかりません（~/.local/bin/agy）"
root=$(git rev-parse --show-toplevel 2>/dev/null) || fail "git リポジトリの中で実行してください"
cd "$root" || fail "cd $root に失敗しました"

[ $# -eq 0 ] && set -- HEAD
worktree=0
[ $# -eq 1 ] && [ "$1" = "HEAD" ] && worktree=1

diff=$(git diff "$@") || fail "git diff $* が失敗しました"
untracked=$(git ls-files --others --exclude-standard)
state_before=""
if [ "$worktree" = 1 ]; then
  # 作業ツリーのレビューでは、未追跡のファイルも新規ファイルとして差分に含める（コミットされうるので）
  while IFS= read -r -d '' f; do
    diff="${diff}
$(git diff --no-index -- /dev/null "$f")"
  done < <(git ls-files --others --exclude-standard -z)
  untracked=""
  state_before=$(state_of)
fi
if [ -z "$diff" ]; then
  echo "CROSS_REVIEW_EMPTY: 差分がありません（git diff $*）"
  exit 0
fi
bytes=$(printf '%s' "$diff" | wc -c | tr -d ' ')
[ "$bytes" -le "$MAX_BYTES" ] || fail "差分が ${bytes} バイトあり大きすぎます。パスかコミット範囲を絞るか、生成物を .gitignore に入れてください"

intro="あなたはコードレビュアーです。下の git diff をレビューし、バグ・回帰・セキュリティ問題だけを重大度順に挙げてください。スタイルや好みの指摘は不要です。各指摘には file:line と根拠を付けてください。問題がなければ「指摘なし」とだけ答えてください。"
with_reads="周辺のコードを確かめたいときは、このリポジトリ内のファイルの読み取りだけを使ってください。シェルコマンドは実行できません（実行しようとすると、その場でレビューが打ち切られます）。ファイルは変更しないでください。"
no_tools="ツールは一切使わず、diff の内容だけから判断してください。"
body="<diff>
${diff}
</diff>"

out=$(mktemp)
err=$(mktemp)
trap 'rm -f "$out" "$err"' EXIT

run_agy() {  # $1 = プロンプト, $2 = 制限時間（秒）
  agy -p "$1" --add-dir "$root" --model "$MODEL" --output-format json --print-timeout "${2}s" >"$out" 2>"$err"
  rc=$?
}
# 直前の結果が「打ち切り」か「空の応答」なら、理由（denied / empty）と会話 ID を出す
retry_reason() {
  python3 - "$out" "$err" <<'PY' 2>/dev/null
import json, sys
try:
    d = json.load(open(sys.argv[1], encoding="utf-8"))
except Exception:
    sys.exit()
if d.get("status") != "SUCCESS" or "print timeout" in open(sys.argv[2], encoding="utf-8", errors="replace").read():
    sys.exit()
if d.get("denied_actions"):
    print("denied", d.get("conversation_id") or "-")
elif not (d.get("response") or "").strip():
    print("empty", d.get("conversation_id") or "-")
PY
}

start=$(date +%s)
note=""
run_agy "${intro}
${with_reads}

${body}" "$BUDGET"
read -r reason first_id <<<"$(retry_reason)"
if [ -n "${reason:-}" ]; then
  case "$reason" in
    denied) why="agy が許可のない操作を試みて打ち切られた" ;;
    *)      why="agy の応答が空だった" ;;
  esac
  left=$(( BUDGET - ($(date +%s) - start) ))
  if [ "$left" -ge 60 ]; then
    note="1回目は ${why}ため、ツールを使わない指示で再実行した（1回目の会話 ID: ${first_id}）"
    run_agy "${intro}
${no_tools}

${body}" "$left"
  else
    note="1回目は ${why}が、残り時間が足りないため再実行しなかった（1回目の会話 ID: ${first_id}）"
  fi
fi

python3 - "$out" "$err" "$rc" "$MODEL" "$untracked" "$note" <<'PY'
import json, sys

out, err, rc, model, untracked, note = sys.argv[1], sys.argv[2], int(sys.argv[3]), sys.argv[4], sys.argv[5], sys.argv[6]
stderr = open(err, encoding="utf-8", errors="replace").read().strip()
cid = ""


def fail(msg):
    print("CROSS_REVIEW_FAILED:", msg)
    if cid:
        print(f"会話 ID: {cid}（agy の記録は ~/.gemini/antigravity-cli/brain/ と conversations/ にある）")
    if note:
        print("注: " + note)
    if stderr:
        print("--- agy stderr ---")
        print(stderr)
    sys.exit(1)


try:
    d = json.load(open(out, encoding="utf-8"))
except Exception as e:
    fail(f"agy の出力を JSON として読めません（exit={rc}, {e}）")
cid = d.get("conversation_id") or ""
if d.get("status") != "SUCCESS":
    fail(f"status={d.get('status')} error={d.get('error')}")
if "print timeout" in stderr:
    fail("制限時間内に終わらず、途中までの応答しかありません。範囲を絞ってください")
if d.get("denied_actions"):
    fail("agy が許可のない操作を試みて打ち切られました: " + json.dumps(d["denied_actions"], ensure_ascii=False))
resp = (d.get("response") or "").strip()
if not resp:
    fail("agy の応答が空です")
if rc != 0:
    fail(f"exit={rc}")

u = d.get("usage") or {}
print(f"CROSS_REVIEW_OK: model={model} duration={d.get('duration_seconds')}s tokens={u.get('total_tokens')}")
if note:
    print("注: " + note)
files = [f for f in untracked.splitlines() if f.strip()]
if files:
    print(f"未追跡のファイル {len(files)} 件は今回のレビュー対象外:")
    for f in files[:20]:
        print("  " + f)
    if len(files) > 20:
        print(f"  ...ほか {len(files) - 20} 件")
print("--- agy review ---")
print(resp)
PY
st=$?
if [ "$st" -eq 0 ]; then
  if [ "$worktree" = 1 ]; then
    state_after=$(state_of)
    if [ ! -f "$here/state.py" ]; then
      echo "注: state.py がないので、クロスレビュー済みの記録はしなかった（コミット前の確認には使えない）"
    elif [ -n "$state_before" ] && [ "$state_before" = "$state_after" ]; then
      printf '%s %s\n' "$state_before" "$(date +%s)" > "$(git rev-parse --git-dir)/cross-review-ok"
      echo "記録: この作業ツリーの状態をクロスレビュー済みとして記録した（コミット前の確認に使う）"
    else
      echo "注: レビューの途中で作業ツリーが変わったか、状態を計算できなかったので、クロスレビュー済みとして記録しなかった。コミットするには、もう一度実行が必要"
    fi
  fi
  echo "--- 対象の差分（git diff $*） ---"
  if [ "$bytes" -le 50000 ]; then
    printf '%s\n' "$diff"
  else
    git diff --stat "$@"
    echo "（差分が大きいので --stat だけ表示。中身は必要な箇所を読んで確かめる）"
  fi
fi
exit "$st"
