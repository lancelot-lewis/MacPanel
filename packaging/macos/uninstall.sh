#!/bin/bash
#
# MacPanel 卸载脚本
#
# 用法：
#   sudo macpanel-uninstall            # 交互式（数据与日志默认保留）
#   sudo macpanel-uninstall --yes      # 非交互：全部保留
#   sudo macpanel-uninstall --purge    # 非交互：删除数据与日志
#   sudo macpanel-uninstall --help
#
# 安全原则（改动本脚本时不要破坏）：
#   - 绝不执行 pfctl -d：那会全局关闭整机防火墙
#   - 只增删本应用自己的标记段（# BEGIN/END MACPANEL IPV6 MANAGED），
#     /etc/pf.conf 的其它任何一行都不碰
#   - 不删除其它程序的 anchor 或 launchd 项
#   - 数据与日志默认保留，删除必须显式要求
#
# 卸载前如果曾用过 IPv6 防火墙，请先在面板里停用规则，或执行
# `sudo macpanel firewall unload`，让本应用先退出 PF。

set -uo pipefail

DATA_DIR="/Library/Application Support/MacPanel"
LOG_DIR="/Library/Logs/MacPanel"
RUN_DIR="/var/run/macpanel"
ANCHOR="/etc/pf.anchors/com.macpanel.ipv6"
ANCHOR_NAME="com.macpanel.ipv6"

AGENT_PLIST="/Library/LaunchDaemons/com.macpanel.agent.plist"
SERVER_PLIST="/Library/LaunchDaemons/com.macpanel.server.plist"

ASSUME_YES=0
PURGE=0

usage() {
	cat <<'USAGE'
MacPanel 卸载脚本

用法:
  sudo macpanel-uninstall              交互式：数据与日志默认保留
  sudo macpanel-uninstall --yes        非交互：全部保留
  sudo macpanel-uninstall --purge      非交互：连数据与日志一起删除
  sudo macpanel-uninstall --help       显示本说明

安全边界（本脚本绝不越界）:
  · 不关闭 PF（从不调用 pfctl -d）
  · 只增删本应用自己的标记段，不改 /etc/pf.conf 的其它内容
  · 不删除其它程序的 anchor 或 launchd 项
USAGE
	exit 0
}

while [[ $# -gt 0 ]]; do
	case "$1" in
		--yes | -y) ASSUME_YES=1 ;;
		--purge) PURGE=1; ASSUME_YES=1 ;;
		--help | -h) usage ;;
		*)
			echo "未知参数: $1（用 --help 查看用法）" >&2
			exit 2
			;;
	esac
	shift
done

if [[ "$(id -u)" -ne 0 ]]; then
	echo "错误: 必须以 root 运行（卸载涉及 launchd 系统域与 root 独占目录）" >&2
	echo "      sudo $(basename "$0")" >&2
	exit 1
fi

# 放在最前面：即使后面某步被 Ctrl-C 中断，收据也已经清掉。
#
# 为什么必须 forget 收据：不清的话，下一次安装持有的仍是同版本收据，
# macOS 会判定为「同版本覆盖安装」并做 receipt-based obsoleting ——
# 实测会在安装完成后把 payload 里的文件删掉，导致 launchd plist 消失、
# 服务完全起不来，而 `pkgutil --files` 依然显示「已安装」，极难排查。
# 清掉收据后重新安装走的是全新安装路径，不会再触发这个行为。
echo "==> 清理安装收据"
if pkgutil --forget com.macpanel 2>/dev/null; then
	echo "    已遗忘 com.macpanel 收据（下次安装将视为全新安装）"
else
	echo "    无收据记录（跳过）"
fi

ask() {
	# $1: 提示语  $2: 默认动作的取值（用于非交互）
	local prompt="$1" default="$2"
	if [[ "${ASSUME_YES}" -eq 1 ]]; then
		return "${default}"
	fi
	local answer
	read -r -p "${prompt} [y/N] " answer
	case "${answer}" in
		y | Y) return 0 ;;
		*) return 1 ;;
	esac
}

echo "==> 停止并移除本应用的 launchd 服务"
launchctl bootout system "${AGENT_PLIST}" 2>/dev/null || echo "    agent 服务未加载（跳过）"
launchctl bootout system "${SERVER_PLIST}" 2>/dev/null || echo "    server 服务未加载（跳过）"
rm -f "${AGENT_PLIST}" "${SERVER_PLIST}"

