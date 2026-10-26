#!/bin/bash
#
# 构建 DMG 分发容器（内含 PKG 安装器）。
#
# 用法：
#   ./packaging/macos/dmg/build-dmg.sh
#
# 前置条件：
#   - 已执行 ./packaging/macos/pkg/build-pkg.sh，生成 MacPanel-<version>.pkg
#
# 注意：本脚本只生成 DMG，不会挂载到 /Applications，也不会安装任何内容。

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
STAGING="${SCRIPT_DIR}/build/staging"
VOLUME_NAME="MacPanel"

# DMG 里必须装的是「PKG 脚本实际产出的那个文件」。
# 早先这里自行计算版本号，与 build-pkg.sh 的规则不一致
# （那边无 tag 时固化成 0.1.0，这边用 git describe 得到提交哈希），
# 结果是 DMG 去找一个根本不存在的 pkg 然后必然失败。
# 现在改为按实际产物推导，版本以产包为准。
PKGS="$(cd "${REPO_ROOT}" 2>/dev/null && ls MacPanel-*.pkg 2>/dev/null | sort -V)"
COUNT="$(printf '%s\n' "${PKGS}" | grep -c . || true)"

if [[ "${COUNT}" -eq 0 ]]; then
	echo "错误: 未找到任何 MacPanel-*.pkg，请先运行 make pkg（或 ./packaging/macos/pkg/build-pkg.sh）" >&2
	exit 1
fi
if [[ "${COUNT}" -gt 1 ]]; then
	echo "发现多个 PKG：$(printf '%s\n' "${PKGS}" | tr '\n' ' ')"
	echo "   取最新的一个打包；旧包建议手工删除，避免分发错版本。"
fi

# macOS 自带 bash 3.2 不支持 negative subscript（arr[-1]），用 tail 取最后一个。
PKG_FILE="${REPO_ROOT}/$(printf '%s\n' "${PKGS}" | tail -1)"
VERSION="${PKG_FILE##*MacPanel-}"
VERSION="${VERSION%.pkg}"
DMG_FILE="${REPO_ROOT}/MacPanel-${VERSION}.dmg"

if [[ ! -f "${PKG_FILE}" ]]; then
	echo "错误: 未找到 ${PKG_FILE}，请先运行 ./packaging/macos/pkg/build-pkg.sh" >&2
	exit 1
fi

echo "==> 准备暂存目录"
rm -rf "${STAGING}" "${DMG_FILE}"
mkdir -p "${STAGING}"
cp "${PKG_FILE}" "${STAGING}/"

# 安装说明随 DMG 一起分发
cat > "${STAGING}/安装说明.txt" <<'README'
MacPanel — 安装说明

一、安装
  1. 双击 MacPanel-<版本>.pkg，按向导完成安装。
  2. 安装器会：
     - 创建专用非特权账户 _macpanel（Web 服务以该身份运行）
     - 把二进制安装到 /usr/local/bin/macpanel
     - 部署两个 launchd 服务（agent 以 root、server 以 _macpanel）
     - 生成 IPC 共享令牌（0600 / 040 权限，仅授权账户可读）
  3. 安装完成后打开 http://127.0.0.1:7788

二、安全提示
  - 首次安装不会启用任何 PF 规则：IPv6 防火墙需要你在面板里显式编辑并应用。
  - 应用规则会启动 60 秒确认倒计时；超时未确认会自动回滚。
  - 若规则导致 SSH 失联，在本机终端执行（详见 docs/recovery.md 3.8）：
      sudo /usr/local/bin/macpanel firewall status
      sudo /usr/local/bin/macpanel firewall rollback
  - PF 状态需要 root 才能读取；Agent 以 root 运行后可读取。
  - 默认只监听 127.0.0.1。启用远程访问前必须先设置管理密码并配置 HTTPS。

三、验证安装
  launchctl print system/com.macpanel.agent | head -20
  launchctl print system/com.macpanel.server | head -20
  curl -s http://127.0.0.1:7788/api/v1/healthz
  sudo /usr/local/bin/macpanel firewall status

四、卸载
  sudo /usr/local/bin/macpanel-uninstall
  或参考 docs/recovery.md 第 4 节。

五、签名说明
  当前包未签名，Gatekeeper 可能提示「无法验证开发者」。
  在「系统设置 → 隐私与安全性」中允许即可，或自行签名后再分发。
README

echo "==> 创建临时 DMG"
# hdiutil 在 macOS 27 上仍可用但已标记 deprecated；
# 换成 diskutil image create 时记得校验 UDRW→UDZO 的压缩步骤能否一次到位。
TMP_DMG="${SCRIPT_DIR}/build/temp.dmg"
hdiutil create -srcfolder "${STAGING}" -volname "${VOLUME_NAME}" -fs HFS+ \
	-format UDRW -ov "${TMP_DMG}" >/dev/null

echo "==> 压缩为最终 DMG"
hdiutil convert "${TMP_DMG}" -format UDZO -imagekey zlib-level=9 -o "${DMG_FILE}" >/dev/null
rm -f "${TMP_DMG}"

echo ""
echo "已生成: ${DMG_FILE}"
echo "大小: $(du -h "${DMG_FILE}" | cut -f1)"
echo ""
echo "提示：分发前建议对 DMG 与内部 PKG 做 Developer ID 签名与公证，否则用户需手动放行。"
