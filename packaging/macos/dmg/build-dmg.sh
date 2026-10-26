#!/bin/bash
#
# 构建 DMG 分发容器（内含 PKG 安装器）。
#
# 用法：
#   ./packaging/macos/dmg/build-dmg.sh
#
# 前置条件：
#   - 已执行 ./packaging/macos/pkg/build-pkg.sh，生成 HomeLabManager-<version>.pkg
#
# 注意：本脚本只生成 DMG，不会挂载到 /Applications，也不会安装任何内容。

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
VERSION="$(cd "${REPO_ROOT}" && git describe --tags --always --dirty 2>/dev/null || echo "0.1.0-dev")"

PKG_FILE="${REPO_ROOT}/HomeLabManager-${VERSION}.pkg"
DMG_FILE="${REPO_ROOT}/HomeLabManager-${VERSION}.dmg"
STAGING="${SCRIPT_DIR}/build/staging"
VOLUME_NAME="HomeLabManager"

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
HomeLab Manager — 安装说明

一、安装
  1. 双击 HomeLabManager-<版本>.pkg，按向导完成安装。
  2. 安装器会：
     - 创建专用非特权账户 _homelab（Web 服务以该身份运行）
     - 把二进制安装到 /usr/local/bin/homelab-manager
     - 部署两个 launchd 服务（agent 以 root、server 以 _homelab）
     - 生成 IPC 共享令牌（0600 / 040 权限，仅授权账户可读）
  3. 安装完成后打开 http://127.0.0.1:7788

二、安全提示
  - 本版本（M1）完全只读，不会添加任何 PF 防火墙规则。
  - PF 状态需要 root 才能读取；Agent 以 root 运行后可读取。
  - 默认只监听 127.0.0.1。启用远程访问前必须先设置管理密码并配置 HTTPS。

三、验证安装
  launchctl print system/com.homelab.manager.agent | head -20
  curl -s http://127.0.0.1:7788/api/v1/healthz

四、卸载
  使用随包分发的 uninstall.sh，或参考 docs/recovery.md。

五、签名说明
  当前包未签名，Gatekeeper 可能提示「无法验证开发者」。
  在「系统设置 → 隐私与安全性」中允许即可，或自行签名后再分发。
README

echo "==> 创建临时 DMG"
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
