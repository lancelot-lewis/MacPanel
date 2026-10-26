#!/bin/bash
#
# PKG 静态校验 —— 不需要 root，不会安装任何东西。
#
# 用法:
#   scripts/verify-pkg.sh                 # 校验仓库根目录下最新的 HomeLabManager-*.pkg
#   scripts/verify-pkg.sh <path.pkg>      # 校验指定包
#
# 为什么需要它：真正的安装会创建账户、写 launchd、占用 7788 端口，
# 在开发机上反复做既不现实也不安全。但「包本身有没有打包错」完全可以静态查：
# 卸载脚本忘了进包、plist 里的占位符没替换、launchd 运行身份写反——
# 这些错误装一次要花十分钟才会暴露，静态校验只要一秒。
#
# 它不能替代真机安装： see docs/m5-acceptance.md 的人工清单。
# 本脚本只回答「包的结构是否正确」。

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

PASS_COUNT=0
FAIL_COUNT=0
WARN_COUNT=0
FAILURES=()
WARNINGS=()

warn() {
	echo "  ! $1"
	WARN_COUNT=$((WARN_COUNT + 1))
	WARNINGS+=("$1")
}

ok() {
	echo "  ✓ $1"
	PASS_COUNT=$((PASS_COUNT + 1))
}

fail() {
	echo "  ✗ $1"
	FAIL_COUNT=$((FAIL_COUNT + 1))
	FAILURES+=("$1")
}

check() {
	# $1 描述  $2 退出码
	if [[ "$2" -eq 0 ]]; then
		ok "$1"
	else
		fail "$1"
	fi
}

# ---- 定位待校验的包 ----

