#!/bin/bash
#
# 构建 macOS PKG 安装包（M5 里程碑的脚手架，当前仅生成结构，不做签名与公证）。
#
# 用法：
#   ./packaging/macos/pkg/build-pkg.sh
#
# 前置条件：
#   - 已执行 `make build`，存在 bin/homelab-manager
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
IDENTIFIER="com.homelab.manager"

# 版本号：优先用 git tag；没有 tag 时用固化的语义化版本，
# 避免安装包版本号变成一串提交哈希。
if TAG="$(cd "${REPO_ROOT}" && git describe --tags --abbrev=0 2>/dev/null)"; then
	VERSION="${TAG#v}"
else
	VERSION="0.1.0"
fi

BINARY="${REPO_ROOT}/bin/homelab-manager"
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
install -m 0755 "${BINARY}" "${PAYLOAD_ROOT}/usr/local/bin/homelab-manager"

for plist in "${REPO_ROOT}"/packaging/macos/launchd/*.plist; do
	name="$(basename "${plist}")"
	sed -e "s|__BINARY_PATH__|/usr/local/bin/homelab-manager|g" \
		-e "s|__LOG_DIR__|/Library/Logs/HomeLabManager|g" \
		"${plist}" > "${PAYLOAD_ROOT}/Library/LaunchDaemons/${name}"
	chmod 0644 "${PAYLOAD_ROOT}/Library/LaunchDaemons/${name}"
done

echo "==> 生成安装后脚本"
cat > "${SCRIPTS_DIR}/postinstall" <<'POSTINSTALL'
#!/bin/bash
set -euo pipefail

BIN="/usr/local/bin/homelab-manager"
DATA_DIR="/Library/Application Support/HomeLabManager"
RUN_DIR="/var/run/homelab-manager"
LOG_DIR="/Library/Logs/HomeLabManager"
TOKEN="${DATA_DIR}/ipc.token"

# 1. 创建专用非特权账户（已存在则跳过）
if ! dscl . -read /Users/_homelab >/dev/null 2>&1; then
	echo "创建服务账户 _homelab"
	# 使用隐藏账户，UID 由系统分配（范围 400-499 为系统保留）
	dscl . -create /Users/_homelab
	dscl . -create /Users/_homelab RealName "HomeLab Manager"
	dscl . -create /Users/_homelab UniqueID 350
	dscl . -create /Users/_homelab PrimaryGroupID 350
	dscl . -create /Users/_homelab UserShell /usr/bin/false
	dscl . -create /Users/_homelab NFSHomeDirectory /var/empty
	dscl . -create /Users/_homelab IsHidden 1
	dscl . -create /Groups/_homelab
	dscl . -create /Groups/_homelab PrimaryGroupID 350
	dscl . -create /Groups/_homelab RealName "HomeLab Manager"
fi

# 2. 目录与权限
#    Agent 以 root 运行；Server 以 _homelab 运行，只能读取令牌、不能写入。
mkdir -p "${DATA_DIR}/data" "${RUN_DIR}" "${LOG_DIR}"
chown -R root:wheel "${DATA_DIR}"
chmod 0750 "${DATA_DIR}"
chown -R _homelab:_homelab "${DATA_DIR}/data"
chmod 0700 "${DATA_DIR}/data"
chown root:_homelab "${RUN_DIR}"
chmod 0750 "${RUN_DIR}"
chown -R _homelab:_homelab "${LOG_DIR}"
chmod 0750 "${LOG_DIR}"

# 3. 生成 IPC 共享令牌（仅在不存在时）
#    令牌文件 root:_homelab 0640：Server 可读，其他账户不可读，root 可写。
if [[ ! -f "${TOKEN}" ]]; then
	echo "生成 IPC 令牌"
	"${BIN}" token -force >/dev/null
	chown root:_homelab "${TOKEN}"
	chmod 0640 "${TOKEN}"
else
	echo "IPC 令牌已存在，保留原值"
fi

# 4. 加载 launchd 服务（先卸旧再装新，避免残留旧定义）
echo "加载 launchd 服务"
launchctl bootout system /Library/LaunchDaemons/com.homelab.manager.agent.plist 2>/dev/null || true
launchctl bootout system /Library/LaunchDaemons/com.homelab.manager.server.plist 2>/dev/null || true
launchctl bootstrap system /Library/LaunchDaemons/com.homelab.manager.agent.plist
launchctl bootstrap system /Library/LaunchDaemons/com.homelab.manager.server.plist

echo "安装完成。管理面板: http://127.0.0.1:7788"
echo "注意：本版本（M1）未安装任何 PF 规则，不会修改系统防火墙。"
exit 0
POSTINSTALL
chmod 0755 "${SCRIPTS_DIR}/postinstall"

echo "==> 生成卸载脚本说明（不包含在 PKG 中，随文档分发）"
cat > "${BUILD_DIR}/uninstall.sh" <<'UNINSTALL'
#!/bin/bash
# HomeLab Manager 卸载脚本
#
# 用途：移除本应用的服务、launchd 定义、二进制与（可选）数据。
#
# 安全原则：
#   - 绝不执行 pfctl -d（那会导致整机防火墙关闭）
#   - 不删除其它程序的 anchor 或 launchd 项
#   - 数据默认保留，需显式确认才删除
set -euo pipefail

echo "==> 停止并移除 launchd 服务"
launchctl bootout system /Library/LaunchDaemons/com.homelab.manager.agent.plist 2>/dev/null || true
launchctl bootout system /Library/LaunchDaemons/com.homelab.manager.server.plist 2>/dev/null || true
rm -f /Library/LaunchDaemons/com.homelab.manager.agent.plist
rm -f /Library/LaunchDaemons/com.homelab.manager.server.plist

echo "==> 移除可执行文件"
rm -f /usr/local/bin/homelab-manager

echo "==> 移除运行时目录"
rm -rf /var/run/homelab-manager

echo "==> 处理本应用的 PF anchor"
ANCHOR="/etc/pf.anchors/com.homelab.manager.ipv6"
if [[ -f "${ANCHOR}" ]]; then
	echo "检测到 anchor 文件: ${ANCHOR}"
	echo "注意：必须先确认 /etc/pf.conf 中的引用已移除，否则 PF 重载会报错。"
	echo "本脚本不会自动修改 /etc/pf.conf，也不会关闭 PF。"
	echo "如需移除引用，请手动编辑 /etc/pf.conf 并执行: sudo pfctl -f /etc/pf.conf"
	echo "保留 anchor 文件。如需删除: sudo rm '${ANCHOR}'"
fi

echo "==> 数据处理"
read -r -p "是否删除数据库与配置 (/Library/Application Support/HomeLabManager)? [y/N] " answer
case "${answer}" in
	y|Y)
		rm -rf "/Library/Application Support/HomeLabManager"
		echo "已删除数据"
		;;
	*)
		echo "已保留数据: /Library/Application Support/HomeLabManager"
		;;
esac

read -r -p "是否删除日志 (/Library/Logs/HomeLabManager)? [y/N] " answer2
case "${answer2}" in
	y|Y) rm -rf /Library/Logs/HomeLabManager ;;
	*) echo "已保留日志" ;;
esac

echo "卸载完成。"
UNINSTALL
chmod 0755 "${BUILD_DIR}/uninstall.sh"

echo "==> 构建组件包"
COMPONENT_PKG="${BUILD_DIR}/component.pkg"

# 清除扩展属性：macOS 会为下载/构建产物打 com.apple.provenance，
# pkgbuild 会把这些属性写成 payload 里的 ._* 旁支文件，
# 并因权限不足输出 "write: Permission denied"。安装包里不需要这些元数据。
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
OUTPUT_PKG="${REPO_ROOT}/HomeLabManager-${VERSION}.pkg"
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
echo "  1. 在测试机上安装: sudo installer -pkg '${OUTPUT_PKG}' -target /"
echo "  2. 验证服务: launchctl print system/com.homelab.manager.agent | head -20"
echo "  3. 验证面板: curl -s http://127.0.0.1:7788/api/v1/healthz"
echo "  4. 卸载测试: sudo '${BUILD_DIR}/uninstall.sh'"
echo ""
echo "警告：本包未签名。分发前需 Developer ID 签名与公证，否则 Gatekeeper 会拦截。"
