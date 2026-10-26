#!/bin/bash
#
# 构建 macOS PKG 安装包（M5 里程碑的脚手架，当前仅生成结构，不做签名与公证）。
#
# 用法：
#   ./packaging/macos/pkg/build-pkg.sh
#
# 前置条件：
#   - 已执行 `make build`，存在 bin/macpanel
#   - 安装 pkgbuild / productbuild（Xcode 或 Command Line Tools 自带）
#
# 重要：本脚本只生成安装包，不会安装、不会修改本机任何系统配置。

set -euo pipefail

# 避免 macOS 在拷贝时生成 ._* AppleDouble 文件混入 payload。
export COPYFILE_DISABLE=1
export COPY_EXTENDED_ATTRIBUTES_DISABLE=1

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
BUILD_DIR="${SCRIPT_DIR}/build"
IDENTIFIER="com.macpanel"

# 版本号：优先用 git tag；没有 tag 时用固化的语义化版本，
# 避免安装包版本号变成一串提交哈希。
if TAG="$(cd "${REPO_ROOT}" && git describe --tags --abbrev=0 2>/dev/null)"; then
	VERSION="${TAG#v}"
else
	VERSION="0.1.0"
fi

BINARY="${REPO_ROOT}/bin/macpanel"
PAYLOAD_ROOT="${BUILD_DIR}/payload"
SCRIPTS_DIR="${BUILD_DIR}/scripts"

echo "==> 版本: ${VERSION}"

if [[ ! -f "${BINARY}" ]]; then
	echo "错误: 未找到 ${BINARY}，请先运行 make build" >&2
	exit 1
fi

if ! command -v pkgbuild >/dev/null 2>&1; then
	echo "错误: 缺少 pkgbuild，请安装 Xcode Command Line Tools: xcode-select --install" >&2
	exit 1
fi

echo "==> 清理构建目录"
rm -rf "${BUILD_DIR}"
mkdir -p "${PAYLOAD_ROOT}/usr/local/bin"
mkdir -p "${PAYLOAD_ROOT}/Library/LaunchDaemons"
mkdir -p "${SCRIPTS_DIR}"

# 说明：数据目录、日志目录、运行目录一律由 postinstall 创建。
# 不放进 payload 的原因：这些目录需要 root:wheel 0750 等严格权限，
# pkgbuild 在为非 root 用户打包时无法写入，会产生 write: Permission denied。
# 由安装脚本创建也能保证权限一定符合预期，而不依赖打包机器的 umask。

echo "==> 复制二进制与 launchd 定义"
install -m 0755 "${BINARY}" "${PAYLOAD_ROOT}/usr/local/bin/macpanel"

# 卸载脚本随包安装：出事的人最需要它，而那时他大概率已经找不到下载回来的 DMG 了。
install -m 0755 "${REPO_ROOT}/packaging/macos/uninstall.sh" "${PAYLOAD_ROOT}/usr/local/bin/macpanel-uninstall"