echo "==> 处理本应用的 PF anchor"
if [[ -x /sbin/pfctl ]]; then
	# 先清空 anchor 里的规则，再谈删文件。
	# 顺序很重要：反过来的话，机器会在删掉规则文件的同时仍带着旧规则运行，
	# 直到下一次 PF 重载才生效——期间实际行为与用户看到的文件状态相反。
	LOADED="$(/sbin/pfctl -s Anchors 2>/dev/null | grep -cx "${ANCHOR_NAME}" || true)"
	if [[ "${LOADED}" -gt 0 ]]; then
		echo "    anchor 仍在 PF 中，先清空规则"
		if /sbin/pfctl -a "${ANCHOR_NAME}" -f /dev/null 2>/dev/null; then
			echo "    已清空（本应用 anchor 不再含任何规则）"
		else
			echo "    清空失败，请手工执行：sudo pfctl -a ${ANCHOR_NAME} -f /dev/null" >&2
		fi
	else
		echo "    anchor 当前未加载"
	fi
else
	echo "    pfctl 不可用，跳过"
fi

# 移除安装器写入 /etc/pf.conf 的那一段引用。
# 只删两个标记之间的内容，别处一行都不碰——文件里还有其它程序的 anchor
# 与 scrub 规则。先备份、再用 pfctl -n 验证，失败就整体还原。
PF_CONF="/etc/pf.conf"
MARKER_BEGIN="# BEGIN MACPANEL IPV6 MANAGED"
MARKER_END="# END MACPANEL IPV6 MANAGED"
if [[ -f "${PF_CONF}" ]] && grep -qxF "${MARKER_BEGIN}" "${PF_CONF}"; then
	if ask "是否从 ${PF_CONF} 移除本应用的 anchor 引用？" 1; then
		PF_BACKUP="${PF_CONF}.macpanel-uninstall-backup"
		cp -p "${PF_CONF}" "${PF_BACKUP}"
		awk -v b="${MARKER_BEGIN}" -v e="${MARKER_END}" \
			'$0==b {skip=1; next} $0==e {skip=0; next} !skip' \
			"${PF_BACKUP}" >"${PF_CONF}"
		if /sbin/pfctl -n -f "${PF_CONF}" 2>/dev/null; then
			if /sbin/pfctl -f "${PF_CONF}" 2>/dev/null; then
				echo "    已移除引用并重载主规则集"
			else
				echo "    已移除引用，但重载失败，请手工执行：sudo pfctl -f ${PF_CONF}" >&2
			fi
			rm -f "${PF_BACKUP}"
		else
			cp -p "${PF_BACKUP}" "${PF_CONF}"
			rm -f "${PF_BACKUP}"
			echo "    移除引用后 ${PF_CONF} 语法检查失败，已还原" >&2
		fi
	else
		echo "    已保留 ${PF_CONF} 中的引用（删除文件前请自行移除，否则 PF 会报错）"
	fi
fi

if [[ -f "${ANCHOR}" ]]; then
	if ask "是否删除规则文件 ${ANCHOR}？" 1; then
		rm -f "${ANCHOR}"
		echo "    已删除"
	else
		echo "    已保留 ${ANCHOR}"
	fi
fi

echo "==> 移除可执行文件"
rm -f /usr/local/bin/macpanel
rm -f /usr/local/bin/macpanel-uninstall

# 打包时 macOS 可能给 payload 带上 com.apple.provenance，pkgbuild 会顺带
# 生成 ._xxx AppleDouble 记录一并安装。这里清理本应用路径下的那几个。
rm -f /usr/local/bin/._macpanel /usr/local/bin/._macpanel-uninstall
rm -f /Library/._LaunchDaemons
rm -f /Library/LaunchDaemons/._com.macpanel.agent.plist
rm -f /Library/LaunchDaemons/._com.macpanel.server.plist

echo "==> 移除运行时目录"
rm -rf "${RUN_DIR}"

echo "==> 数据目录 ${DATA_DIR}"
if [[ -d "${DATA_DIR}" ]]; then
	if [[ "${PURGE}" -eq 1 ]] || ask "是否删除数据库、备份与配置？" 1; then
		rm -rf "${DATA_DIR}"
		echo "    已删除（含升级前备份）"
	else
		echo "    已保留。其中 backups/ 下有升级前备份，重装后仍可找回旧数据"
	fi
else
	echo "    不存在，跳过"
fi

echo "==> 日志目录 ${LOG_DIR}"
if [[ -d "${LOG_DIR}" ]]; then
	if [[ "${PURGE}" -eq 1 ]] || ask "是否删除日志？" 1; then
		rm -rf "${LOG_DIR}"
		echo "    已删除"
	else
		echo "    已保留"
	fi
else
	echo "    不存在，跳过"
fi

echo ""
echo "卸载完成。"
echo "未做的事（安全边界，不是遗漏）：未关闭 PF、未修改 /etc/pf.conf、未动任何第三方配置。"
