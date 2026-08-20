#!/bin/sh

set -eu

root_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
proto_path="$root_dir/contract-input/runtime.proto"
codes_path="$root_dir/contract-input/frozen_public_error_codes.json"
source_path="$root_dir/contract-input/SOURCE.txt"

if [ ! -f "$proto_path" ]; then
  echo "contract pin check: missing $proto_path" >&2
  exit 1
fi

# 产物缺失必须**报错**而不是跳过：一份"文件不在就不检查"的门禁，
# 在「有人把副本删了」这一形态上与「检查通过」完全同形。
if [ ! -f "$codes_path" ]; then
  echo "contract pin check: missing $codes_path" >&2
  echo "  该文件是中枢 backend/service/gateway/frozen_public_error_codes.json 的副本，" >&2
  echo "  同步方式=从中枢仓拷贝并同批更新 contract-input/SOURCE.txt 的 frozen_codes_* 三项。" >&2
  exit 1
fi

if [ ! -f "$source_path" ]; then
  echo "contract pin check: missing $source_path" >&2
  exit 1
fi

expected_hash=$(sed -n 's/^sha256:[[:space:]]*//p' "$source_path")
source_repo=$(sed -n 's/^source_repo:[[:space:]]*//p' "$source_path")
source_path_value=$(sed -n 's/^source_path:[[:space:]]*//p' "$source_path")
source_commit=$(sed -n 's/^source_commit:[[:space:]]*//p' "$source_path")
frozen_at=$(sed -n 's/^frozen_at:[[:space:]]*//p' "$source_path")
pinned_protoc=$(sed -n 's/^toolchain_protoc:[[:space:]]*//p' "$source_path")
pinned_gen_go=$(sed -n 's/^toolchain_protoc_gen_go:[[:space:]]*//p' "$source_path")
pinned_gen_go_grpc=$(sed -n 's/^toolchain_protoc_gen_go_grpc:[[:space:]]*//p' "$source_path")
codes_expected_hash=$(sed -n 's/^frozen_codes_sha256:[[:space:]]*//p' "$source_path")
codes_source_path_value=$(sed -n 's/^frozen_codes_source_path:[[:space:]]*//p' "$source_path")
codes_source_commit=$(sed -n 's/^frozen_codes_source_commit:[[:space:]]*//p' "$source_path")
codes_frozen_at=$(sed -n 's/^frozen_codes_frozen_at:[[:space:]]*//p' "$source_path")

if [ -z "$expected_hash" ] || [ -z "$source_repo" ] || \
   [ -z "$source_path_value" ] || [ -z "$source_commit" ] || \
   [ -z "$frozen_at" ] || [ -z "$pinned_protoc" ] || \
   [ -z "$pinned_gen_go" ] || [ -z "$pinned_gen_go_grpc" ] || \
   [ -z "$codes_expected_hash" ] || [ -z "$codes_source_path_value" ] || \
   [ -z "$codes_source_commit" ] || [ -z "$codes_frozen_at" ]; then
  echo "contract pin check: SOURCE.txt is missing required pin metadata" >&2
  exit 1
fi

# 两个被钉产物共用同一套格式校验与计算，避免第二份拷贝跑偏。
assert_sha256_shape() {
  # $1=值 $2=SOURCE.txt 里的键名
  shape_length=$(printf '%s' "$1" | awk '{print length}')
  case "$shape_length" in
    64) ;;
    *)
      echo "contract pin check: SOURCE.txt $2 must be 64 characters" >&2
      exit 1
      ;;
  esac
  case "$1" in
    *[!0123456789abcdef]*)
      echo "contract pin check: SOURCE.txt $2 must be lowercase hex" >&2
      exit 1
      ;;
  esac
}