for plist in "${REPO_ROOT}"/packaging/macos/launchd/*.plist; do
	name="$(basename "${plist}")"
	sed -e "s|__BINARY_PATH__|/usr/local/bin/macpanel|g" \
		-e "s|__LOG_DIR__|/Library/Logs/MacPanel|g" \
		"${plist}" > "${PAYLOAD_ROOT}/Library/LaunchDaemons/${name}"
	chmod 0644 "${PAYLOAD_ROOT}/Library/LaunchDaemons/${name}"
done

echo "==> 生成安装前脚本（升级前备份）"
#
# 为什么必须有 preinstall：升级会覆盖 /usr/local/bin/macpanel
# 并重启服务，此时若新版本的数据库迁移或配置校验失败，旧数据就是唯一的退路。
# PLAN.md 第 10 节把「安装前备份」列为硬要求。
#
# 与 postinstall 的分工：
#   preinstall  停服务 → 备份数据/令牌/PF 现状（此时旧二进制还在）
#   postinstall 建账户 → 建目录 → 生成令牌 → 加载服务
cat > "${SCRIPTS_DIR}/preinstall" <<'PREINSTALL'
#!/bin/bash
set -uo pipefail

BIN="/usr/local/bin/macpanel"
DATA_DIR="/Library/Application Support/MacPanel"
TOKEN="${DATA_DIR}/ipc.token"
ANCHOR="/etc/pf.anchors/com.macpanel.ipv6"
ANCHOR_NAME="com.macpanel.ipv6"
# 保留的备份份数上限：够回溯几次升级，又不会让数据目录无限增长。
KEEP=5

# 全新安装时没有任何东西可备份，直接返回成功。
if [[ ! -d "${DATA_DIR}" ]]; then
	echo "全新安装：无需备份"
	exit 0
fi

echo "检测到既有安装，开始升级前备份"

# 1. 先停服务再备份。
#    SQLite 处于 WAL 模式，直接复制运行中的文件可能拿到不一致的快照；
#    停服 '.backup' 之前先让它 checkpoint 完毕是最省事的做法。
launchctl bootout system /Library/LaunchDaemons/com.macpanel.server.plist 2>/dev/null || true
launchctl bootout system /Library/LaunchDaemons/com.macpanel.agent.plist 2>/dev/null || true

STAMP="$(date +%Y%m%d-%H%M%S)"
BACKUP_ROOT="${DATA_DIR}/backups"
BACKUP_DIR="${BACKUP_ROOT}/preupgrade-${STAMP}"
mkdir -p "${BACKUP_DIR}"

# 2. 数据库与令牌
[[ -d "${DATA_DIR}/data" ]] && cp -a "${DATA_DIR}/data" "${BACKUP_DIR}/data"
[[ -f "${TOKEN}" ]] && cp -a "${TOKEN}" "${BACKUP_DIR}/ipc.token"

# 3. PF 现状：文件 + 实际生效的规则。
#    两者可能不一致（文件改了没加载），都要留。
if [[ -f "${ANCHOR}" ]]; then
	cp -a "${ANCHOR}" "${BACKUP_DIR}/anchor-rules.conf"
fi
if [[ -x /sbin/pfctl ]]; then
	/sbin/pfctl -a "${ANCHOR_NAME}" -sr > "${BACKUP_DIR}/anchor-live.rules" 2>/dev/null || true
	/sbin/pfctl -s info > "${BACKUP_DIR}/pf-info.txt" 2>/dev/null || true
	/sbin/pfctl -s Anchors > "${BACKUP_DIR}/pf-anchors.txt" 2>/dev/null || true
fi
[[ -f /etc/pf.conf ]] && cp -a /etc/pf.conf "${BACKUP_DIR}/pf.conf" 2>/dev/null || true

chmod -R 0700 "${BACKUP_DIR}"
echo "已备份到 ${BACKUP_DIR}"

# 4. 只保留最近 KEEP 份，避免每次升级都留一份把磁盘慢慢吃光
if ls -1d "${BACKUP_ROOT}"/preupgrade-* >/dev/null 2>&1; then
	ls -1d "${BACKUP_ROOT}"/preupgrade-* | sort | head -n -"${KEEP}" | while read -r old; do
		rm -rf "${old}"
		echo "已清理旧备份 $(basename "${old}")"
	done
fi

# 备份失败不能让安装继续：宁可停在半路，也不要在没有退路的情况下升级。
if [[ ! -d "${BACKUP_DIR}" ]]; then
	echo "错误: 备份失败，中止安装" >&2
	exit 1
fi
exit 0
PREINSTALL
chmod 0755 "${SCRIPTS_DIR}/preinstall"

echo "==> 生成安装后脚本"
cat > "${SCRIPTS_DIR}/postinstall" <<'POSTINSTALL'
#!/bin/bash
set -euo pipefail

BIN="/usr/local/bin/macpanel"
DATA_DIR="/Library/Application Support/MacPanel"
RUN_DIR="/var/run/macpanel"
LOG_DIR="/Library/Logs/MacPanel"
TOKEN="${DATA_DIR}/ipc.token"

# 1. 创建专用非特权账户（已存在则跳过）
#
# UID/GID 从 350 起向下找空位：硬编码一个值在已装过其它软件的机器上
# 会撞号，而 dscl -create 失败会连带让整个安装器失败。
HM_UID=350
HM_GID=350
uid_in_use() { dscl . -list /Users UniqueID 2>/dev/null | awk '{print $2}' | grep -qx "$1"; }
gid_in_use() { dscl . -list /Groups PrimaryGroupID 2>/dev/null | awk '{print $2}' | grep -qx "$1"; }

if ! dscl . -read /Users/_macpanel >/dev/null 2>&1; then
	while uid_in_use "${HM_UID}"; do
		HM_UID=$((HM_UID - 1))
	done
	echo "创建服务账户 _macpanel（UID ${HM_UID}）"
	dscl . -create /Users/_macpanel
	dscl . -create /Users/_macpanel RealName "MacPanel"
	dscl . -create /Users/_macpanel UniqueID "${HM_UID}"
	dscl . -create /Users/_macpanel UserShell /usr/bin/false
	dscl . -create /Users/_macpanel NFSHomeDirectory /var/empty
	dscl . -create /Users/_macpanel IsHidden 1
fi

# 组单独判定：只建用户、不建组会让 UserName=_macpanel 的 launchd 任务起不来。
if ! dscl . -read /Groups/_macpanel >/dev/null 2>&1; then
	while gid_in_use "${HM_GID}"; do
		HM_GID=$((HM_GID - 1))
	done
	echo "创建服务组 _macpanel（GID ${HM_GID}）"
	dscl . -create /Groups/_macpanel
	dscl . -create /Groups/_macpanel PrimaryGroupID "${HM_GID}"
	dscl . -create /Groups/_macpanel RealName "MacPanel"
fi

# 无论账户是这次新建的还是已存在的，都确保两者 ID 对齐。
dscl . -create /Users/_macpanel PrimaryGroupID "$(dscl . -read /Groups/_macpanel PrimaryGroupID | awk '{print $2}')"

# 2. 目录与权限
#    Agent 以 root 运行；Server 以 _macpanel 运行，只能读取令牌、不能写入。
mkdir -p "${DATA_DIR}/data" "${RUN_DIR}" "${LOG_DIR}"
chown -R root:wheel "${DATA_DIR}"
chmod 0750 "${DATA_DIR}"
chown -R _macpanel:_macpanel "${DATA_DIR}/data"
chmod 0700 "${DATA_DIR}/data"
chown root:_macpanel "${RUN_DIR}"
chmod 0750 "${RUN_DIR}"
chown -R _macpanel:_macpanel "${LOG_DIR}"
chmod 0750 "${LOG_DIR}"

# 3. 生成 IPC 共享令牌（仅在不存在时）
#    令牌文件 root:_macpanel 0640：Server 可读，其他账户不可读，root 可写。
if [[ ! -f "${TOKEN}" ]]; then
	echo "生成 IPC 令牌"
	"${BIN}" token -force >/dev/null
	chown root:_macpanel "${TOKEN}"
	chmod 0640 "${TOKEN}"
else
	echo "IPC 令牌已存在，保留原值"
fi

# 4. 加载 launchd 服务（先卸旧再装新，避免残留旧定义）
echo "加载 launchd 服务"
launchctl bootout system /Library/LaunchDaemons/com.macpanel.agent.plist 2>/dev/null || true
launchctl bootout system /Library/LaunchDaemons/com.macpanel.server.plist 2>/dev/null || true
launchctl bootstrap system /Library/LaunchDaemons/com.macpanel.agent.plist
launchctl bootstrap system /Library/LaunchDaemons/com.macpanel.server.plist

echo "安装完成。管理面板: http://127.0.0.1:7788"
echo ""
echo "说明："
echo "  · 安装器不会启用任何 PF 规则——IPv6 防火墙需要你在面板里显式编辑并应用。"
echo "  · 应用规则会进入 60 秒确认倒计时，未确认会自动回滚。"
echo "  · 若规则导致 SSH 失联，在本机终端执行："
echo "      sudo ${BIN} firewall status"
echo "      sudo ${BIN} firewall rollback"
echo ""
echo "卸载：sudo /usr/local/bin/macpanel-uninstall"
exit 0
POSTINSTALL
chmod 0755 "${SCRIPTS_DIR}/postinstall"

echo "==> 构建组件包"
COMPONENT_PKG="${BUILD_DIR}/component.pkg"

# macOS 会给本地产生的文件写上 com.apple.provenance。它是内核合成的：
# 删除调用返回成功但属性仍在（本机实测 xattr -c / -d / removexattr 都拿不掉）。
# pkgbuild 处理它时会为每条记录额外生成 `._xxx` AppleDouble 一并塞进 BOM，
# 安装后系统里会多出几个隐藏文件——功能不受影响，卸载脚本会顺手清掉。
# 这里仍然尽力尝试清除：换到 CI 或干净构建机上通常就不需要这一步了。
find "${PAYLOAD_ROOT}" -exec xattr -d com.apple.provenance {} \; 2>/dev/null || true
find "${PAYLOAD_ROOT}" -exec xattr -d com.apple.quarantine {} \; 2>/dev/null || true
xattr -cr "${PAYLOAD_ROOT}" 2>/dev/null || true

# --ownership recommended：交给安装器按目标文件系统分配属主，
# 避免把打包机上的用户名（如 han:staff）烧进包里。
pkgbuild \
	--root "${PAYLOAD_ROOT}" \
	--scripts "${SCRIPTS_DIR}" \
	--identifier "${IDENTIFIER}" \
	--version "${VERSION}" \
	--install-location "/" \
	--ownership recommended \
	"${COMPONENT_PKG}"

echo "==> 构建分发包"
OUTPUT_PKG="${REPO_ROOT}/MacPanel-${VERSION}.pkg"
productbuild \
	--package "${COMPONENT_PKG}" \
	--identifier "${IDENTIFIER}" \
	--version "${VERSION}" \
	"${OUTPUT_PKG}"

echo ""
echo "已生成: ${OUTPUT_PKG}"
echo "大小: $(du -h "${OUTPUT_PKG}" | cut -f1)"
echo ""
echo "下一步（需人工执行并验证）："
echo "  1. 静态校验（不需要 root）:  make verify-pkg"
echo "  2. 在测试机上安装:          sudo installer -pkg '${OUTPUT_PKG}' -target /"
echo "  3. 验证服务:                sudo launchctl print system/com.macpanel.agent | head -20"
echo "  4. 验证面板:                curl -s http://127.0.0.1:7788/api/v1/healthz"
echo "  5. 验证防火墙救援通道:      sudo /usr/local/bin/macpanel firewall status"
echo "  6. 卸载测试:                sudo macpanel-uninstall"
echo "  7. 完整验收清单:            docs/m5-acceptance.md"
echo ""
echo "警告：本包未签名。分发前需 Developer ID 签名与公证，否则 Gatekeeper 会拦截。"