if [[ $# -ge 1 ]]; then
	PKG="$1"
else
	# 注意：macOS 自带的 bash 是 3.2，不支持 negative subscript（arr[-1]），
	# 因此这里用 tail 取最后一个，而不是 `${ALL[-1]}`。
	ALL="$(cd "${REPO_ROOT}" && ls HomeLabManager-*.pkg 2>/dev/null | sort -V)"
	if [[ -z "${ALL}" ]]; then
		echo "错误: 未找到 HomeLabManager-*.pkg，请先运行 make pkg" >&2
		exit 2
	fi
	PKG="${REPO_ROOT}/$(printf '%s\n' "${ALL}" | tail -1)"
fi

if [[ ! -f "${PKG}" ]]; then
	echo "错误: 包不存在: ${PKG}" >&2
	exit 2
fi

echo "校验对象: ${PKG}"
echo "大小:     $(du -h "${PKG}" | cut -f1)"
echo ""

# ---- 1. shell 脚本语法 ----

echo "[1/7] shell 脚本语法"
shopt -s nullglob
SCRIPTS=(
	"${REPO_ROOT}"/scripts/*.sh
	"${REPO_ROOT}"/packaging/macos/uninstall.sh
	"${REPO_ROOT}"/packaging/macos/pkg/*.sh
	"${REPO_ROOT}"/packaging/macos/dmg/*.sh
)
shopt -u nullglob

if [[ ${#SCRIPTS[@]} -eq 0 ]]; then
	fail "没有找到任何 shell 脚本"
else
	for s in "${SCRIPTS[@]}"; do
		bash -n "${s}" >/dev/null 2>&1
		check "bash -n $(basename "${s}")" $?
	done
fi

# ---- 2. plist 语法 ----

echo ""
echo "[2/7] launchd plist 语法"
for p in "${REPO_ROOT}"/packaging/macos/launchd/*.plist; do
	plutil -lint "${p}" >/dev/null 2>&1
	check "plutil -lint $(basename "${p}")" $?
done

# ---- 3. 模板占位符已替换 ----

echo ""
echo "[3/7] 模板占位符"
PLACEHOLDER_FOUND=0
for p in "${REPO_ROOT}"/packaging/macos/launchd/*.plist; do
	if grep -q -e "__BINARY_PATH__" -e "__LOG_DIR__" "${p}" 2>/dev/null; then
		PLACEHOLDER_FOUND=1
	fi
done
# plist 源文件里带占位符是正常的（它们是模板，由安装脚本 sed 替换）；
# 这里确认「模板仍然是模板」，否则一旦有人无意中改成了硬编码路径，
# 打包出来的 dev 路径会被静默带进安装包。
if [[ "${PLACEHOLDER_FOUND:-0}" -eq 1 ]]; then
	ok "模板占位符存在（预期，由安装脚本替换）"
else
	fail "模板占位符缺失，请确认 plist 是否已被硬编码路径替换"
fi

# ---- 4. 展开并检查包结构 ----

echo ""
echo "[4/7] 包结构"
WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT

# pkgutil --expand 要求目标目录不存在，因此给出尚不存在的子路径。
PROD="${WORK}/product"
COMP="${WORK}/component"
pkgutil --expand "${PKG}" "${PROD}" >/dev/null 2>&1
check "productbuild 分发包可展开" $?

if [[ -d "${PROD}" ]]; then
	# productbuild 产出的是 distribution 包：expand 之后里面是一个
	# 已展开的 component.pkg 目录。**不能**对它再 expand 一次
	# （pkgutil 会报 "Could not open package for expansion"）。
	if [[ -f "${PROD}/Distribution" ]]; then
		COMP="$(find "${PROD}" -mindepth 1 -maxdepth 1 -type d -name '*.pkg' | head -1)"
		check "分发包内含组件包" "$([[ -n "${COMP}" ]] && echo 0 || echo 1)"
	else
		COMP="${PROD}"
		ok "单组件包（无 Distribution）"
	fi
else
	COMP=""
fi

if [[ -d "${COMP}" ]]; then
	[[ -f "${COMP}/Bom" ]]
	check "存在 BOM（文件清单）" $?
	[[ -f "${COMP}/Payload" ]]
	check "存在 Payload（压缩负载）" $?
	[[ -f "${COMP}/PackageInfo" ]]
	check "存在 PackageInfo" $?
	[[ -d "${COMP}/Scripts" ]]
	check "存在 Scripts（安装脚本）" $?
	[[ -f "${COMP}/Scripts/preinstall" ]]
	check "存在 preinstall（升级前备份）" $?
	[[ -f "${COMP}/Scripts/postinstall" ]]
	check "存在 postinstall" $?
fi

# ---- 5. 安装后脚本不残留占位符 ----

echo ""
echo "[5/7] 安装脚本内容"
if [[ -d "${COMP}/Scripts" ]]; then
	for s in preinstall postinstall; do
		f="${COMP}/Scripts/${s}"
		[[ -f "${f}" ]] || continue
		grep -q "__BINARY_PATH__\|__LOG_DIR__" "${f}"
		if [[ $? -eq 0 ]]; then
			fail "${s} 残留未替换的占位符"
		else
			ok "${s} 无占位符残留"
		fi
	done

	if [[ -f "${COMP}/Scripts/preinstall" ]]; then
		grep -q "backups" "${COMP}/Scripts/preinstall"
		check "preinstall 会做升级前备份" $?
	fi
	if [[ -f "${COMP}/Scripts/postinstall" ]]; then
		grep -q "homelab-manager-uninstall" "${COMP}/Scripts/postinstall"
		check "postinstall 提到了随包安装的卸载命令" $?
		grep -q "firewall rollback" "${COMP}/Scripts/postinstall"
		check "postinstall 输出了防火墙救援命令" $?
	fi
fi

# ---- 6. 文件清单与权限 ----

echo ""
echo "[6/7] Payload 文件清单"
if [[ -f "${COMP}/Bom" ]]; then
	# -f 输出格式：path<TAB>mode(八进制)<TAB>uid/gid<TAB>size<TAB>checksum
	BOM_OUT="$(lsbom -f "${COMP}/Bom" 2>/dev/null)"
	if [[ -z "${BOM_OUT}" ]]; then
		fail "BOM 为空，Payload 里什么都不会安装"
	else
		ok "BOM 含 $(echo "${BOM_OUT}" | wc -l | tr -d ' ') 条记录"

		# 关键文件必须存在，且权限符合预期。
		# 权限写错不会让安装失败，但会让服务静默跑不起来。
		bom_has() {
			local path="$1" want="$2"
			local line mode
			line="$(echo "${BOM_OUT}" | grep -F "${path}	" | head -1)"
			if [[ -z "${line}" ]]; then
				fail "缺少 ${path}"
				return
			fi
			mode="$(echo "${line}" | awk -F'\t' '{print $2}')"
			if [[ -n "${want}" && "${mode}" != "${want}" ]]; then
				fail "${path} 权限为 ${mode}，期望 ${want}"
				return
			fi
			ok "${path}（${mode}）"
		}

		bom_has "./usr/local/bin/homelab-manager" "100755"
		bom_has "./usr/local/bin/homelab-manager-uninstall" "100755"
		bom_has "./Library/LaunchDaemons/com.homelab.manager.agent.plist" "100644"
		bom_has "./Library/LaunchDaemons/com.homelab.manager.server.plist" "100644"

		# 不许包进东西：数据目录一旦进了 payload，
		# 升级时会覆盖用户现有的数据库。
		if echo "${BOM_OUT}" | grep -q "Application Support"; then
			fail "Payload 不应包含数据目录（会覆盖用户数据）"
		else
			ok "未包含数据目录（由 postinstall 创建）"
		fi

		# AppleDouble 残留：本机 macOS 会给构建产物打上 com.apple.provenance，
		# 它是内核合成的、删除调用返回成功但实际仍在；pkgbuild 读到它就为每条记录
		# 额外生成一份 ._xxx 一并塞进 BOM。安装只是多几个隐藏文件，功能不受影响，
		# 因此记为警告而非失败。更换干净的构建机（或 CI）打包通常不再出现。
		AD_COUNT="$(echo "${BOM_OUT}" | grep -c '/\._' || true)"
		if [[ "${AD_COUNT}" -gt 0 ]]; then
			warn "含 ${AD_COUNT} 条 AppleDouble（._xxx）记录——本机构建环境的已知现象，详见 docs/m5-acceptance.md"
		else
			ok "无 AppleDouble 残留"
		fi
	fi
fi

# ---- 7. launchd 运行身份 ----

echo ""
echo "[7/7] launchd 运行身份"
PAYLOAD_EXTRACT="${WORK}/payload"
if [[ -f "${COMP}/Payload" ]]; then
	mkdir -p "${PAYLOAD_EXTRACT}"
	(cd "${PAYLOAD_EXTRACT}" && cat "${COMP}/Payload" | gunzip -c | cpio -i --quiet 2>/dev/null)

	AGENT="${PAYLOAD_EXTRACT}/Library/LaunchDaemons/com.homelab.manager.agent.plist"
	SERVER="${PAYLOAD_EXTRACT}/Library/LaunchDaemons/com.homelab.manager.server.plist"

	if [[ -f "${AGENT}" ]]; then
		grep -A1 "<key>UserName</key>" "${AGENT}" | grep -q "<string>root</string>"
		check "agent 以 root 运行" $?
		grep -q "/usr/local/bin/homelab-manager" "${AGENT}"
		check "agent 的二进制路径已替换" $?
	else
		fail "Payload 里找不到 agent plist"
	fi

	if [[ -f "${SERVER}" ]]; then
		grep -A1 "<key>UserName</key>" "${SERVER}" | grep -q "<string>_homelab</string>"
		check "server 以非特权账户 _homelab 运行" $?
	else
		fail "Payload 里找不到 server plist"
	fi

	# PF anchor 名必须带 manager 前缀：本机已有一个无关的 com.homelab.ipv6，
	# 混用会写到别人的 anchor 里。
	if [[ -f "${AGENT}" ]]; then
		grep -q "com.homelab.manager.ipv6" "${AGENT}"
		check "anchor 名带 manager 前缀（不与他人冲突）" $?
	fi
else
	fail "组件包没有 Payload"
fi

# ---- 结论 ----

echo ""
echo "────────────────────────────────"
echo "通过 ${PASS_COUNT} 项，警告 ${WARN_COUNT} 项，失败 ${FAIL_COUNT} 项"

if [[ ${WARN_COUNT} -gt 0 ]]; then
	echo ""
	echo "警告（不影响可用性，但值得知道）："
	for w in "${WARNINGS[@]}"; do
		echo "  · ${w}"
	done
fi

if [[ ${FAIL_COUNT} -gt 0 ]]; then
	echo ""
	echo "失败项："
	for f in "${FAILURES[@]}"; do
		echo "  · ${f}"
	done
	echo ""
	echo "静态校验不能替代真机安装。完整清单见 docs/m5-acceptance.md。"
	exit 1
fi

echo "包结构正确。下一步（需 root，请在测试机执行）："
echo "  sudo installer -pkg '${PKG}' -target /"
echo "  sudo launchctl print system/com.homelab.manager.agent | head -20"
echo "  curl -s http://127.0.0.1:7788/api/v1/healthz"
exit 0