# hashed_manifest 记的是**实际发生过**的哈希对拍，不是"打算哈希哪些"的第二份清单。
# 这个区别是本守卫成立的前提：如果它是一份独立声明，就会重新长出被守的那个洞——
# 「登记在册、却没人真去哈希」。现在登记这个动作只能由 record_hashed 完成，
# 而 record_hashed 只在一次真的 file_sha256 对拍通过之后才被调用。
hashed_manifest=""
record_hashed() {
  # $1=contract-input/ 下的相对文件名  $2=刚刚对拍通过的实际 SHA-256
  #
  # 之所以硬要第二个参数：让「登记」没法脱离「真的算过」单独发生。
  # 只收文件名的话，一句 record_hashed "new.json" 就能让一份从没被哈希的文件
  # 混过清点——那正是本守卫要消灭的形态，只是换了个地方长出来。
  assert_sha256_shape "$2" "record_hashed($1)"
  hashed_manifest="${hashed_manifest}
$1"
}

file_sha256() {
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk 'NR == 1 { print $1 }'
  elif command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk 'NR == 1 { print $1 }'
  else
    echo "contract pin check: no shasum or sha256sum available" >&2
    exit 1
  fi
}

assert_sha256_shape "$expected_hash" "sha256"
assert_sha256_shape "$codes_expected_hash" "frozen_codes_sha256"

actual_hash=$(file_sha256 "$proto_path")
if [ "$actual_hash" != "$expected_hash" ]; then
  echo "contract pin check: SHA-256 mismatch" >&2
  echo "  source commit: $source_commit" >&2
  echo "  expected:      $expected_hash" >&2
  echo "  actual:        $actual_hash" >&2
  exit 1
fi
record_hashed "runtime.proto" "$actual_hash"

codes_actual_hash=$(file_sha256 "$codes_path")
if [ "$codes_actual_hash" != "$codes_expected_hash" ]; then
  echo "contract pin check: frozen public error codes SHA-256 mismatch" >&2
  echo "  source commit: $codes_source_commit ($codes_source_path_value)" >&2
  echo "  expected:      $codes_expected_hash" >&2
  echo "  actual:        $codes_actual_hash" >&2
  echo "  修法：产物是中枢导出的，本仓只做副本——要更新就整份重拷并同批改 frozen_codes_* 三项，" >&2
  echo "        不要手改 JSON 内容来迁就本仓实现。" >&2
  exit 1
fi
record_hashed "frozen_public_error_codes.json" "$codes_actual_hash"

# 工具链对账。
#
# 承重的不是「SOURCE.txt 里有没有写版本号」，而是「写的那个版本号和生成物自己声明的
# 是不是同一个」—— 只有后者抓得住"换了工具链重生成却没声明"。SDK-012 就是这么漂的：
# 原产物头记 protoc v3.19.4，重生成用的是 v7.35.1，而 README 只说"pinned local protoc
# toolchain"、没有任何地方记版本号，于是那次跳跃在 diff 里只表现为一行头注释。
#
# ⚠ 这里刻意**不**去比对本机安装的 protoc：门禁要能在没装 protoc 的机器上跑，
# 而且真正要防的是"产物与声明不一致"，不是"这台机器上没有那个版本"。
#
# 两个生成物的头格式不同（pb.go 用 tab 对齐，grpc.pb.go 用 "- " 前缀），
# 所以按「行内出现该工具名后的下一个 vN.N.N 记号」提取，不要按列位置切。
extract_version() {
  # $1=文件 $2=工具名
  sed -n "s|^//[^A-Za-z]*$2[[:space:]][[:space:]]*\\(v[0-9][0-9.]*\\).*|\\1|p" "$1" | head -n 1
}

pb_path="$root_dir/runtime/runtime.pb.go"
grpc_path="$root_dir/runtime/runtime_grpc.pb.go"

for generated in "$pb_path" "$grpc_path"; do
  if [ ! -f "$generated" ]; then
    echo "contract pin check: missing $generated" >&2
    exit 1
  fi
done

check_toolchain() {
  # $1=文件 $2=工具名 $3=SOURCE.txt 里钉的值
  actual=$(extract_version "$1" "$2")
  if [ -z "$actual" ]; then
    echo "contract pin check: cannot read $2 version from $1" >&2
    exit 1
  fi
  if [ "$actual" != "$3" ]; then
    echo "contract pin check: $2 version mismatch" >&2
    echo "  file:     $1" >&2
    echo "  pinned:   $3" >&2
    echo "  actual:   $actual" >&2
    echo "  修法：重生成用的工具链变了就同批更新 contract-input/SOURCE.txt，不要改这里放行。" >&2
    exit 1
  fi
}

check_toolchain "$pb_path" "protoc-gen-go" "$pinned_gen_go"
check_toolchain "$pb_path" "protoc" "$pinned_protoc"
check_toolchain "$grpc_path" "protoc-gen-go-grpc" "$pinned_gen_go_grpc"
check_toolchain "$grpc_path" "protoc" "$pinned_protoc"

# ---- contract-input/ 目录清点 ----
#
# CONTRIBUTING 说 contract-input/ 只能有两类文件：被哈希的 **mirror**，
# 和装着期望值、因此不自哈希的 **pin record**。在这段之前，那条规矩只是散文——
# 上面的对拍走的是硬编码路径，不遍历目录，所以再丢一份未哈希的镜像进去门禁照样 OK。
# 那正是 reference/jcs-server-reference.go.txt 能在 key 排序上悄悄 stale 掉的原因：
# 它躺在 contract-input/ 下看着像冻结事实，却没有任何东西盯着它。
#
# pin_records 是**显式**清单，加一份新的钉记录必须来这里登记一行。这个摩擦是有意的：
# 「往 contract-input/ 里放东西」应该需要一次决定，而不是放进去就自动被接受。
pin_records="SOURCE.txt
GATEWAY_HTTP_ANCHOR.txt"

if [ ! -d "$root_dir/contract-input" ]; then
  echo "contract pin check: missing $root_dir/contract-input" >&2
  exit 1
fi

# grep -Fxq = 固定字符串 + **整行**匹配。整行这一条是承重的：
# 用子串匹配的话 SOURCE.txt 会顺带放行 MY_SOURCE.txt 和 SOURCE.txt.bak。
unregistered=$(
  find "$root_dir/contract-input" -type f -print | while IFS= read -r found_path; do
    relative=${found_path#"$root_dir/contract-input/"}
    if printf '%s\n%s\n' "$hashed_manifest" "$pin_records" | grep -Fxq -- "$relative"; then
      continue
    fi
    printf '%s\n' "$relative"
  done
)

if [ -n "$unregistered" ]; then
  echo "contract pin check: contract-input/ 下有既没被哈希、也没登记为 pin record 的文件：" >&2
  printf '%s\n' "$unregistered" | sed 's/^/    /' >&2
  echo "" >&2
  echo "  contract-input/ 只允许两类文件（见 CONTRIBUTING.md）：" >&2
  echo "    · mirror —— 中枢某个文件的副本，**必须**在本脚本里做 SHA-256 对拍；" >&2
  echo "      加法：照 runtime.proto 的样子加一次对拍，并在通过后 record_hashed <文件名>。" >&2
  echo "      同批要往 SOURCE.txt 里加它的 source_commit / sha256 / frozen_at。" >&2
  echo "    · pin record —— 装期望值的文件，本身不自哈希；加法：登记进本脚本的 pin_records。" >&2
  echo "" >&2
  echo "  没有第三类。一份不值得钉的副本就不该放在 contract-input/ 里——" >&2
  echo "  它会被当成冻结事实读，而没有任何东西保证它还是最新的。" >&2
  echo "  （如果这是 .DS_Store 之类的杂物：删掉它，或者让 .gitignore 管住它。）" >&2
  exit 1
fi

echo "contract pin check: OK ($actual_hash)"
echo "contract pin check: frozen codes OK ($codes_actual_hash, sluice@${codes_source_commit})"
echo "contract pin check: toolchain OK (protoc $pinned_protoc, protoc-gen-go $pinned_gen_go, protoc-gen-go-grpc $pinned_gen_go_grpc)"
inventory_count=$(find "$root_dir/contract-input" -type f | awk 'END { print NR }')
echo "contract pin check: contract-input inventory OK ($inventory_count files, all hashed or registered)"
